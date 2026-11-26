package spans_querier

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	spans_core "obzly/internal/features/spans/core"
	spans_ingester "obzly/internal/features/spans/ingester"
	"obzly/internal/query"
	"obzly/internal/storage/segments"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Every trace query answers the same in-process and spread over executors,
// with each trace's spans split across segments (so across fragments): a
// trace's summary, filters and latest k8s attributes are merged, not
// taken from one fragment.
func Test_SpanQueries_SpreadOverExecutors_MatchInProcess(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	projectID := seedSplitTraces(t, now, 0)
	inProcess, err := NewSpanQuerier(WithQueryDir(filepath.Join(t.TempDir(), "query")))
	require.NoError(t, err)
	t.Cleanup(func() { _ = inProcess.Close() })
	spread, err := NewSpanQuerier(WithQueryDir(filepath.Join(t.TempDir(), "query")))
	require.NoError(t, err)
	t.Cleanup(func() { _ = spread.Close() })
	records, err := spread.segmentRepo.ListByProject(projectID, "spans")
	require.NoError(t, err)
	// Segment ids are random: pick executor ids that split them.
	var executors []query.Executor
	for attempt := 0; len(query.Assign(records, executors)) < 2; attempt++ {
		executors = nil
		for i := range 3 {
			executors = append(executors, query.NewLocalExecutor(fmt.Sprintf("e%d-%d", i, attempt), spread.db, nil, ""))
		}
	}
	spread.coordinator, err = query.NewCoordinator(filepath.Join(t.TempDir(), "spread"), spread.db,
		query.NewLocalExecutor("local", spread.db, nil, ""), func() []query.Executor { return executors },
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	from, to := now.Add(-time.Hour), now.Add(time.Hour)
	window := &spans_core.TimeRangeDTO{From: &from, To: &to}
	minDuration := 2500.0

	for _, request := range []*spans_core.ListTracesRequestDTO{
		{TimeRange: window},
		{TimeRange: window, ServiceName: "svc-b"},
		{TimeRange: window, OperationName: "GET /a", ErrorsOnly: true},
		{TimeRange: window, MinDurationMs: &minDuration, Limit: 3},
	} {
		want, err := inProcess.ListTraces(context.Background(), projectID, request)
		require.NoError(t, err)
		got, err := spread.ListTraces(context.Background(), projectID, request)
		require.NoError(t, err)
		require.NotEmpty(t, want.Traces)
		require.Equal(t, sortedServices(want.Traces), sortedServices(got.Traces), "trace search %+v", request)
	}

	traceID := fmt.Sprintf("trace-%02d", 3)
	wantTrace, err := inProcess.GetTrace(projectID, traceID, window)
	require.NoError(t, err)
	gotTrace, err := spread.GetTrace(projectID, traceID, window)
	require.NoError(t, err)
	require.Len(t, gotTrace.Spans, 4)
	require.Equal(t, wantTrace, gotTrace)

	wantServices, err := inProcess.ListServices(context.Background(), projectID, window, 0)
	require.NoError(t, err)
	gotServices, err := spread.ListServices(context.Background(), projectID, window, 0)
	require.NoError(t, err)
	require.Equal(t, wantServices, gotServices)
	wantOperations, err := inProcess.ListOperations(context.Background(), projectID, "svc-a", window, 0)
	require.NoError(t, err)
	gotOperations, err := spread.ListOperations(context.Background(), projectID, "svc-a", window, 0)
	require.NoError(t, err)
	require.Equal(t, wantOperations, gotOperations)

	wantK8s, err := inProcess.ServiceK8sMetadata(context.Background(), projectID, window, 0)
	require.NoError(t, err)
	gotK8s, err := spread.ServiceK8sMetadata(context.Background(), projectID, window, 0)
	require.NoError(t, err)
	require.Equal(t, "ns-new", wantK8s["svc-a"].Namespace)
	require.Equal(t, wantK8s, gotK8s)

	for _, request := range []*spans_core.AggregationRequestDTO{
		{TimeRange: window},
		{TimeRange: window, GroupField: spans_core.GroupFieldServiceName, Limit: 2},
		{TimeRange: window, Metric: spans_core.AggregationMetricErrorCount, GroupField: spans_core.GroupFieldServiceName},
		{TimeRange: window, Metric: spans_core.AggregationMetricAvgDuration, BucketMs: 60_000},
		{TimeRange: window, Metric: spans_core.AggregationMetricP95Duration, GroupField: spans_core.GroupFieldServiceName},
		{TimeRange: window, RootSpansOnly: true, BucketMs: 120_000, GroupField: spans_core.GroupFieldStatusCode},
	} {
		want, err := inProcess.ExecuteAggregation(projectID, request)
		require.NoError(t, err)
		got, err := spread.ExecuteAggregation(projectID, request)
		require.NoError(t, err)
		require.NotEmpty(t, want.Buckets)
		require.Equal(t, want, got, "aggregation %+v", request)
	}

	wantEdges, err := inProcess.ServiceCallEdges(context.Background(), projectID, window, 0)
	require.NoError(t, err)
	gotEdges, err := spread.ServiceCallEdges(context.Background(), projectID, window, 0)
	require.NoError(t, err)
	require.NotEmpty(t, wantEdges)
	require.ElementsMatch(t, wantEdges, gotEdges)

	errorRates := &spans_core.ServiceErrorRatesQuery{From: now.Add(-5 * time.Minute), To: to, PreviousFrom: from, Limit: 10}
	wantRates, err := inProcess.ServiceErrorRates(context.Background(), projectID, errorRates)
	require.NoError(t, err)
	gotRates, err := spread.ServiceErrorRates(context.Background(), projectID, errorRates)
	require.NoError(t, err)
	// svc-b's errors (traces 0, 4, 8) lead; svc-c's CLIENT spans aren't requests.
	require.Len(t, wantRates, 3)
	require.Equal(t, "svc-b", wantRates[0].ServiceName)
	require.Equal(t, int64(3), wantRates[0].Errors+wantRates[0].PreviousErrors)
	require.Equal(t, int64(12), wantRates[0].Requests+wantRates[0].PreviousRequests)
	require.Equal(t, wantRates, gotRates)

	llm := &spans_core.LLMCallsQueryDTO{From: from, To: to, Args: []any{int64(60)},
		SQL: `SELECT model, count(*)::BIGINT, sum(input_tokens)::DOUBLE, quantile_cont(duration_ms, 0.95)::DOUBLE,
	count(DISTINCT time_bucket(to_seconds(?::BIGINT), ts::TIMESTAMP))::BIGINT FROM llm GROUP BY 1 ORDER BY 1`}
	wantLLM, err := inProcess.QueryLLMCalls(context.Background(), projectID, llm)
	require.NoError(t, err)
	gotLLM, err := spread.QueryLLMCalls(context.Background(), projectID, llm)
	require.NoError(t, err)
	require.Len(t, wantLLM.Rows, 3)
	require.Equal(t, int64(4), wantLLM.Rows[0][1])
	require.Equal(t, wantLLM, gotLLM)
}

// The service map and Traces summary queries (one fragment each: a trace's
// spans must meet) answer the same on one remote executor - here with two
// segments not uploaded yet, shipped to it - as in-process, and they did
// run there.
func Test_SingleFragmentSpanQueries_OnOneExecutor_MatchInProcess(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	projectID := seedSplitTraces(t, now, 2)
	inProcess, err := NewSpanQuerier(WithQueryDir(filepath.Join(t.TempDir(), "query")))
	require.NoError(t, err)
	t.Cleanup(func() { _ = inProcess.Close() })
	remote, err := NewSpanQuerier(WithQueryDir(filepath.Join(t.TempDir(), "query")))
	require.NoError(t, err)
	t.Cleanup(func() { _ = remote.Close() })
	var executors []query.Executor
	var recorders []*shippedRecorder
	for i := range 3 {
		recorder := &shippedRecorder{LocalExecutor: query.NewLocalExecutor(fmt.Sprintf("e%d", i), remote.db, nil, "")}
		recorders = append(recorders, recorder)
		executors = append(executors, recorder)
	}
	local := &shippedRecorder{LocalExecutor: query.NewLocalExecutor("local", remote.db, nil, "")}
	remote.coordinator, err = query.NewCoordinator(filepath.Join(t.TempDir(), "remote"), remote.db,
		local, func() []query.Executor { return executors },
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	from, to := now.Add(-time.Hour), now.Add(time.Hour)
	window := &spans_core.TimeRangeDTO{From: &from, To: &to}

	wantNodes, err := inProcess.ServiceNodeStats(context.Background(), projectID, window, 0)
	require.NoError(t, err)
	gotNodes, err := remote.ServiceNodeStats(context.Background(), projectID, window, 0)
	require.NoError(t, err)
	require.NotEmpty(t, wantNodes.Services)
	// Rows come in GROUP BY order, which no query fixes.
	require.ElementsMatch(t, wantNodes.Services, gotNodes.Services)
	require.ElementsMatch(t, wantNodes.PeerEdges, gotNodes.PeerEdges)
	require.ElementsMatch(t, wantNodes.Peers, gotNodes.Peers)

	wantEdges, err := inProcess.ServiceCallEdges(context.Background(), projectID, window, 0)
	require.NoError(t, err)
	gotEdges, err := remote.ServiceCallEdges(context.Background(), projectID, window, 0)
	require.NoError(t, err)
	require.NotEmpty(t, wantEdges)
	require.ElementsMatch(t, wantEdges, gotEdges)

	wantCounts, err := inProcess.ServiceErrorSpanCounts(projectID, window, []string{"svc-a", "svc-b"}, []string{"c"})
	require.NoError(t, err)
	gotCounts, err := remote.ServiceErrorSpanCounts(projectID, window, []string{"svc-a", "svc-b"}, []string{"c"})
	require.NoError(t, err)
	require.Equal(t, int64(3), wantCounts["svc-b"])
	require.Equal(t, wantCounts, gotCounts)

	for _, request := range []*spans_core.TraceSummaryQuery{
		{From: now.Add(-5 * time.Minute), To: to, PreviousFrom: from, StepMs: 60_000, MatchCap: 100},
		{From: from, To: to, PreviousFrom: from.Add(-time.Hour), StepMs: 120_000, ServiceName: "svc-b",
			ErrorsOnly: true, MatchCap: 100},
	} {
		want, err := inProcess.TraceSummary(context.Background(), projectID, request)
		require.NoError(t, err)
		got, err := remote.TraceSummary(context.Background(), projectID, request)
		require.NoError(t, err)
		require.NotZero(t, want.MatchingTraces)
		for _, result := range []*spans_core.TraceSummaryResult{want, got} {
			slices.SortFunc(result.Buckets, func(a, b spans_core.TraceSummaryBucket) int {
				return cmp.Compare(a.Index, b.Index)
			})
		}
		require.Equal(t, want, got, "trace summary %+v", request)
	}

	shipped := 0
	for _, recorder := range recorders {
		shipped += recorder.localFiles
	}
	// 4 single-fragment queries (peers, edges, error counts, and the second
	// summary's service + errors count, which joins each root to its trace),
	// each with both local segments shipped. Span stats queries (services,
	// tiles, root counts) merge partials, so they spread, their local parts
	// in-process.
	require.Equal(t, 2*4, shipped, "every single-fragment query ran on an executor")
	require.Equal(t, 4, local.fragments, "only the spread stats queries' local parts ran here")
}

// seedSplitTraces writes 12 four-span traces (gateway -> svc-a -> svc-c,
// gateway -> svc-b; every fourth svc-b span errors) with each trace's
// spans spread over six segments, all uploaded but the last local ones.
// svc-a carries a k8s namespace that changes from ns-old to ns-new halfway
// through.
func seedSplitTraces(t *testing.T, now time.Time, local int) uuid.UUID {
	t.Helper()
	// These segments are marked uploaded: hold the lock the uploader tests
	// take before resetting every uploaded spans segment.
	test_utils.AcquireExclusiveGlobalQueue(t, "spans-uploaded-queue")
	ingester, err := spans_ingester.NewSpanIngester(t.TempDir(), segments.WithMaxRows(1_000_000))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ingester.Close() })
	projectID := uuid.New()
	bySegment := make([][]*spans_core.Span, 6)
	for trace := range 12 {
		traceID := fmt.Sprintf("trace-%02d", trace)
		start := now.Add(-time.Duration(trace) * 47 * time.Second)
		namespace := "ns-old"
		if trace < 6 {
			namespace = "ns-new"
		}
		status := spans_core.StatusCodeOK
		if trace%4 == 0 {
			status = spans_core.StatusCodeError
		}
		spanList := []*spans_core.Span{
			{SpanID: "root", Name: "GET /", ServiceName: "gateway", Kind: spans_core.SpanKindServer,
				StartTime: start, EndTime: start.Add(time.Duration(1000+trace*300) * time.Millisecond)},
			{SpanID: "a", ParentSpanID: "root", Name: "GET /a", ServiceName: "svc-a", Kind: spans_core.SpanKindServer,
				StartTime: start.Add(100 * time.Millisecond), EndTime: start.Add(600 * time.Millisecond),
				ResourceAttributes: map[string]any{"k8s.namespace.name": namespace}},
			{
				SpanID:       "b",
				ParentSpanID: "root",
				Name:         "GET /b",
				ServiceName:  "svc-b",
				Kind:         spans_core.SpanKindServer,
				StartTime: start.Add(
					200 * time.Millisecond,
				),
				EndTime:    start.Add(900 * time.Millisecond),
				StatusCode: status,
			},
			{SpanID: "c", ParentSpanID: "a", Name: "query", ServiceName: "svc-c", Kind: spans_core.SpanKindClient,
				StartTime: start.Add(300 * time.Millisecond), EndTime: start.Add(400 * time.Millisecond),
				Attributes: map[string]any{"gen_ai.operation.name": "chat",
					"gen_ai.request.model": fmt.Sprintf("model-%d", trace%3), "gen_ai.usage.input_tokens": trace * 10}},
		}
		for i, span := range spanList {
			span.ID, span.ProjectID, span.TraceID = uuid.New(), projectID, traceID
			if span.StatusCode == "" {
				span.StatusCode = spans_core.StatusCodeOK
			}
			bySegment[(trace+i)%6] = append(bySegment[(trace+i)%6], span)
		}
	}
	for _, batch := range bySegment {
		require.NoError(t, ingester.StoreSpansBatch(map[uuid.UUID][]*spans_core.Span{projectID: batch}))
		_, err = ingester.FlushAll()
		require.NoError(t, err)
	}
	repo := &segments.SegmentRepository{}
	records, err := repo.ListByProject(projectID, "spans")
	require.NoError(t, err)
	require.Len(t, records, 6)
	for _, record := range records[:len(records)-local] {
		require.NoError(t, repo.MarkUploaded(record.ID, "spans/"+record.ID.String()+".parquet"))
	}
	return projectID
}

// shippedRecorder counts the fragments it runs and their local files.
type shippedRecorder struct {
	*query.LocalExecutor
	mu         sync.Mutex
	fragments  int
	localFiles int
}

func (r *shippedRecorder) Execute(ctx context.Context, fragment query.Fragment, path string) error {
	r.mu.Lock()
	r.fragments++
	r.localFiles += len(fragment.LocalFiles)
	r.mu.Unlock()
	return r.LocalExecutor.Execute(ctx, fragment, path)
}

func sortedServices(traces []spans_core.TraceSummaryDTO) []spans_core.TraceSummaryDTO {
	result := slices.Clone(traces)
	for i := range result {
		result[i].Services = slices.Sorted(slices.Values(result[i].Services))
	}
	return result
}
