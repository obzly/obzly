package segments

import (
	"database/sql"
	"errors"
	"os"
	"sync"

	"obzly/internal/telemetry"
)

// sharedHandle is one DuckDB database every user of a kind in this process
// shares: one memory cap for the kind instead of one per user. A worker held
// a writer per stream and path (10), a compactor per stream (4) and a token
// filter builder per logs stream (2), each with its own cap; their sum could
// never fit the pod. Queries on the shared database still run side by side
// (each on its caller's thread, past the cap they spill).
type sharedHandle struct {
	kind    string
	limit   string
	threads int
	// setup runs once on a newly opened database.
	setup func(db *sql.DB) error

	mu       sync.Mutex
	db       *sql.DB
	spillDir string
	refs     int
}

// acquire returns the shared database, opening it on the first reference
// with its spill directory under spillRoot. Pair each acquire with release.
func (h *sharedHandle) acquire(spillRoot string) (*sql.DB, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.db != nil {
		h.refs++
		return h.db, nil
	}
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, err
	}
	spillDir, err := ConfigureMemory(db, h.limit, spillRoot)
	if err == nil {
		err = SetHandleLimits(db, "", h.threads)
	}
	if err == nil && h.setup != nil {
		err = h.setup(db)
	}
	if err != nil {
		_ = db.Close()
		if spillDir != "" {
			_ = os.RemoveAll(spillDir)
		}
		return nil, err
	}
	telemetry.RegisterDuckDB(h.kind, db, MemoryLimitBytes(h.limit))
	h.db, h.spillDir, h.refs = db, spillDir, 1
	return db, nil
}

// release drops one reference and closes the database with the last.
func (h *sharedHandle) release() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.refs == 0 {
		return errors.New("duckdb shared handle released more often than acquired")
	}
	h.refs--
	if h.refs > 0 {
		return nil
	}
	telemetry.UnregisterDuckDB(h.db)
	err := h.db.Close()
	// After Close: DuckDB deletes its spill files only on a clean Close.
	_ = os.RemoveAll(h.spillDir)
	h.db, h.spillDir = nil, ""
	return err
}

// spillDirectory is the open database's spill directory ("" when closed).
func (h *sharedHandle) spillDirectory() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.spillDir
}
