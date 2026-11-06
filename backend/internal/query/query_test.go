package query_test

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"obzly/internal/broker"
	"obzly/internal/query"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/micro"
	"github.com/stretchr/testify/require"
)

const (
	countSQL      = "SELECT count(*) AS n FROM " + query.SourcePlaceholder + " WHERE level = ? AND timestamp >= ?"
	countFinalSQL = "SELECT coalesce(sum(n), 0)::BIGINT FROM " + query.ResultsPlaceholder
	pageSQL       = "SELECT id, timestamp FROM " + query.SourcePlaceholder + " ORDER BY timestamp DESC LIMIT ?"
	pageFinalSQL  = "SELECT id FROM " + query.ResultsPlaceholder + " ORDER BY timestamp DESC LIMIT ? OFFSET ?"
)

func Test_Assign_RemovingAnExecutor_MovesOnlyItsSegments(t *testing.T) {
	records := make([]*segments.SegmentRecord, 300)
	for i := range records {
		records[i] = &segments.SegmentRecord{ID: uuid.New()}
	}
	three := []query.Executor{stubExecutor("a"), stubExecutor("b"), stubExecutor("c")}

	before := owners(query.Assign(records, three))
	after := owners(query.Assign(records, three[:2]))

	moved := 0
	for id, owner := range before {
		if owner != "c" {
			require.Equal(t, owner, after[id], "a segment not on the removed executor stays put")
		} else {
			moved++
		}
	}
	require.InDelta(t, 100, moved, 40, "about a third of the segments were on c")
}

// Executor ids that differ only in their last character still share
// segments evenly.
func Test_Assign_SimilarExecutorIDs_ShareSegmentsEvenly(t *testing.T) {
	records := make([]*segments.SegmentRecord, 600)
	for i := range records {
		records[i] = &segments.SegmentRecord{ID: uuid.New()}
	}

	assigned := query.Assign(records, []query.Executor{stubExecutor("e0"), stubExecutor("e1"), stubExecutor("e2")})

	require.Len(t, assigned, 3)
	for _, group := range assigned {
		require.InDelta(t, 200, len(group.Segments), 60, "executor %s", group.Executor.ID())
	}
}

// A querier restarted on its persistent cache volume comes back with a new
// instance ID but the same affinity, and gets the same segments - the ones
// its cache holds.
func Test_Assign_RestartedExecutorWithSameAffinity_KeepsItsSegments(t *testing.T) {
	records := make([]*segments.SegmentRecord, 300)
	for i := range records {
		records[i] = &segments.SegmentRecord{ID: uuid.New()}
	}
	before := []query.Executor{affinityExecutor{"id-1", "disk-a"}, affinityExecutor{"id-2", "disk-b"}}
	after := []query.Executor{affinityExecutor{"id-3", "disk-a"}, affinityExecutor{"id-2", "disk-b"}}

	byAffinity := func(assigned []query.Assignment) map[uuid.UUID]string {
		result := map[uuid.UUID]string{}
		for _, group := range assigned {
			for _, record := range group.Segments {
				result[record.ID] = group.Executor.(affinityExecutor).affinity
			}
		}
		return result
	}

	require.Equal(t, byAffinity(query.Assign(records, before)), byAffinity(query.Assign(records, after)))
}

func Test_Owned_IsWhatAssignSendsThatExecutor(t *testing.T) {
	records := make([]*segments.SegmentRecord, 200)
	for i := range records {
		records[i] = &segments.SegmentRecord{ID: uuid.New()}
	}
	executors := []query.Executor{affinityExecutor{"id-1", "disk-a"}, affinityExecutor{"id-2", "disk-b"}}

	owned := query.Owned(records, executors, "disk-b")

	var assigned []*segments.SegmentRecord
	for _, group := range query.Assign(records, executors) {
		if group.Executor.(affinityExecutor).affinity == "disk-b" {
			assigned = group.Segments
		}
	}
	require.NotEmpty(t, owned)
	require.Equal(t, assigned, owned)
}

