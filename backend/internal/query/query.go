// Package query is distributed query: a coordinator splits a query's segments
// into fragments, executors run each fragment's SQL in DuckDB and write
// Parquet results, and the coordinator merges them with a final SQL.
//
// Segments go to executors by rendezvous hashing, so each keeps hitting the
// same executor's warm cache while membership is stable. Remote executors
// (SERVICE_MODE=querier) are dispatched to over NATS and return results over
// gRPC; without any, everything runs in-process.
package query

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"obzly/internal/storage/segments"
	"obzly/internal/telemetry"

	"github.com/google/uuid"
)

// SourcePlaceholder in a fragment's SQL is replaced by the executor with the
// read expression over the fragment's files.
const SourcePlaceholder = "{{source}}"

// ResultsPlaceholder in the final SQL is replaced with the read expression
// over every fragment's result.
const ResultsPlaceholder = "{{results}}"

var (
	// ErrNoFiles: a fragment had no readable file, so no result.
	ErrNoFiles = errors.New("fragment has no readable files")
	// ErrNoData: no fragment had a readable file.
	ErrNoData = errors.New("query has no readable files")
	// ErrScanBudget: the query planned more bytes than its ScanBudget
	// (a *ScanBudgetError says how many).
	ErrScanBudget = errors.New("query scan budget exceeded")
)

// ScanBudgetError is a query refused before reading anything.
type ScanBudgetError struct {
	Planned, Budget int64
}

const (
	// fragmentTimeout bounds one fragment, retries included: a wide search on
	// cold queriers reads everything from object storage. Under the control
	// plane's 180s proxy timeout, so a timeout still reaches the user as a 408.
	fragmentTimeout = 170 * time.Second
	// maxRemoteAttempts: a fragment's segments try this many remote
	// executors (rendezvous rank order) before running in-process.
	maxRemoteAttempts = 2
	// staleQueryDirAge: well past fragmentTimeout, so no live query's
	// results are removed.
	staleQueryDirAge = 10 * time.Minute
)

// Fragment is one executor's share of a query. Args hold only plain values
// (strings, numbers, bools, times) so a fragment can travel to a remote
// executor; LocalFiles (the query-time tail) only ever run in-process.
type Fragment struct {
	ID         string
	Stream     string
	Segments   []*segments.SegmentRecord
	LocalFiles []segments.ResolvedFile
	SQL        string
	Args       []any
	// Tokens: see Request.Tokens.
	Tokens []string
}

// Executor runs fragments.
type Executor interface {
	// ID is stable for the executor's lifetime (how it is addressed).
	ID() string
	// Execute runs fragment and writes its rows to a Parquet file at
	// resultPath.
	Execute(ctx context.Context, fragment Fragment, resultPath string) error
}

// Coordinator runs queries over executors.
type Coordinator struct {
	local  Executor
	remote func() []Executor
	dir    string
	db     *sql.DB
	log    *slog.Logger
}

// Request is one distributed query: FragmentSQL runs over each fragment's
// files (SourcePlaceholder), FinalSQL over every fragment's result
// (ResultsPlaceholder). Arguments bind in text order around the source's own
// (Render); FinalSQL may have no parameter before ResultsPlaceholder.
// LocalFiles come first in the read order, then Segments in their given order
// (newest-first searches rely on it).
type Request struct {
	Stream       string
	Segments     []*segments.SegmentRecord
	LocalFiles   []segments.ResolvedFile
	FragmentSQL  string
	FragmentArgs []any
	FinalSQL     string
	FinalArgs    []any
	// SingleSQL, if set, is the whole query over SourcePlaceholder (same columns
	// as FinalSQL), run when the plan is one in-process fragment. Otherwise
	// FinalSQL wraps FragmentSQL, which for a deep offset re-sorts offset+limit
	// full rows (a quota pass ran out of memory that way).
	SingleSQL  string
	SingleArgs []any
	// SingleFragment runs the query as one in-process fragment (e.g. a deep
	// offset, whose per-fragment results would each be as large as the page).
	SingleFragment bool
	// Slices, if set, replace Segments, LocalFiles and the fragment SQL:
	// each is a fragment with its own SQL that must run whole (one
	// executor, retried whole), e.g. a time slice holding every sample its
	// results need. FinalSQL combines their results.
	Slices []Slice
	// ScanBudget refuses the query (ErrScanBudget) when its segments exceed this
	// many compressed bytes; 0 for none. Only for user-requested full scans, not
	// page reads (a top-N reads far less than it plans) or background work.
	ScanBudget int64
	// Tokens every matching row holds (ID-like search terms): executors
	// skip segments whose token filter lacks one (segments.SkipByTokens).
	Tokens []string
}

