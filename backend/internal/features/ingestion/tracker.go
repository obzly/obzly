package ingestion

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	// flushInterval bounds how stale "last seen" is (plus the status cache).
	flushInterval = 10 * time.Second
	flushTimeout  = 5 * time.Second
	pruneInterval = 10 * time.Minute
	// maxTrackedEntries bounds one window's memory; a source first seen once
	// it is full waits for the next window.
	maxTrackedEntries = 50_000
)

// Tracker counts what each source sends at accept time, in memory, and
// writes the counts every flushInterval. Each receiving replica has its
// own; writes add up across replicas.
type Tracker struct {
	repo   *repository
	logger *slog.Logger
	now    func() time.Time

	mu      sync.Mutex
	entries map[entryKey]*trackedEntry
	full    bool
}

type entryKey struct {
	projectID uuid.UUID
	sourceKey string
	signal    string
}

type trackedEntry struct {
	identifiedBy string
	attributes   map[string]string
	kind         kindHint
	first, last  time.Time
	// minutes: accepted count per UTC minute (Unix seconds).
	minutes map[int64]int64
}

// batch is a request's sources by identity key.
type batch map[string]*batchSource

// batchSource is one source's share of a request.
type batchSource struct {
	identity   sourceIdentity
	attributes map[string]string
	kind       kindHint
	count      int64
}

// Run flushes every flushInterval and prunes old rows every pruneInterval
// until ctx is done, then flushes once more.
func (t *Tracker) Run(ctx context.Context) {
	flushTicker := time.NewTicker(flushInterval)
	defer flushTicker.Stop()
	pruneTicker := time.NewTicker(pruneInterval)
	defer pruneTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.flushLogged()
			return
		case <-flushTicker.C:
			t.flushLogged()
		case <-pruneTicker.C:
			pruneCtx, cancel := context.WithTimeout(context.Background(), flushTimeout)
			if err := t.repo.prune(pruneCtx, t.now()); err != nil {
				t.logger.Warn("Failed to prune ingestion status rows", "error", err)
			}
			cancel()
		}
	}
}

// Flush writes and forgets the counts collected since the last flush. A
// failed write loses that window's counts: the status is best effort and
// must never hold up ingest.
func (t *Tracker) Flush(ctx context.Context) error {
	t.mu.Lock()
	entries := t.entries
	t.entries = map[entryKey]*trackedEntry{}
	t.full = false
	t.mu.Unlock()
	if len(entries) == 0 {
		return nil
	}
	byProject := map[uuid.UUID]map[entryKey]*trackedEntry{}
	for key, entry := range entries {
		if byProject[key.projectID] == nil {
			byProject[key.projectID] = map[entryKey]*trackedEntry{}
		}
		byProject[key.projectID][key] = entry
	}
	var firstErr error
	for projectID, projectEntries := range byProject {
		if err := t.repo.write(ctx, projectID, projectEntries); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// record adds a request's sources for signal.
func (t *Tracker) record(projectID uuid.UUID, signal string, sources batch) {
	if len(sources) == 0 {
		return
	}
	now := t.now()
	minute := now.Truncate(time.Minute).Unix()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.entries == nil {
		t.entries = map[entryKey]*trackedEntry{}
	}
	for _, source := range sources {
		key := entryKey{projectID, sourceKey(source.identity.key), signal}
		entry, ok := t.entries[key]
		if !ok {
			if len(t.entries) >= maxTrackedEntries {
				if !t.full {
					t.full = true
					t.logger.Warn("Ingestion status is tracking too many sources; new ones wait for the next flush",
						"limit", maxTrackedEntries)
				}
				continue
			}
			entry = &trackedEntry{
				identifiedBy: source.identity.by,
				attributes:   source.attributes,
				first:        now,
				minutes:      map[int64]int64{},
			}
			t.entries[key] = entry
		}
		entry.last = now
		entry.minutes[minute] += source.count
		if source.kind.rank > entry.kind.rank {
			entry.kind = source.kind
		}
	}
}

func (t *Tracker) flushLogged() {
	ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
	defer cancel()
	if err := t.Flush(ctx); err != nil {
		t.logger.Warn("Failed to write ingestion status", "error", err)
	}
}

// add counts count records from the source get describes. kind is asked
// until a specific match is found: a request's records mostly share a source.
func (b batch) add(get attributeGetter, count int64, kind func() kindHint) {
	identity := identify(get)
	source, ok := b[identity.key]
	if !ok {
		source = &batchSource{identity: identity, attributes: identityAttributes(get, identity.by)}
		b[identity.key] = source
	}
	source.count += count
	if source.kind.rank < rankSpecific {
		if hint := kind(); hint.rank > source.kind.rank {
			source.kind = hint
		}
	}
}