// An uploaded segment's notice reaches the executor it belongs to, which
// fetches it; an executor that doesn't own it leaves it alone.
func Test_ServePrefetch_FetchesUploadedSegmentsThisExecutorOwns(t *testing.T) {
	client := startBroker(t)
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() {
		served <- query.Serve(ctx, query.ServeConfig{
			Conn: client.Conn, ResultsDir: t.TempDir(), ListenAddr: "127.0.0.1:0", Token: "secret",
			Concurrency: 1, Log: discard(), Executor: query.NewLocalExecutor("served", duckDB(t), nil, ""),
			Affinity: "disk-a",
		})
	}()
	registry := query.NewRegistry(client.Conn, "secret", nil, discard())
	require.Eventually(t, func() bool { return len(registry.Executors()) == 1 }, 10*time.Second, 100*time.Millisecond)
	owner, stranger := make(chan uuid.UUID, 1), make(chan uuid.UUID, 1)
	for affinity, fetched := range map[string]chan uuid.UUID{"disk-a": owner, "disk-z": stranger} {
		go func() {
			_ = query.ServePrefetch(ctx, client.Conn, registry, affinity, func(record *segments.SegmentRecord) {
				fetched <- record.ID
			}, discard())
		}()
	}
	t.Cleanup(func() {
		cancel()
		<-served
	})
	time.Sleep(200 * time.Millisecond) // both subscriptions in place
	record := &segments.SegmentRecord{
		ID:        uuid.New(),
		ProjectID: uuid.New(),
		Stream:    "logs",
		ObjectKey: "logs/x.parquet",
	}

	query.PublishUploaded(client.Conn, record)

	select {
	case id := <-owner:
		require.Equal(t, record.ID, id)
	case <-time.After(5 * time.Second):
		t.Fatal("the owning executor never fetched the segment")
	}
	select {
	case <-stranger:
		t.Fatal("an executor that doesn't own the segment fetched it")
	case <-time.After(300 * time.Millisecond):
	}
}

// A coordinator that gives up (the user left, a newer search) cancels the
// fragment on the executor instead of leaving it a slot until the deadline.
func Test_RemoteExecutor_CoordinatorGivesUp_CancelsTheFragmentOnTheExecutor(t *testing.T) {
	client := startBroker(t)
	ctx, cancel := context.WithCancel(context.Background())
	started, stopped := make(chan struct{}, 1), make(chan error, 1)
	served := make(chan error, 1)
	go func() {
		served <- query.Serve(ctx, query.ServeConfig{
			Conn: client.Conn, ResultsDir: t.TempDir(), ListenAddr: "127.0.0.1:0", Token: "secret",
			Concurrency: 1, Log: discard(), Executor: blockingExecutor{started: started, stopped: stopped},
		})
	}()
	t.Cleanup(func() {
		cancel()
		<-served
	})
	registry := query.NewRegistry(client.Conn, "secret", nil, discard())
	require.Eventually(t, func() bool { return len(registry.Executors()) == 1 }, 10*time.Second, 100*time.Millisecond)
	search, giveUp := context.WithCancel(context.Background())
	go func() {
		<-started
		giveUp()
	}()

	err := registry.Executors()[0].Execute(search, query.Fragment{ID: uuid.NewString(), Stream: "logs"},
		filepath.Join(t.TempDir(), "r.parquet"))

	require.Error(t, err)
	select {
	case stopErr := <-stopped:
		require.ErrorIs(t, stopErr, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("the executor kept running the fragment after the coordinator gave up")
	}
}

// An executor killed mid-fragment (a Spot interruption) used to leave the
// coordinator waiting the whole fragmentTimeout; now the fragment fails as
// soon as discovery drops the executor, and is retried elsewhere.
func Test_RemoteExecutor_ExecutorLeavesMidFragment_FailsWithErrExecutorLeft(t *testing.T) {
	srv, err := broker.StartServer(broker.ServerConfig{
		Name: "query-" + uuid.NewString()[:8], StoreDir: t.TempDir(), InProcess: true,
	}, discard())
	require.NoError(t, err)
	t.Cleanup(srv.Shutdown)
	coordinator, err := broker.ConnectInProcess(srv, "coordinator")
	require.NoError(t, err)
	t.Cleanup(coordinator.Close)
	executor, err := broker.ConnectInProcess(srv, "executor")
	require.NoError(t, err)
	started, stopped := make(chan struct{}, 1), make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = query.Serve(ctx, query.ServeConfig{
			Conn: executor.Conn, ResultsDir: t.TempDir(), ListenAddr: "127.0.0.1:0", Token: "secret",
			Concurrency: 1, Log: discard(), Executor: blockingExecutor{started: started, stopped: stopped},
		})
	}()
	registry := query.NewRegistry(coordinator.Conn, "secret", nil, discard())
	require.Eventually(t, func() bool { return len(registry.Executors()) == 1 }, 10*time.Second, 100*time.Millisecond)
	go func() {
		<-started
		executor.Close()
	}()
	began := time.Now()

	err = registry.Executors()[0].Execute(context.Background(), query.Fragment{ID: uuid.NewString(), Stream: "logs"},
		filepath.Join(t.TempDir(), "r.parquet"))

	require.ErrorIs(t, err, query.ErrExecutorLeft)
	require.Less(t, time.Since(began), 10*time.Second)
}

