package query_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"obzly/internal/query"
	"obzly/internal/storage/segments"

	"github.com/stretchr/testify/require"
)

// singleSQL is a whole query of the kind SingleFragment requests run (the
// spans service map, trace summary): errors and distinct ids over every
// file at once.
const singleSQL = "SELECT count(*) FILTER (WHERE level = ?) AS errors, count(DISTINCT id) AS ids FROM " +
	query.SourcePlaceholder

// oldExecutor stands in for an executor that predates shipped files.
type oldExecutor struct {
	*query.LocalExecutor
	calls *atomic.Int64
}

// countingShipper counts the fragments it runs and the local files they
// had, then runs them.
type countingShipper struct {
	*query.LocalExecutor
	mu         sync.Mutex
	fragments  int
	localFiles int
}

// failingShipper takes shipped files and then fails every fragment.
type failingShipper struct {
	id    string
	calls *atomic.Int64
}

// A single-fragment request with a query-time tail and a segment not
// uploaded yet runs on one remote executor over NATS: the coordinator
// ships it those files (the local executor fails, so nothing ran here),
// the answer is what one query over every file gives, and the executor
// removes the shipped files afterwards.
func Test_Coordinator_SingleFragment_RunsRemotelyWithTheLocalFilesShipped(t *testing.T) {
	dir := t.TempDir()
	records, tail := singleFragmentInputs(t, dir)
	client := startBroker(t)
	var served atomic.Int64
	executorDirs := []string{filepath.Join(dir, "executor-0"), filepath.Join(dir, "executor-1")}
	for _, executorDir := range executorDirs {
		serveExecutor(t, client, executorDir, "secret", &served)
	}
	registry := query.NewRegistry(client.Conn, "secret", nil, discard())
	require.Eventually(t, func() bool { return len(registry.Executors()) == 2 }, 10*time.Second, 100*time.Millisecond)
	for _, executor := range registry.Executors() {
		shipper, ok := executor.(query.LocalFileShipper)
		require.True(t, ok)
		require.True(t, shipper.ShipsLocalFiles(), "a current executor advertises shipping")
	}
	db := duckDB(t)
	coordinator, err := query.NewCoordinator(filepath.Join(dir, "results"), db,
		failingExecutor{id: "local"}, registry.Executors, discard())
	require.NoError(t, err)

	errorsCount, ids := runSingle(t, coordinator, records, tail)

	wantErrors, wantIDs := singleAnswer(t, records, tail)
	require.Equal(t, wantErrors, errorsCount)
	require.Equal(t, wantIDs, ids)
	require.Equal(t, int64(1), served.Load(), "one fragment, on one executor")
	for _, executorDir := range executorDirs {
		entries, err := os.ReadDir(filepath.Join(executorDir, "shipped"))
		require.NoError(t, err)
		require.Empty(t, entries, "shipped files are removed after the fragment")
	}
}

// Placement: past the shipping bounds, without executors, with only
// executors that predate shipping, or when the remote run fails, the
// request runs in-process and answers the same; with nothing to ship an
// older executor still takes it.
func Test_Coordinator_SingleFragment_FallsBackInProcess(t *testing.T) {
	dir := t.TempDir()
	records, tail := singleFragmentInputs(t, dir)
	wantErrors, wantIDs := singleAnswer(t, records, tail)
	db := duckDB(t)
	manyTail := append([]segments.ResolvedFile{}, tail...)
	for range 64 {
		manyTail = append(manyTail, tail[0])
	}
	wantManyErrors, _ := singleAnswer(t, records, manyTail)

	for _, scenario := range []struct {
		name         string
		remote       func() []query.Executor
		tail         []segments.ResolvedFile
		uploadedOnly bool
		wantRemote   bool
	}{
		{name: "shipped", tail: tail, wantRemote: true},
		{name: "over the file bound", tail: manyTail},
		{name: "no executors", tail: tail, remote: func() []query.Executor { return nil }},
		{name: "older executors", tail: tail, remote: oldExecutors(db, nil)},
		{name: "remote fails", tail: tail, remote: func() []query.Executor {
			return []query.Executor{failingShipper{id: "broken", calls: &atomic.Int64{}}}
		}},
	} {
		local := &countingShipper{LocalExecutor: query.NewLocalExecutor("local", db, nil, "")}
		remote := &countingShipper{LocalExecutor: query.NewLocalExecutor("remote", db, nil, "")}
		executors := scenario.remote
		if executors == nil {
			executors = func() []query.Executor { return []query.Executor{remote} }
		}
		coordinator, err := query.NewCoordinator(filepath.Join(dir, "results-"+scenario.name), db,
			local, executors, discard())
		require.NoError(t, err)

		errorsCount, ids := runSingle(t, coordinator, records, scenario.tail)

		if len(scenario.tail) == len(manyTail) {
			require.Equal(t, wantManyErrors, errorsCount, scenario.name)
		} else {
			require.Equal(t, wantErrors, errorsCount, scenario.name)
		}
		require.Equal(t, wantIDs, ids, scenario.name)
		if scenario.wantRemote {
			require.Equal(t, 1, remote.fragments, scenario.name)
			require.Equal(t, len(tail)+1, remote.localFiles, "%s: the tail and the local segment", scenario.name)
			require.Zero(t, local.fragments, scenario.name)
		} else {
			require.Zero(t, remote.fragments, scenario.name)
			require.Equal(t, 1, local.fragments, "%s: in-process", scenario.name)
		}
	}

	var calls atomic.Int64
	coordinator, err := query.NewCoordinator(filepath.Join(dir, "results-old"), db,
		failingExecutor{id: "local"}, oldExecutors(db, &calls), discard())
	require.NoError(t, err)
	var uploaded []*segments.SegmentRecord
	for _, record := range records {
		if record.ObjectKey != "" {
			uploaded = append(uploaded, record)
		}
	}
	errorsCount, _ := runSingle(t, coordinator, uploaded, nil)
	wantUploaded, _ := singleAnswer(t, uploaded, nil)
	require.Equal(t, wantUploaded, errorsCount)
	require.Equal(t, int64(1), calls.Load(), "nothing to ship: an older executor runs it")
}