// Slice is one whole fragment of a sliced Request. It runs in-process when
// it has local files or a segment not uploaded yet.
type Slice struct {
	Segments   []*segments.SegmentRecord
	LocalFiles []segments.ResolvedFile
	SQL        string
	Args       []any
}

// Assignment is one executor's segments.
type Assignment struct {
	Executor Executor
	Segments []*segments.SegmentRecord
}

// NewCoordinator merges on db, with fragment results in dir. local runs the
// local files, segments not in object storage, and everything when remote
// (called per query; nil for none) lists no executors.
func NewCoordinator(
	dir string,
	db *sql.DB,
	local Executor,
	remote func() []Executor,
	log *slog.Logger,
) (*Coordinator, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create query results directory: %w", err)
	}
	removeStaleQueryDirs(dir)
	if remote == nil {
		remote = func() []Executor { return nil }
	}
	return &Coordinator{local: local, remote: remote, dir: dir, db: db, log: log}, nil
}

// Query runs request and passes the final rows to scan, or returns
// ErrNoData. Fragment results are removed afterwards.
func (c *Coordinator) Query(ctx context.Context, request Request, scan func(*sql.Rows) error) error {
	var planned int64
	for _, record := range request.Segments {
		planned += record.SizeBytes
	}
	for _, slice := range request.Slices {
		for _, record := range slice.Segments {
			planned += record.SizeBytes
		}
	}
	telemetry.QueryPlannedBytes.WithLabelValues(request.Stream).Observe(float64(planned))
	if request.ScanBudget > 0 && planned > request.ScanBudget {
		telemetry.QueryRejected.WithLabelValues(request.Stream, "scan_budget").Inc()
		return &ScanBudgetError{Planned: planned, Budget: request.ScanBudget}
	}

	parts := c.plan(request)
	if len(parts) == 0 {
		return ErrNoData
	}

	queryDir, err := os.MkdirTemp(c.dir, "query-")
	if err != nil {
		return fmt.Errorf("failed to create query results directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(queryDir) }()

	// One fragment runs the final SQL itself, so its result is the answer:
	// no merge, and a deep page isn't written out whole. A retry can split
	// it, so that holds only in-process.
	fragmentSQL, fragmentArgs := request.FragmentSQL, request.FragmentArgs
	finalSQL, finalArgs := request.FinalSQL, request.FinalArgs
	if len(parts) == 1 && parts[0].executor == nil && len(request.Slices) == 0 {
		if request.SingleSQL != "" {
			fragmentSQL, fragmentArgs = request.SingleSQL, request.SingleArgs
		} else {
			fragmentSQL = strings.Replace(finalSQL, ResultsPlaceholder, "("+fragmentSQL+")", 1)
			fragmentArgs = append(append([]any{}, fragmentArgs...), finalArgs...)
		}
		finalSQL, finalArgs = "SELECT * FROM "+ResultsPlaceholder, nil
	}

	run := &fragmentRun{
		coordinator: c, dir: queryDir, stream: request.Stream, sql: fragmentSQL, args: fragmentArgs,
		tokens: request.Tokens,
	}
	fragmentCtx, cancel := context.WithTimeout(ctx, fragmentTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for _, p := range parts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			run.execute(fragmentCtx, p, nil)
		}()
	}
	wg.Wait()
	if err := errors.Join(run.errs...); err != nil {
		return fmt.Errorf("query fragment failed: %w", err)
	}
	if len(run.results) == 0 {
		return ErrNoData
	}

	quoted := make([]string, len(run.results))
	for i, path := range run.results {
		quoted[i] = "'" + strings.ReplaceAll(path, "'", "''") + "'"
	}
	finalSQL = strings.Replace(finalSQL, ResultsPlaceholder,
		"read_parquet(["+strings.Join(quoted, ", ")+"], union_by_name=true)", 1)
	rows, err := c.db.QueryContext(ctx, finalSQL, finalArgs...)
	if err != nil {
		return fmt.Errorf("failed to merge query fragments: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if err := scan(rows); err != nil {
		return err
	}
	return rows.Err()
}

// Assign groups segments into one fragment per executor by rendezvous
// (highest random weight) hashing of (segment, executor): a segment stays on
// the same executor while membership is stable, and adding or removing an
// executor moves only about 1/N of segments. Segments keep their order
// within a fragment.
func Assign(records []*segments.SegmentRecord, executors []Executor) []Assignment {
	if len(records) == 0 || len(executors) == 0 {
		return nil
	}
	byExecutor := map[string]*Assignment{}
	var order []string
	for _, record := range records {
		best := Ranked(record, executors)[0]
		group, ok := byExecutor[best.ID()]
		if !ok {
			group = &Assignment{Executor: best}
			byExecutor[best.ID()] = group
			order = append(order, best.ID())
		}
		group.Segments = append(group.Segments, record)
	}
	result := make([]Assignment, 0, len(order))
	for _, id := range order {
		result = append(result, *byExecutor[id])
	}
	return result
}

// Ranked orders executors by rendezvous score for record, best first.
func Ranked(record *segments.SegmentRecord, executors []Executor) []Executor {
	key := record.ID.String()
	ranked := append([]Executor(nil), executors...)
	sort.SliceStable(ranked, func(i, j int) bool {
		si, sj := rendezvousScore(key, affinityOf(ranked[i])), rendezvousScore(key, affinityOf(ranked[j]))
		if si != sj {
			return si > sj
		}
		return ranked[i].ID() < ranked[j].ID()
	})
	return ranked
}

// part is one fragment to run: on a remote executor, or in-process when
// executor is nil. A slice's part has its own SQL and is never split.
type part struct {
	executor   Executor
	segments   []*segments.SegmentRecord
	localFiles []segments.ResolvedFile
	slice      *Slice
}

// fragmentRun collects one query's fragment results.
type fragmentRun struct {
	coordinator *Coordinator
	dir         string
	stream      string
	sql         string
	args        []any
	tokens      []string

	mu      sync.Mutex
	results []string
	errs    []error
}

// plan splits request into parts: local files and segments without an
// object key in-process, the rest by rendezvous over the remote executors.
func (c *Coordinator) plan(request Request) []part {
	if len(request.Slices) > 0 {
		return c.planSlices(request.Slices)
	}
	if len(request.Segments) == 0 && len(request.LocalFiles) == 0 {
		return nil
	}
	remote := c.remote()
	if request.SingleFragment || len(remote) == 0 {
		return []part{{segments: request.Segments, localFiles: request.LocalFiles}}
	}

	local := part{localFiles: request.LocalFiles}
	var dispatchable []*segments.SegmentRecord
	for _, record := range request.Segments {
		if record.ObjectKey == "" {
			local.segments = append(local.segments, record)
		} else {
			dispatchable = append(dispatchable, record)
		}
	}
	var parts []part
	if len(local.segments) > 0 || len(local.localFiles) > 0 {
		parts = append(parts, local)
	}
	for _, assigned := range Assign(dispatchable, remote) {
		parts = append(parts, part{executor: assigned.Executor, segments: assigned.Segments})
	}
	return parts
}

// RemoteExecutors is how many remote executors are live now.
func (c *Coordinator) RemoteExecutors() int {
	return len(c.remote())
}

// planSlices puts each slice on the executor ranked best for its largest
// segment (where it is most likely cached), preferring one no other slice
// has; in-process when it has local files or local-only segments.
func (c *Coordinator) planSlices(slices []Slice) []part {
	remote := c.remote()
	used := map[string]bool{}
	var parts []part
	for i := range slices {
		slice := &slices[i]
		if len(slice.Segments) == 0 && len(slice.LocalFiles) == 0 {
			continue
		}
		p := part{segments: slice.Segments, localFiles: slice.LocalFiles, slice: slice}
		if len(remote) > 0 && len(slice.LocalFiles) == 0 && allUploaded(slice.Segments) {
			ranked := Ranked(largest(slice.Segments), remote)
			p.executor = ranked[0]
			for _, candidate := range ranked {
				if !used[candidate.ID()] {
					p.executor = candidate
					break
				}
			}
			used[p.executor.ID()] = true
		}
		parts = append(parts, p)
	}
	return parts
}

// execute runs p. When a remote executor fails, its segments go to each
// one's best live executor not yet tried, up to maxRemoteAttempts, then
// in-process; fragments are read-only, so a retry is safe.
func (r *fragmentRun) execute(ctx context.Context, p part, failed map[string]bool) {
	resultPath := filepath.Join(r.dir, uuid.NewString()+".parquet")
	fragment := Fragment{
		ID: uuid.NewString(), Stream: r.stream, Segments: p.segments, LocalFiles: p.localFiles,
		SQL: r.sql, Args: r.args, Tokens: r.tokens,
	}
	if p.slice != nil {
		fragment.SQL, fragment.Args = p.slice.SQL, p.slice.Args
	}
	executor, where := p.executor, telemetry.QueryWhereRemote
	if executor == nil {
		executor, where = r.coordinator.local, telemetry.QueryWhereLocal
	}
	started := time.Now()
	err := executor.Execute(ctx, fragment, resultPath)
	telemetry.QueryFragmentDuration.WithLabelValues(where).Observe(time.Since(started).Seconds())
	switch {
	case errors.Is(err, ErrNoFiles):
		telemetry.QueryFragments.WithLabelValues(where, telemetry.ResultEmpty).Inc()
		return
	case err != nil:
		telemetry.QueryFragments.WithLabelValues(where, telemetry.ResultError).Inc()
	default:
		telemetry.QueryFragments.WithLabelValues(where, telemetry.ResultOK).Inc()
	}
	if err == nil {
		r.mu.Lock()
		r.results = append(r.results, resultPath)
		r.mu.Unlock()
		return
	}
	if p.executor == nil || ctx.Err() != nil {
		r.mu.Lock()
		r.errs = append(r.errs, err)
		r.mu.Unlock()
		return
	}

	telemetry.QueryFragmentRetries.Inc()
	r.coordinator.log.Warn("query fragment failed on an executor, retrying elsewhere",
		"executor", p.executor.ID(), "segments", len(p.segments), "error", err)
	failed = cloneWith(failed, p.executor.ID())
	var live []Executor
	for _, candidate := range r.coordinator.remote() {
		if !failed[candidate.ID()] {
			live = append(live, candidate)
		}
	}
	retries := []part{{segments: p.segments, slice: p.slice}}
	if p.slice != nil {
		if len(failed) < maxRemoteAttempts && len(live) > 0 {
			retries[0].executor = Ranked(largest(p.segments), live)[0]
		}
	} else if len(failed) < maxRemoteAttempts && len(live) > 0 {
		retries = nil
		for _, assigned := range Assign(p.segments, live) {
			retries = append(retries, part{executor: assigned.Executor, segments: assigned.Segments})
		}
	}
	var wg sync.WaitGroup
	for _, retry := range retries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.execute(ctx, retry, failed)
		}()
	}
	wg.Wait()
}

