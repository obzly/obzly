package ingest

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"obzly/internal/broker"
	"obzly/internal/storage/segments"
	"obzly/internal/telemetry"

	"github.com/google/uuid"
	_ "github.com/marcboeker/go-duckdb/v2"
)

// Query-time tail: rows not yet in an uploaded segment are still JetStream
// batches, so queries scan them as a few extra Parquet files; otherwise the
// newest INGEST_L0_MAX_AGE would be missing everywhere.
//
// Nothing runs in the background: a query finding its tail older than
// TailRefreshAfter refreshes it and waits for that, so it sees what was
// acked up to then. A refresh decodes only batches newer than the last one
// it saw, so on a replica queried often it is a few seconds of ingest; on a
// replica nobody queried since the project's last segment upload it is the
// whole pending window (at most about INGEST_L0_TARGET_BYTES), decoded in
// chunks by several writers at once and served chunk by chunk as they are
// written. Keeping tails warm in the background instead cost every API
// replica 1.3-1.9 cores in the homelab.
const (
	// TailRefreshAfter: a tail older than this is refreshed before a query
	// uses it.
	TailRefreshAfter   = 2 * time.Second
	tailRefreshTimeout = 30 * time.Second
	// TailMaxDeltas: more delta files than this are merged into one.
	TailMaxDeltas = 8
	// TailWaitForRefresh: how long a query waits for its tail's refresh.
	// Until 2026-11-26 a query used a stale tail without waiting (missing
	// everything since), and skipped it after this wait when a segment it
	// planned was newer (missing the newest minute or two on a replica not
	// queried since the project's last upload).
	TailWaitForRefresh = 3 * time.Second
	// TailMaxWaitForRefresh: a query whose refresh has read its batches and
	// is still writing them keeps waiting up to this long in all (the work
	// left is known and finite; a refresh still waiting on the broker isn't
	// waited for longer), then takes the chunks written so far.
	TailMaxWaitForRefresh = 8 * time.Second
	// tailWriters: writers per signal, so a refresh decodes this many chunks
	// at once (and every project's refreshes together no more).
	tailWriters = 4
	// tailMinChunkBytes: a refresh splits its new batches into chunks of at
	// least this much payload, one per writer.
	tailMinChunkBytes = 4 << 20
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
	signal  string
	dir     string
	log     *slog.Logger
	writers chan DeltaWriter
	merger  *sql.DB
	// wait, maxWait, minChunk: TailWaitForRefresh, TailMaxWaitForRefresh and
	// tailMinChunkBytes, shorter in tests.
	wait, maxWait time.Duration
	minChunk      int

	mu      sync.Mutex
	entries map[uuid.UUID]*tailEntry
	retired []retiredFile
}

type tailEntry struct {
	mu     sync.Mutex
	deltas []tailDelta
	// refreshedAt: when the last refresh that finished started. checkedAt:
	// when the latest refresh started that has dropped every delta whose
	// batches were acked by then; the deltas held since were all pending
	// after it, so none has rows of a segment readable before it.
	refreshedAt time.Time
	checkedAt   time.Time
	// refreshed is closed when the running refresh ends (nil: none runs);
	// progress is closed and replaced whenever writing changes. writing:
	// the running refresh's chunks not written yet, -1 until it has read
	// its batches.
	refreshed chan struct{}
	progress  chan struct{}
	writing   int
}

// tailDelta holds the rows of batches firstSeq..lastSeq, one file per
// stream that has any. subjects is the sequence range of its batches per
// subject: with lanes a project's batches are on several shards, each acked
// on its own, so whether a delta is still pending is decided per subject.
type tailDelta struct {
	firstSeq, lastSeq uint64
	subjects          map[string]seqRange
	files             map[string]*segments.Segment
}

type seqRange struct {
	first, last uint64
}

type retiredFile struct {
	path string
	at   time.Time
}