func (o oldExecutor) ShipsLocalFiles() bool { return false }

func (o oldExecutor) Execute(ctx context.Context, fragment query.Fragment, path string) error {
	if o.calls != nil {
		o.calls.Add(1)
	}
	if len(fragment.LocalFiles) > 0 {
		return errors.New("an older executor was sent local files")
	}
	return o.LocalExecutor.Execute(ctx, fragment, path)
}

func (c *countingShipper) Execute(ctx context.Context, fragment query.Fragment, path string) error {
	c.mu.Lock()
	c.fragments++
	c.localFiles += len(fragment.LocalFiles)
	c.mu.Unlock()
	return c.LocalExecutor.Execute(ctx, fragment, path)
}

func (f failingShipper) ID() string { return f.id }

func (f failingShipper) ShipsLocalFiles() bool { return true }

func (f failingShipper) Execute(context.Context, query.Fragment, string) error {
	f.calls.Add(1)
	return errors.New("executor " + f.id + " is down")
}

// singleFragmentInputs: 4 uploaded segments, one not uploaded (on this
// disk), and a tail file.
func singleFragmentInputs(t *testing.T, dir string) ([]*segments.SegmentRecord, []segments.ResolvedFile) {
	t.Helper()
	for _, sub := range []string{"segments", "tail"} {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, sub), 0o755))
	}
	records := writeSegments(t, filepath.Join(dir, "segments"), 5, 300)
	records[4].ObjectKey = ""
	tailRecords := writeSegments(t, filepath.Join(dir, "tail"), 1, 50)
	return records, []segments.ResolvedFile{{Path: tailRecords[0].Path}}
}

func oldExecutors(db *sql.DB, calls *atomic.Int64) func() []query.Executor {
	return func() []query.Executor {
		return []query.Executor{oldExecutor{LocalExecutor: query.NewLocalExecutor("old", db, nil, ""), calls: calls}}
	}
}

func runSingle(
	t *testing.T, coordinator *query.Coordinator, records []*segments.SegmentRecord, tail []segments.ResolvedFile,
) (int64, int64) {
	t.Helper()
	var errorsCount, ids int64
	err := coordinator.Query(context.Background(), query.Request{
		Stream: "logs", Segments: records, LocalFiles: tail,
		SingleSQL: singleSQL, SingleArgs: []any{"error"}, SingleFragment: true,
	}, func(rows *sql.Rows) error {
		if !rows.Next() {
			return errors.New("no row")
		}
		return rows.Scan(&errorsCount, &ids)
	})
	require.NoError(t, err)
	return errorsCount, ids
}

func singleAnswer(t *testing.T, records []*segments.SegmentRecord, tail []segments.ResolvedFile) (int64, int64) {
	t.Helper()
	files := append([]segments.ResolvedFile{}, tail...)
	for _, record := range records {
		files = append(files, segments.ResolvedFile{Path: record.Path})
	}
	source, args := segments.SourceExpr("logs", files)
	var errorsCount, ids int64
	require.NoError(t, duckDB(t).QueryRow(
		"SELECT count(*) FILTER (WHERE level = 'error'), count(DISTINCT id) FROM "+source, args...,
	).Scan(&errorsCount, &ids))
	return errorsCount, ids
}