// removeStaleQueryDirs removes the results of queries a killed process
// never finished. Only old ones: dir may be shared by replicas.
func removeStaleQueryDirs(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-staleQueryDirAge)
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "query-") {
			continue
		}
		if info, err := entry.Info(); err == nil && info.ModTime().Before(cutoff) {
			_ = os.RemoveAll(filepath.Join(dir, entry.Name()))
		}
	}
}

func cloneWith(set map[string]bool, id string) map[string]bool {
	result := make(map[string]bool, len(set)+1)
	for key := range set {
		result[key] = true
	}
	result[id] = true
	return result
}

// Owned is the records ranked first for the executor with affinity among
// executors: what it will be sent to read while membership holds.
func Owned(records []*segments.SegmentRecord, executors []Executor, affinity string) []*segments.SegmentRecord {
	var owned []*segments.SegmentRecord
	for _, record := range records {
		if ranked := Ranked(record, executors); len(ranked) > 0 && affinityOf(ranked[0]) == affinity {
			owned = append(owned, record)
		}
	}
	return owned
}

// affinityOf is what an executor's share of segments is hashed by: its
// Affinity when it has one (remote executors), else its ID.
func affinityOf(executor Executor) string {
	if withAffinity, ok := executor.(interface{ Affinity() string }); ok {
		return withAffinity.Affinity()
	}
	return executor.ID()
}