func Test_Registry_ExecutorAffinity_ComesFromServeConfig(t *testing.T) {
	client := startBroker(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- query.Serve(ctx, query.ServeConfig{
			Conn: client.Conn, ResultsDir: t.TempDir(), ListenAddr: "127.0.0.1:0", Token: "secret",
			Concurrency: 1, Log: discard(), Executor: query.NewLocalExecutor("served", duckDB(t), nil, ""),
			Affinity: "disk-a",
		})
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	registry := query.NewRegistry(client.Conn, "secret", nil, discard())

	require.Eventually(t, func() bool { return len(registry.Executors()) == 1 }, 10*time.Second, 100*time.Millisecond)
	executor, ok := registry.Executors()[0].(interface{ Affinity() string })
	require.True(t, ok)
	require.Equal(t, "disk-a", executor.Affinity())
}

func Test_Assign_KeepsSegmentOrderWithinAFragment(t *testing.T) {
	records := []*segments.SegmentRecord{{ID: uuid.New()}, {ID: uuid.New()}, {ID: uuid.New()}}

	assigned := query.Assign(records, []query.Executor{stubExecutor("only")})

	require.Len(t, assigned, 1)
	require.Equal(t, records, assigned[0].Segments)
}

// In-process, a query (tail files included) gives what one query over
// every file gives.
func Test_Coordinator_InProcess_MatchesASingleQuery(t *testing.T) {
	dir := t.TempDir()
	records := writeSegments(t, dir, 6, 500)
	tail := []segments.ResolvedFile{{Path: records[5].Path}}
	db := duckDB(t)
	coordinator, err := query.NewCoordinator(filepath.Join(dir, "results"), db,
		query.NewLocalExecutor("local", db, nil, ""), nil, discard())
	require.NoError(t, err)

	total, page := countAndPage(t, coordinator, records[:5], tail)

	wantTotal, wantPage := singleQuery(t, records)
	require.Equal(t, wantTotal, total)
	require.Equal(t, wantPage, page)
}

// Executors found over NATS run the fragments and return results over
// gRPC; the merge gives what one query gives.
func Test_Coordinator_RemoteExecutors_MatchASingleQuery(t *testing.T) {
	dir := t.TempDir()
	records := writeSegments(t, dir, 9, 400)
	client := startBroker(t)
	var served atomic.Int64
	for i := range 3 {
		serveExecutor(t, client, filepath.Join(dir, fmt.Sprintf("executor-%d", i)), "secret", &served)
	}
	registry := query.NewRegistry(client.Conn, "secret", nil, discard())
	require.Eventually(t, func() bool { return len(registry.Executors()) == 3 }, 10*time.Second, 100*time.Millisecond)
	db := duckDB(t)
	coordinator, err := query.NewCoordinator(filepath.Join(dir, "results"), db,
		failingExecutor{id: "local"}, registry.Executors, discard())
	require.NoError(t, err)

	total, page := countAndPage(t, coordinator, records, nil)

	wantTotal, wantPage := singleQuery(t, records)
	require.Equal(t, wantTotal, total)
	require.Equal(t, wantPage, page)
	require.GreaterOrEqual(t, served.Load(), int64(4), "both queries spread over the executors")
}

// A failing executor's segments run on the next-ranked one: the result is
// the same.
func Test_Coordinator_ExecutorFails_RetriesItsSegmentsElsewhere(t *testing.T) {
	dir := t.TempDir()
	records := writeSegments(t, dir, 9, 400)
	db := duckDB(t)
	// Segment ids are random: pick a broken executor id that owns some.
	var remote []query.Executor
	for attempt := 0; ; attempt++ {
		remote = []query.Executor{
			failingExecutor{id: fmt.Sprintf("broken-%d", attempt)},
			query.NewLocalExecutor("e1", db, nil, ""),
			query.NewLocalExecutor("e2", db, nil, ""),
		}
		if slices.Contains(ownerSet(query.Assign(records, remote)), remote[0].ID()) {
			break
		}
	}
	coordinator, err := query.NewCoordinator(filepath.Join(dir, "results"), db,
		failingExecutor{id: "local"}, func() []query.Executor { return remote }, discard())
	require.NoError(t, err)

	total, page := countAndPage(t, coordinator, records, nil)

	wantTotal, wantPage := singleQuery(t, records)
	require.Equal(t, wantTotal, total)
	require.Equal(t, wantPage, page)
}

// Slices run whole, each with its own SQL, spread over executors; one
// whose executor fails runs whole elsewhere, never split.
func Test_Coordinator_Slices_RunWholeAndRetryWhole(t *testing.T) {
	dir := t.TempDir()
	records := writeSegments(t, dir, 6, 100)
	db := duckDB(t)
	healthy := []*sliceRecorder{
		{LocalExecutor: query.NewLocalExecutor("e1", db, nil, "")},
		{LocalExecutor: query.NewLocalExecutor("e2", db, nil, "")},
	}
	remote := []query.Executor{failingExecutor{id: "broken"}, healthy[0], healthy[1]}
	coordinator, err := query.NewCoordinator(filepath.Join(dir, "results"), db,
		failingExecutor{id: "local"}, func() []query.Executor { return remote }, discard())
	require.NoError(t, err)

	var slices []query.Slice
	for i := range 3 {
		slices = append(slices, query.Slice{
			Segments: records[i*2 : i*2+2],
			SQL:      "SELECT ? AS slice, count(*) AS n FROM " + query.SourcePlaceholder,
			Args:     []any{i},
		})
	}
	counts := map[int]int64{}
	err = coordinator.Query(context.Background(), query.Request{
		Stream: "logs", Slices: slices, FinalSQL: "SELECT slice, n FROM " + query.ResultsPlaceholder,
	}, func(rows *sql.Rows) error {
		for rows.Next() {
			var slice int
			var n int64
			if err := rows.Scan(&slice, &n); err != nil {
				return err
			}
			counts[slice] += n
		}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, map[int]int64{0: 200, 1: 200, 2: 200}, counts, "each slice once, whole")
	ran := 0
	for _, executor := range healthy {
		for _, segmentCount := range executor.segments {
			require.Equal(t, 2, segmentCount, "a slice is never split")
		}
		ran += len(executor.segments)
	}
	require.Equal(t, 3, ran)
}

// With every remote executor failing, the segments run in-process.
func Test_Coordinator_EveryExecutorFails_RunsInProcess(t *testing.T) {
	dir := t.TempDir()
	records := writeSegments(t, dir, 4, 100)
	db := duckDB(t)
	remote := []query.Executor{failingExecutor{id: "a"}, failingExecutor{id: "b"}, failingExecutor{id: "c"}}
	coordinator, err := query.NewCoordinator(filepath.Join(dir, "results"), db,
		query.NewLocalExecutor("local", db, nil, ""), func() []query.Executor { return remote }, discard())
	require.NoError(t, err)

	total, page := countAndPage(t, coordinator, records, nil)

	wantTotal, wantPage := singleQuery(t, records)
	require.Equal(t, wantTotal, total)
	require.Equal(t, wantPage, page)
}

// One in-process fragment runs SingleSQL as is: nesting the page query in
// the merge SQL held and re-sorted offset+limit full rows, and the worker's
// quota pass (offset up to 1M) ran out of memory.
func Test_Coordinator_OneInProcessFragment_RunsSingleSQL(t *testing.T) {
	dir := t.TempDir()
	records := writeSegments(t, dir, 3, 100)
	db := duckDB(t)
	recorder := &recordingExecutor{LocalExecutor: query.NewLocalExecutor("local", db, nil, "")}
	coordinator, err := query.NewCoordinator(filepath.Join(dir, "results"), db, recorder, nil, discard())
	require.NoError(t, err)
	single := "SELECT id FROM " + query.SourcePlaceholder + " ORDER BY timestamp DESC LIMIT ? OFFSET ?"

	var page []string
	err = coordinator.Query(context.Background(), query.Request{
		Stream: "logs", Segments: records,
		FragmentSQL: pageSQL, FragmentArgs: []any{10},
		FinalSQL: pageFinalSQL, FinalArgs: []any{5, 5},
		SingleSQL: single, SingleArgs: []any{5, 5},
	}, func(rows *sql.Rows) error {
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			page = append(page, id)
		}
		return nil
	})
	require.NoError(t, err)

	require.Equal(t, []string{single}, recorder.sqls)
	_, wantPage := singleQuery(t, records)
	require.Equal(t, wantPage, page)
}

func Test_Coordinator_NoReadableFiles_ReturnsErrNoData(t *testing.T) {
	dir := t.TempDir()
	db := duckDB(t)
	coordinator, err := query.NewCoordinator(filepath.Join(dir, "results"), db,
		query.NewLocalExecutor("local", db, nil, ""), nil, discard())
	require.NoError(t, err)

	err = coordinator.Query(context.Background(), query.Request{
		Stream: "logs",
		Segments: []*segments.SegmentRecord{
			{ID: uuid.New(), Stream: "logs", Path: filepath.Join(dir, "gone.parquet")},
		},
		FragmentSQL:  countSQL,
		FragmentArgs: []any{"error", time.Time{}},
		FinalSQL:     countFinalSQL,
	}, func(*sql.Rows) error { return nil })

	require.ErrorIs(t, err, query.ErrNoData)
}

func Test_LocalExecutor_PastItsDeadline_FailsRatherThanSkippingSegments(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := query.NewLocalExecutor("local", duckDB(t), nil, "").Execute(ctx, query.Fragment{
		ID: "late", Stream: "logs",
		Segments: []*segments.SegmentRecord{
			{ID: uuid.New(), Stream: "logs", Path: filepath.Join(dir, "cold.parquet")},
		},
		SQL: "SELECT count(*) FROM " + query.SourcePlaceholder,
	}, filepath.Join(dir, "result.parquet"))

	require.ErrorIs(t, err, context.Canceled)
}

// The results listener refuses a caller without the executor token.
func Test_RemoteExecutor_WrongToken_IsRefused(t *testing.T) {
	dir := t.TempDir()
	records := writeSegments(t, dir, 2, 10)
	client := startBroker(t)
	serveExecutor(t, client, filepath.Join(dir, "executor"), "secret", nil)
	registry := query.NewRegistry(client.Conn, "wrong", nil, discard())
	require.Eventually(t, func() bool { return len(registry.Executors()) == 1 }, 10*time.Second, 100*time.Millisecond)

	err := registry.Executors()[0].Execute(context.Background(), query.Fragment{
		ID: uuid.NewString(), Stream: "logs", Segments: records,
		SQL: countSQL, Args: []any{"error", time.Time{}},
	}, filepath.Join(dir, "result.parquet"))

	require.ErrorContains(t, err, "invalid query executor token")
}

// Any client with the NATS token can reach an executor's subject: SQL it
// didn't get from a coordinator holding QUERIER_TOKEN is refused unrun.
func Test_Serve_UnsignedFragment_IsRefused(t *testing.T) {
	dir := t.TempDir()
	client := startBroker(t)
	var served atomic.Int64
	serveExecutor(t, client, filepath.Join(dir, "executor"), "secret", &served)
	registry := query.NewRegistry(client.Conn, "secret", nil, discard())
	require.Eventually(t, func() bool { return len(registry.Executors()) == 1 }, 10*time.Second, 100*time.Millisecond)
	subject := "obzly.query.exec." + registry.Executors()[0].ID()
	payload := []byte(`{"id":"` + uuid.NewString() + `","stream":"logs","sql":"SELECT 42"}`)

	for _, signature := range []string{"", "0123abcd"} {
		request := nats.NewMsg(subject)
		request.Data = payload
		if signature != "" {
			request.Header.Set("Obzly-Fragment-Signature", signature)
		}
		reply, err := client.Conn.RequestMsg(request, 5*time.Second)
		require.NoError(t, err)
		require.Equal(t, "401", reply.Header.Get(micro.ErrorCodeHeader), "signature %q", signature)
	}
	require.Zero(t, served.Load())
}

// A parameter before the source (a bucket width in the SELECT list) binds
// before the source's file arguments.
func Test_Render_ParameterBeforeTheSource_BindsFirst(t *testing.T) {
	files := []segments.ResolvedFile{{Path: "/a.parquet"}}

	sqlText, args, err := query.Render(query.Fragment{
		Stream: "logs",
		SQL:    "SELECT ? AS width FROM " + query.SourcePlaceholder + " WHERE level = ?",
		Args:   []any{60.0, "error"},
	}, files)

	require.NoError(t, err)
	_, sourceArgs := segments.SourceExpr("logs", files)
	require.Equal(t, append(append([]any{60.0}, sourceArgs...), "error"), args)
	require.NotContains(t, sqlText, query.SourcePlaceholder)
}

// Over its scan budget a query is refused before any executor runs, and the
// refusal says how much it would have read.
func Test_Coordinator_OverScanBudget_RefusesBeforeReading(t *testing.T) {
	dir := t.TempDir()
	db := duckDB(t)
	recorder := &recordingExecutor{LocalExecutor: query.NewLocalExecutor("local", db, nil, "")}
	coordinator, err := query.NewCoordinator(filepath.Join(dir, "results"), db, recorder, nil, discard())
	require.NoError(t, err)
	records := []*segments.SegmentRecord{
		{ID: uuid.New(), Stream: "logs", Path: "/nowhere/a.parquet", SizeBytes: 3 << 30},
		{ID: uuid.New(), Stream: "logs", Path: "/nowhere/b.parquet", SizeBytes: 2 << 30},
	}

	err = coordinator.Query(context.Background(), query.Request{
		Stream: "logs", Segments: records, ScanBudget: 4 << 30,
		FragmentSQL: countSQL, FragmentArgs: []any{"error", time.Time{}}, FinalSQL: countFinalSQL,
	}, func(*sql.Rows) error { return nil })

	require.ErrorIs(t, err, query.ErrScanBudget)
	message, refused := query.BudgetRefusal(fmt.Errorf("wrapped: %w", err))
	require.True(t, refused)
	require.Contains(t, message, "5.0 GiB")
	require.Contains(t, message, "4.0 GiB")
	require.Empty(t, recorder.sqls, "nothing ran")
}

// A coordinator removes old query results a killed process left behind,
// not recent ones (another replica's running query).
func Test_NewCoordinator_RemovesStaleQueryDirs(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "query-1")
	fresh := filepath.Join(dir, "query-2")
	require.NoError(t, os.MkdirAll(stale, 0o755))
	require.NoError(t, os.MkdirAll(fresh, 0o755))
	old := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(stale, old, old))

	_, err := query.NewCoordinator(dir, duckDB(t), stubExecutor("local"), nil, discard())

	require.NoError(t, err)
	require.NoDirExists(t, stale)
	require.DirExists(t, fresh)
}

