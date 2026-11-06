package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"obzly/internal/broker"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	_ "github.com/marcboeker/go-duckdb/v2"
)

// Query-time tail: rows not yet in an uploaded segment are still JetStream
// batches, so queries scan them as a few extra Parquet files; otherwise the
// newest INGEST_L0_MAX_AGE would be missing everywhere.
//
// Queries never wait for the broker: they use the files built so far and
// trigger a background refresh that decodes only newer batches (a full
// rebuild made every Logs query wait seconds).
const (
	// TailRefreshAfter: a tail older than this is refreshed in the background.
	TailRefreshAfter   = 2 * time.Second
	tailRefreshTimeout = 30 * time.Second
	// TailMaxDeltas: more delta files than this are merged into one.
	TailMaxDeltas = 8
	// tailMaxBytes bounds one read; a worker flushes a project at
	// INGEST_L0_TARGET_BYTES, so pending batches rarely exceed it.
	tailMaxBytes = 256 << 20
	// tailFileGrace keeps a replaced file for queries already reading it.
	tailFileGrace = time.Minute
)

// DeltaWriter decodes one project's pending batches and writes their rows to
// Parquet files that are not in the catalog, one per stream with rows.
type DeltaWriter func(projectID uuid.UUID, payloads [][]byte) (map[string]*segments.Segment, error)

// Tail builds and caches per-project tail files for one signal.
type Tail struct {
	signal string
	dir    string
	log    *slog.Logger
	write  DeltaWriter
	merger *sql.DB

	mu      sync.Mutex
	entries map[uuid.UUID]*tailEntry
	retired []retiredFile
}

type tailEntry struct {
	mu          sync.Mutex
	deltas      []tailDelta
	refreshedAt time.Time
	refreshing  bool
}

// tailDelta holds the rows of batches firstSeq..lastSeq, one file per
// stream that has any.
type tailDelta struct {
	firstSeq, lastSeq uint64
	files             map[string]*segments.Segment
}

type retiredFile struct {
	path string
	at   time.Time
}

// NewTail serves signal's pending rows, written by write under dir (which it
// empties first).
func NewTail(signal, dir string, write DeltaWriter, log *slog.Logger) (*Tail, error) {
	if err := os.RemoveAll(dir); err != nil {
		return nil, fmt.Errorf("failed to clear tail directory: %w", err)
	}
	merger, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, fmt.Errorf("failed to open tail merge handle: %w", err)
	}
	return &Tail{signal: signal, dir: dir, log: log.With("signal", signal), write: write, merger: merger,
		entries: map[uuid.UUID]*tailEntry{}}, nil
}

// TailFiles returns the project's pending rows of stream built so far. It
// returns nothing when no tail is built yet or when a segment the query
// planned became readable (newestReadable) after the last refresh - those
// rows may be in both - and refreshes in the background when stale.
func (t *Tail) TailFiles(
	_ context.Context,
	projectID uuid.UUID,
	stream string,
	newestReadable time.Time,
) []segments.TailFile {
	if broker.Default() == nil {
		return nil
	}
	entry := t.entry(projectID)
	entry.mu.Lock()
	defer entry.mu.Unlock()

	usable := !entry.refreshedAt.IsZero() && newestReadable.Before(entry.refreshedAt)
	if !usable || time.Since(entry.refreshedAt) > TailRefreshAfter {
		t.startRefresh(projectID, entry)
	}
	if !usable {
		return nil
	}

	var files []segments.TailFile
	for _, delta := range entry.deltas {
		if file := delta.files[stream]; file != nil {
			files = append(files, segments.TailFile{Path: file.Path, MinTime: file.MinTime, MaxTime: file.MaxTime})
		}
	}
	return files
}

// startRefresh runs one background refresh at a time per project. Call
// with entry.mu held.
func (t *Tail) startRefresh(projectID uuid.UUID, entry *tailEntry) {
	if entry.refreshing {
		return
	}
	entry.refreshing = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), tailRefreshTimeout)
		defer cancel()
		if err := t.refresh(ctx, projectID, entry); err != nil {
			t.log.Warn("Failed to refresh live rows", "project", projectID, "error", err)
		}
		entry.mu.Lock()
		entry.refreshing = false
		entry.mu.Unlock()
	}()
}