// NewTail serves signal's pending rows, written under dir (which it empties
// first) by tailWriters writers from newWriter.
func NewTail(signal, dir string, newWriter func() (DeltaWriter, error), log *slog.Logger) (*Tail, error) {
	if err := os.RemoveAll(dir); err != nil {
		return nil, fmt.Errorf("failed to clear tail directory: %w", err)
	}
	writers := make(chan DeltaWriter, tailWriters)
	for range tailWriters {
		write, err := newWriter()
		if err != nil {
			return nil, fmt.Errorf("failed to create tail writer: %w", err)
		}
		writers <- write
	}
	merger, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, fmt.Errorf("failed to open tail merge handle: %w", err)
	}
	if _, err := segments.ConfigureMemory(merger, segments.TailMergerMemoryLimit,
		filepath.Join(dir, "spill")); err != nil {
		_ = merger.Close()
		return nil, err
	}
	if err := segments.SetHandleLimits(merger, "", segments.TailMergerThreads); err != nil {
		_ = merger.Close()
		return nil, err
	}
	telemetry.RegisterDuckDB("tail_merger_"+signal, merger, segments.MemoryLimitBytes(segments.TailMergerMemoryLimit))
	return &Tail{signal: signal, dir: dir, log: log.With("signal", signal), writers: writers, merger: merger,
		wait: TailWaitForRefresh, maxWait: TailMaxWaitForRefresh, minChunk: tailMinChunkBytes,
		entries: map[uuid.UUID]*tailEntry{}}, nil
}

// TailFiles returns the project's pending rows of stream. A tail older than
// TailRefreshAfter, or built before a segment the query planned became
// readable (newestReadable, so it may hold that segment's rows), is
// refreshed first, waiting as long as TailWaitForRefresh and
// TailMaxWaitForRefresh allow; the query then gets every delta checked
// after newestReadable, all of them unless the refresh is still writing.
func (t *Tail) TailFiles(
	ctx context.Context,
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

	fresh := !entry.refreshedAt.IsZero() && time.Since(entry.refreshedAt) <= TailRefreshAfter &&
		newestReadable.Before(entry.refreshedAt)
	// A refresh that was already running may have checked its deltas before
	// newestReadable: then the next one is waited for too.
	for attempt := 0; !fresh && attempt < 2; attempt++ {
		t.startRefresh(projectID, entry)
		t.waitForRefresh(ctx, entry, newestReadable)
		if newestReadable.Before(entry.checkedAt) || entry.refreshed != nil || ctx.Err() != nil {
			break
		}
	}
	if entry.checkedAt.IsZero() || !newestReadable.Before(entry.checkedAt) {
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

// waitForRefresh waits until the running refresh has checked its deltas
// after newestReadable and written every chunk (a merge after that changes
// no rows): up to t.wait, or t.maxWait once it is writing. Call with
// entry.mu held; it is released while waiting.
func (t *Tail) waitForRefresh(ctx context.Context, entry *tailEntry, newestReadable time.Time) {
	start := time.Now()
	deadline := start.Add(t.wait)
	for entry.refreshed != nil && (entry.writing != 0 || !newestReadable.Before(entry.checkedAt)) {
		refreshed, progress := entry.refreshed, entry.progress
		entry.mu.Unlock()
		timer := time.NewTimer(time.Until(deadline))
		timedOut := false
		select {
		case <-refreshed:
		case <-progress:
		case <-timer.C:
			timedOut = true
		case <-ctx.Done():
		}
		timer.Stop()
		entry.mu.Lock()
		if ctx.Err() != nil {
			return
		}
		if timedOut {
			if entry.writing <= 0 || !deadline.Before(start.Add(t.maxWait)) {
				return
			}
			deadline = start.Add(t.maxWait)
		}
	}
}

// startRefresh runs one refresh at a time per project. Call with entry.mu
// held.
func (t *Tail) startRefresh(projectID uuid.UUID, entry *tailEntry) {
	if entry.refreshed != nil {
		return
	}
	refreshed := make(chan struct{})
	entry.refreshed = refreshed
	entry.progress = make(chan struct{})
	entry.writing = -1
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), tailRefreshTimeout)
		defer cancel()
		if err := t.refresh(ctx, projectID, entry); err != nil {
			t.log.Warn("Failed to refresh live rows", "project", projectID, "error", err)
		}
		entry.mu.Lock()
		entry.refreshed = nil
		entry.writing = 0
		entry.mu.Unlock()
		close(refreshed)
	}()
}