// A ? inside a string literal (a regex) is not a parameter.
func Test_Render_QuestionMarkInALiteral_IsNotAParameter(t *testing.T) {
	sqlText, args, err := query.Render(query.Fragment{Stream: "logs",
		SQL:  "SELECT regexp_replace(message, '(?i)\\d+?', '<n>') FROM " + query.SourcePlaceholder + " WHERE level = ?",
		Args: []any{"error"}}, []segments.ResolvedFile{{Path: "/a.parquet"}})

	require.NoError(t, err)
	require.Equal(t, "error", args[len(args)-1])
	require.NotContains(t, sqlText, query.SourcePlaceholder)
}

type stubExecutor string

// blockingExecutor runs until its fragment is cancelled.
type blockingExecutor struct {
	started chan struct{}
	stopped chan error
}

func (blockingExecutor) ID() string { return "blocking" }

func (b blockingExecutor) Execute(ctx context.Context, _ query.Fragment, _ string) error {
	b.started <- struct{}{}
	<-ctx.Done()
	b.stopped <- ctx.Err()
	return ctx.Err()
}

type affinityExecutor struct{ id, affinity string }

func (a affinityExecutor) ID() string { return a.id }

func (a affinityExecutor) Affinity() string { return a.affinity }

func (a affinityExecutor) Execute(context.Context, query.Fragment, string) error { return nil }