// refresh drops deltas whose batches a worker has acked (written to a
// segment), re-reads a delta that is only partly acked, and adds a delta
// for batches newer than the last one seen.
func (t *Tail) refresh(ctx context.Context, projectID uuid.UUID, entry *tailEntry) error {
	client := broker.Default()
	if client == nil {
		return nil
	}
	startedAt := time.Now().UTC()
	first, hasPending, err := client.FirstPendingSeq(ctx, t.signal, projectID)
	if err != nil {
		return err
	}

	entry.mu.Lock()
	old := entry.deltas
	entry.mu.Unlock()

	var kept, dropped []tailDelta
	var partial *tailDelta
	for i := range old {
		delta := old[i]
		switch {
		case !hasPending || delta.lastSeq < first:
			dropped = append(dropped, delta)
		case delta.firstSeq < first:
			partial = &delta
			dropped = append(dropped, delta)
		default:
			kept = append(kept, delta)
		}
	}

	var added []tailDelta
	if hasPending {
		if partial != nil {
			delta, err := t.buildDelta(ctx, projectID, first, partial.lastSeq)
			if err != nil {
				return err
			}
			if delta != nil {
				added = append(added, *delta)
			}
		}
		from := first
		for _, delta := range append(append([]tailDelta{}, kept...), added...) {
			from = max(from, delta.lastSeq+1)
		}
		delta, err := t.buildDelta(ctx, projectID, from, 0)
		if err != nil {
			return err
		}
		if delta != nil {
			added = append(added, *delta)
		}
	}

	deltas := append(kept, added...)
	sort.Slice(deltas, func(i, j int) bool { return deltas[i].firstSeq < deltas[j].firstSeq })
	if len(deltas) > TailMaxDeltas {
		merged, err := t.merge(projectID, deltas)
		if err != nil {
			return err
		}
		dropped = append(dropped, deltas...)
		deltas = []tailDelta{merged}
	}

	entry.mu.Lock()
	entry.deltas = deltas
	entry.refreshedAt = startedAt
	entry.mu.Unlock()
	t.retire(dropped)
	return nil
}

// buildDelta writes batches fromSeq..upToSeq (0: to the end) to files; nil
// when there are none.
func (t *Tail) buildDelta(ctx context.Context, projectID uuid.UUID, fromSeq, upToSeq uint64) (*tailDelta, error) {
	pending, err := broker.Default().
		PendingIngestRange(ctx, t.signal, projectID, fromSeq, upToSeq, tailMaxBytes)
	if err != nil && !errors.Is(err, broker.ErrPendingTooLarge) {
		return nil, err
	}
	if len(pending) == 0 {
		return nil, nil
	}

	payloads := make([][]byte, len(pending))
	for i, msg := range pending {
		payloads[i] = msg.Data
	}
	files, err := t.write(projectID, payloads)
	if err != nil {
		return nil, err
	}
	return &tailDelta{firstSeq: pending[0].Seq, lastSeq: pending[len(pending)-1].Seq, files: files}, nil
}

// merge combines deltas into one per stream with a Parquet-to-Parquet copy,
// so a query opens a handful of files at most.
func (t *Tail) merge(projectID uuid.UUID, deltas []tailDelta) (tailDelta, error) {
	merged := tailDelta{firstSeq: deltas[0].firstSeq, lastSeq: deltas[len(deltas)-1].lastSeq,
		files: map[string]*segments.Segment{}}
	streams := map[string]bool{}
	for _, delta := range deltas {
		for stream := range delta.files {
			streams[stream] = true
		}
	}
	for stream := range streams {
		var paths []string
		file := &segments.Segment{ProjectID: projectID, Stream: stream}
		for _, delta := range deltas {
			part := delta.files[stream]
			if part == nil {
				continue
			}
			paths = append(paths, "'"+strings.ReplaceAll(part.Path, "'", "''")+"'")
			if file.MinTime.IsZero() || part.MinTime.Before(file.MinTime) {
				file.MinTime = part.MinTime
			}
			if part.MaxTime.After(file.MaxTime) {
				file.MaxTime = part.MaxTime
			}
		}
		if len(paths) == 0 {
			continue
		}
		file.Path = filepath.Join(t.dir, stream, projectID.String(),
			fmt.Sprintf("merged_%d_%s.parquet", time.Now().UnixNano(), uuid.NewString()[:8]))
		query := fmt.Sprintf("COPY (SELECT * FROM read_parquet([%s], union_by_name=true)) TO '%s' (FORMAT PARQUET)",
			strings.Join(paths, ","), strings.ReplaceAll(file.Path, "'", "''"))
		if _, err := t.merger.Exec(query); err != nil {
			return tailDelta{}, fmt.Errorf("failed to merge tail files: %w", err)
		}
		merged.files[stream] = file
	}
	return merged, nil
}

func (t *Tail) entry(projectID uuid.UUID) *tailEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	entry, ok := t.entries[projectID]
	if !ok {
		entry = &tailEntry{}
		t.entries[projectID] = entry
	}
	return entry
}

// retire schedules dropped deltas' files for deletion and deletes those
// past the grace period.
func (t *Tail) retire(deltas []tailDelta) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	for _, delta := range deltas {
		for _, file := range delta.files {
			t.retired = append(t.retired, retiredFile{path: file.Path, at: now})
		}
	}
	kept := t.retired[:0]
	for _, file := range t.retired {
		if now.Sub(file.at) < tailFileGrace {
			kept = append(kept, file)
			continue
		}
		if err := os.Remove(file.path); err != nil && !os.IsNotExist(err) {
			t.log.Warn("Failed to remove an old tail file", "path", filepath.Base(file.path), "error", err)
		}
	}
	t.retired = kept
}