// refresh drops deltas whose batches workers have acked (written to a
// segment), re-reads a delta that is only partly acked, and adds deltas for
// batches newer than the last one seen. The deltas still pending are served
// as soon as that is checked, and each new one as soon as it is written.
func (t *Tail) refresh(ctx context.Context, projectID uuid.UUID, entry *tailEntry) error {
	client := broker.Default()
	if client == nil {
		return nil
	}
	startedAt := time.Now().UTC()

	entry.mu.Lock()
	old := entry.deltas
	entry.mu.Unlock()

	subjects := map[string]bool{}
	for _, delta := range old {
		for subject := range delta.subjects {
			subjects[subject] = true
		}
	}
	firsts, err := client.FirstPendingSeqs(ctx, t.signal, slices.Sorted(maps.Keys(subjects)))
	if err != nil {
		return err
	}

	var kept, partial, dropped []tailDelta
	from := uint64(1)
	for _, delta := range old {
		from = max(from, delta.lastSeq+1)
		switch deltaPending(delta, firsts) {
		case allPending:
			kept = append(kept, delta)
		case somePending:
			partial = append(partial, delta)
			dropped = append(dropped, delta)
		default:
			dropped = append(dropped, delta)
		}
	}
	entry.mu.Lock()
	entry.deltas = kept
	entry.checkedAt = startedAt
	entry.mu.Unlock()
	t.retire(dropped)

	if err := t.extend(ctx, projectID, entry, partial, from); err != nil {
		// A range not re-read now would be skipped by the next refresh,
		// which reads on from the newest delta: start over instead.
		entry.mu.Lock()
		cleared := entry.deltas
		entry.deltas = nil
		entry.mu.Unlock()
		t.retire(cleared)
		return err
	}
	entry.mu.Lock()
	entry.refreshedAt = startedAt
	entry.mu.Unlock()
	return nil
}

// extend re-reads the pending batches of partly acked deltas, adds the
// batches from seq from on, and merges past TailMaxDeltas.
func (t *Tail) extend(ctx context.Context, projectID uuid.UUID, entry *tailEntry, partial []tailDelta,
	from uint64,
) error {
	// Acked batches are gone from the stream: re-reading a partly acked
	// delta's range keeps only its pending ones.
	var jobs [][]broker.PendingMessage
	for _, delta := range partial {
		pending, err := t.read(ctx, projectID, delta.firstSeq, delta.lastSeq)
		if err != nil {
			return err
		}
		if len(pending) > 0 {
			jobs = append(jobs, pending)
		}
	}
	newer, err := t.read(ctx, projectID, from, 0)
	if err != nil {
		return err
	}
	jobs = append(jobs, t.chunks(newer)...)
	if err := t.writeAll(projectID, entry, jobs); err != nil {
		return err
	}

	entry.mu.Lock()
	deltas := slices.Clone(entry.deltas)
	entry.mu.Unlock()
	if len(deltas) <= TailMaxDeltas {
		return nil
	}
	merged, err := t.merge(projectID, deltas)
	if err != nil {
		return err
	}
	entry.mu.Lock()
	entry.deltas = []tailDelta{merged}
	entry.mu.Unlock()
	t.retire(deltas)
	return nil
}

// writeAll writes each job's batches as a delta on the writers, adding each
// to entry as it is done.
func (t *Tail) writeAll(projectID uuid.UUID, entry *tailEntry, jobs [][]broker.PendingMessage) error {
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		failed error
	)
	entry.mu.Lock()
	entry.setWriting(len(jobs))
	entry.mu.Unlock()
	for _, job := range jobs {
		wg.Go(func() {
			write := <-t.writers
			delta, err := t.writeDelta(write, projectID, job)
			t.writers <- write
			mu.Lock()
			defer mu.Unlock()
			entry.mu.Lock()
			defer entry.mu.Unlock()
			if err != nil {
				failed = errors.Join(failed, err)
			} else {
				entry.deltas = append(entry.deltas, delta)
				slices.SortFunc(entry.deltas, func(a, b tailDelta) int { return cmp.Compare(a.firstSeq, b.firstSeq) })
			}
			entry.setWriting(entry.writing - 1)
		})
	}
	wg.Wait()
	return failed
}