func (s stubExecutor) ID() string { return string(s) }

func (s stubExecutor) Execute(context.Context, query.Fragment, string) error { return nil }

type failingExecutor struct{ id string }

func (f failingExecutor) ID() string { return f.id }

func (f failingExecutor) Execute(context.Context, query.Fragment, string) error {
	return errors.New("executor " + f.id + " is down")
}

// recordingExecutor records the fragment SQL it runs.
type recordingExecutor struct {
	*query.LocalExecutor
	sqls []string
}

func (r *recordingExecutor) Execute(ctx context.Context, fragment query.Fragment, path string) error {
	r.sqls = append(r.sqls, fragment.SQL)
	return r.LocalExecutor.Execute(ctx, fragment, path)
}

// sliceRecorder records how many segments each fragment it runs has.
type sliceRecorder struct {
	*query.LocalExecutor
	mu       sync.Mutex
	segments []int
}

func (r *sliceRecorder) Execute(ctx context.Context, fragment query.Fragment, path string) error {
	r.mu.Lock()
	r.segments = append(r.segments, len(fragment.Segments))
	r.mu.Unlock()
	return r.LocalExecutor.Execute(ctx, fragment, path)
}

// countingExecutor counts the fragments a served executor runs.
type countingExecutor struct {
	*query.LocalExecutor
	served *atomic.Int64
}

