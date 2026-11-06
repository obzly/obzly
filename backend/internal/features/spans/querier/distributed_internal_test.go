package spans_querier

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
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
	projectID := seedSplitTraces(t, now)
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

	wantServices, err := inProcess.ListServices(projectID, window)
	require.NoError(t, err)
	gotServices, err := spread.ListServices(projectID, window)
	require.NoError(t, err)
	require.Equal(t, wantServices, gotServices)
	wantOperations, err := inProcess.ListOperations(projectID, "svc-a", window)
	require.NoError(t, err)
	gotOperations, err := spread.ListOperations(projectID, "svc-a", window)
	require.NoError(t, err)
	require.Equal(t, wantOperations, gotOperations)

	wantK8s, err := inProcess.ServiceK8sMetadata(projectID, window)
	require.NoError(t, err)
	gotK8s, err := spread.ServiceK8sMetadata(projectID, window)
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

	wantEdges, err := inProcess.ServiceCallEdges(projectID, window)
	require.NoError(t, err)
	gotEdges, err := spread.ServiceCallEdges(projectID, window)
	require.NoError(t, err)
	require.NotEmpty(t, wantEdges)
	require.ElementsMatch(t, wantEdges, gotEdges)

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

// seedSplitTraces writes 12 four-span traces (gateway -> svc-a -> svc-c,
// gateway -> svc-b; every fourth svc-b span errors) with each trace's
// spans spread over six uploaded segments. svc-a carries a k8s namespace
// that changes from ns-old to ns-new halfway through.
func seedSplitTraces(t *testing.T, now time.Time) uuid.UUID {
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
	for _, record := range records {
		require.NoError(t, repo.MarkUploaded(record.ID, "spans/"+record.ID.String()+".parquet"))
	}
	return projectID
}

func sortedServices(traces []spans_core.TraceSummaryDTO) []spans_core.TraceSummaryDTO {
	result := slices.Clone(traces)
	for i := range result {
		result[i].Services = slices.Sorted(slices.Values(result[i].Services))
	}
	return result
}