// rendezvousScore mixes both hashes (splitmix64): plain FNV of keys that
// differ only in their last byte ("e1", "e2") sent nearly every segment to
// one executor.
func rendezvousScore(key, executor string) uint64 {
	return mix64(fnv64(key) ^ mix64(fnv64(executor)))
}

func fnv64(value string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(value))
	return h.Sum64()
}

func mix64(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

func (e *ScanBudgetError) Error() string {
	return fmt.Sprintf("this query would read %s of stored data, over the %s limit per query: "+
		"narrow the time range or add filters", formatBytes(e.Planned), formatBytes(e.Budget))
}

func (e *ScanBudgetError) Is(target error) bool {
	return target == ErrScanBudget
}

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value, suffix := float64(n), "B"
	for _, next := range []string{"KiB", "MiB", "GiB", "TiB", "PiB"} {
		if value < unit {
			break
		}
		value, suffix = value/unit, next
	}
	return fmt.Sprintf("%.1f %s", value, suffix)
}

// BudgetRefusal is the user-facing message of a scan-budget refusal
// anywhere in err's chain.
func BudgetRefusal(err error) (string, bool) {
	var refused *ScanBudgetError
	if errors.As(err, &refused) {
		return refused.Error(), true
	}
	return "", false
}

func allUploaded(records []*segments.SegmentRecord) bool {
	for _, record := range records {
		if record.ObjectKey == "" {
			return false
		}
	}
	return true
}

func largest(records []*segments.SegmentRecord) *segments.SegmentRecord {
	best := records[0]
	for _, record := range records[1:] {
		if record.SizeBytes > best.SizeBytes {
			best = record
		}
	}
	return best
}