func (c countingExecutor) Execute(ctx context.Context, fragment query.Fragment, path string) error {
	c.served.Add(1)
	return c.LocalExecutor.Execute(ctx, fragment, path)
}

func countAndPage(
	t *testing.T,
	coordinator *query.Coordinator,
	records []*segments.SegmentRecord,
	tail []segments.ResolvedFile,
) (int64, []string) {
	t.Helper()
	since := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var total int64
	err := coordinator.Query(context.Background(), query.Request{
		Stream: "logs", Segments: records, LocalFiles: tail,
		FragmentSQL: countSQL, FragmentArgs: []any{"error", since}, FinalSQL: countFinalSQL,
	}, func(rows *sql.Rows) error {
		if !rows.Next() {
			return errors.New("no count row")
		}
		return rows.Scan(&total)
	})
	require.NoError(t, err)

	var page []string
	err = coordinator.Query(context.Background(), query.Request{
		Stream: "logs", Segments: records, LocalFiles: tail,
		FragmentSQL: pageSQL, FragmentArgs: []any{10},
		FinalSQL: pageFinalSQL, FinalArgs: []any{5, 5},
	}, func(rows *sql.Rows) error {
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			page = append(page, id)
		}
		return nil
	})
	require.NoError(t, err)
	return total, page
}