// chunks splits batches, in order, into about equal parts by payload size:
// one per writer, none under t.minChunk.
func (t *Tail) chunks(batches []broker.PendingMessage) [][]broker.PendingMessage {
	if len(batches) == 0 {
		return nil
	}
	total := 0
	for _, msg := range batches {
		total += len(msg.Data)
	}
	parts := min(tailWriters, max(1, total/max(t.minChunk, 1)), len(batches))
	var chunks [][]broker.PendingMessage
	start, size := 0, 0
	for i, msg := range batches {
		size += len(msg.Data)
		// Chunk k ends once (k+1)/parts of the bytes are in.
		if len(chunks) < parts-1 && size*parts >= total*(len(chunks)+1) {
			chunks = append(chunks, batches[start:i+1])
			start = i + 1
		}
	}
	if start < len(batches) {
		chunks = append(chunks, batches[start:])
	}
	return chunks
}

// read returns the project's pending batches fromSeq..upToSeq (0: to the
// end), up to tailMaxBytes.
func (t *Tail) read(ctx context.Context, projectID uuid.UUID, fromSeq, upToSeq uint64) (
	[]broker.PendingMessage, error,
) {
	pending, err := broker.Default().
		PendingIngestRange(ctx, t.signal, projectID, fromSeq, upToSeq, tailMaxBytes)
	if err != nil && !errors.Is(err, broker.ErrPendingTooLarge) {
		return nil, err
	}
	return pending, nil
}

type pendingState int

const (
	nonePending pendingState = iota
	somePending
	allPending
)

// deltaPending: how many of delta's batches are still pending, given each
// subject's oldest pending sequence (a shard acks oldest first).
func deltaPending(delta tailDelta, firsts map[string]uint64) pendingState {
	acked, pending := false, false
	for subject, seqs := range delta.subjects {
		first, ok := firsts[subject]
		switch {
		case !ok || seqs.last < first:
			acked = true
		case seqs.first < first:
			acked, pending = true, true
		default:
			pending = true
		}
	}
	switch {
	case !pending:
		return nonePending
	case acked:
		return somePending
	default:
		return allPending
	}
}

// writeDelta writes pending (at least one batch) to files with write.
func (t *Tail) writeDelta(write DeltaWriter, projectID uuid.UUID, pending []broker.PendingMessage) (
	tailDelta, error,
) {
	payloads := make([][]byte, len(pending))
	subjects := map[string]seqRange{}
	for i, msg := range pending {
		payloads[i] = msg.Data
		subjects[msg.Subject] = widen(subjects[msg.Subject], seqRange{msg.Seq, msg.Seq})
	}
	files, err := write(projectID, payloads)
	if err != nil {
		return tailDelta{}, err
	}
	return tailDelta{firstSeq: pending[0].Seq, lastSeq: pending[len(pending)-1].Seq, subjects: subjects,
		files: files}, nil
}

// merge combines deltas into one per stream with a Parquet-to-Parquet copy,
// so a query opens a handful of files at most.
func (t *Tail) merge(projectID uuid.UUID, deltas []tailDelta) (tailDelta, error) {
	merged := tailDelta{firstSeq: deltas[0].firstSeq, lastSeq: deltas[len(deltas)-1].lastSeq,
		subjects: map[string]seqRange{}, files: map[string]*segments.Segment{}}
	streams := map[string]bool{}
	for _, delta := range deltas {
		for stream := range delta.files {
			streams[stream] = true
		}
		for subject, seqs := range delta.subjects {
			merged.subjects[subject] = widen(merged.subjects[subject], seqs)
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

// setWriting records the chunks left to write and wakes the waiting
// queries. Call with e.mu held.
func (e *tailEntry) setWriting(chunks int) {
	e.writing = chunks
	close(e.progress)
	e.progress = make(chan struct{})
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

// widen is the range covering both (an unset range is zero).
func widen(current, more seqRange) seqRange {
	if current == (seqRange{}) {
		return more
	}
	return seqRange{min(current.first, more.first), max(current.last, more.last)}
}