func singleQuery(t *testing.T, records []*segments.SegmentRecord) (int64, []string) {
	t.Helper()
	files := make([]segments.ResolvedFile, len(records))
	for i, record := range records {
		files[i] = segments.ResolvedFile{Path: record.Path}
	}
	db := duckDB(t)
	source, args := segments.SourceExpr("logs", files)
	var total int64
	require.NoError(t, db.QueryRow("SELECT count(*) FROM "+source+" WHERE level = 'error'", args...).Scan(&total))
	rows, err := db.Query("SELECT id FROM "+source+" ORDER BY timestamp DESC LIMIT 5 OFFSET 5", args...)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var page []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		page = append(page, id)
	}
	return total, page
}

func startBroker(t *testing.T) *broker.Client {
	t.Helper()
	client, err := broker.StartInProcess("query-"+uuid.NewString()[:8], t.TempDir(), discard())
	require.NoError(t, err)
	t.Cleanup(client.Close)
	return client
}

func serveExecutor(t *testing.T, client *broker.Client, dir, token string, served *atomic.Int64) {
	t.Helper()
	serveExecutorTLS(t, client, dir, token, served, nil)
}

// serveExecutorTLS serves an executor whose results listener uses
// serverTLS (nil: plaintext).
func serveExecutorTLS(
	t *testing.T, client *broker.Client, dir, token string, served *atomic.Int64, serverTLS *tls.Config,
) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	local := query.NewLocalExecutor("served", duckDB(t), nil, "")
	config := query.ServeConfig{
		Conn: client.Conn, ResultsDir: dir, ListenAddr: "127.0.0.1:0", AdvertiseAddr: "",
		Token: token, Concurrency: 2, Log: discard(), Executor: local, TLS: serverTLS,
	}
	if served != nil {
		config.Executor = countingExecutor{LocalExecutor: local, served: served}
	}
	done := make(chan error, 1)
	go func() { done <- query.Serve(ctx, config) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func duckDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	require.NoError(t, segments.RegisterNormalizationFunctions(db))
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func owners(assigned []query.Assignment) map[uuid.UUID]string {
	result := map[uuid.UUID]string{}
	for _, group := range assigned {
		for _, record := range group.Segments {
			result[record.ID] = group.Executor.ID()
		}
	}
	return result
}

func ownerSet(assigned []query.Assignment) []string {
	var ids []string
	for _, group := range assigned {
		ids = append(ids, group.Executor.ID())
	}
	return ids
}

// writeSegments writes n uploaded-looking segments of rows each: ids
// unique, timestamps distinct, every third row an error.
func writeSegments(t *testing.T, dir string, n, rows int) []*segments.SegmentRecord {
	t.Helper()
	db := duckDB(t)
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var records []*segments.SegmentRecord
	for i := range n {
		path := filepath.Join(dir, fmt.Sprintf("seg-%d.parquet", i))
		_, err := db.Exec(fmt.Sprintf(`COPY (
			SELECT 'f%d-' || r AS id,
			       TIMESTAMP '%s' + to_milliseconds((r * %d + %d)::BIGINT) AS timestamp,
			       CASE WHEN r %% 3 = 0 THEN 'error' ELSE 'info' END AS level
			FROM range(%d) t(r)) TO '%s' (FORMAT PARQUET)`,
			i, base.Format("2006-01-02 15:04:05"), n, i, rows, path))
		require.NoError(t, err)
		records = append(records, &segments.SegmentRecord{
			ID: uuid.New(), Stream: "logs", Path: path, ObjectKey: "logs/" + filepath.Base(path),
			SchemaVersion: segments.CurrentSchemaVersion,
		})
	}
	return records
}
