package spans_querier

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"testing"
	"time"

	spans_compactor "obzly/internal/features/spans/compactor"
	spans_core "obzly/internal/features/spans/core"
	spans_ingester "obzly/internal/features/spans/ingester"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// rollupAnswers is every span stats view over one window.
type rollupAnswers struct {
	summaries    []*spans_core.TraceSummaryResult
	aggregations []*spans_core.AggregationResponseDTO
	errorRates   []spans_core.ServiceErrorRate
	nodes        *spans_core.ServiceNodeStats
	edges        []spans_core.ServiceCallEdge
	k8s          map[string]spans_core.K8sResourceInfo
	services     *spans_core.ListServicesResponseDTO
	operations   *spans_core.ListOperationsResponseDTO
}

// Once segments are rolled up, every stats view answers as it did from raw
// spans: whole hours come from spans_1h, other whole minutes from spans_1m,
// the window's partial minutes raw.
func Test_SpanStats_AfterRollup_AnswerAsRawSpansDid(t *testing.T) {
	base := time.Now().UTC().Add(-7 * time.Hour).Truncate(time.Hour)
	projectID, dataDir := seedRollupTraces(t, base, 3)
	querier, err := NewSpanQuerier(WithQueryDir(filepath.Join(t.TempDir(), "query")))
	require.NoError(t, err)
	t.Cleanup(func() { _ = querier.Close() })
	from := base.Add(17*time.Second + 250*time.Millisecond)
	to := base.Add(4*time.Hour + 41*time.Minute + 3*time.Second)

	raw := collectRollupAnswers(t, querier, projectID, from, to)

	repo := &segments.SegmentRepository{}
	records, err := repo.ListByProject(projectID, spans_core.StreamSpans)
	require.NoError(t, err)
	require.Len(t, records, 3)
	ids := make([]uuid.UUID, len(records))
	for i, record := range records {
		ids[i] = record.ID
	}
	require.NoError(t, repo.SetLevel(ids, segments.LevelCompacted))
	compactor, err := spans_compactor.NewSpanCompactor(dataDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = compactor.Close() })
	require.NoError(t, compactor.RollupSegments(projectID))

	inputs, err := querier.statsInputs(projectID, []interval{{start: from, end: to, includeEnd: true}},
		segments.PruningHints{}, spanStats.tiers())
	require.NoError(t, err)
	kinds := map[rangeKind]string{}
	for _, input := range inputs {
		kinds[input.kind] = input.stream
	}
	require.Equal(t, spans_core.StreamSpansHourRollup, kinds[wholeHours])
	require.Equal(t, spans_core.StreamSpansRollup, kinds[minutesOutsideHours])
	require.Equal(t, spans_core.StreamSpans, kinds[minuteEdges])
	require.Empty(t, inputs[0].records, "every segment is rolled up")

	rolled := collectRollupAnswers(t, querier, projectID, from, to)
	require.Equal(t, raw.summaries, rolled.summaries)
	require.Equal(t, raw.aggregations, rolled.aggregations)
	require.Equal(t, raw.errorRates, rolled.errorRates)
	require.ElementsMatch(t, raw.nodes.Services, rolled.nodes.Services)
	require.ElementsMatch(t, raw.nodes.Peers, rolled.nodes.Peers)
	require.ElementsMatch(t, raw.nodes.PeerEdges, rolled.nodes.PeerEdges)
	require.ElementsMatch(t, raw.edges, rolled.edges)
	require.Equal(t, raw.k8s, rolled.k8s)
	require.Equal(t, raw.services, rolled.services)
	require.Equal(t, raw.operations, rolled.operations)
	require.Equal(t, "ns-4", rolled.k8s["svc-a"].Namespace, "the latest namespace in the window")
}

// Percentiles come from the duration histogram: within its resolution of
// the exact value.
func Test_ExecuteAggregation_Percentile_IsWithinHistogramResolution(t *testing.T) {
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Minute)
	projectID, _ := seedRollupTraces(t, base, 1)
	querier, err := NewSpanQuerier(WithQueryDir(filepath.Join(t.TempDir(), "query")))
	require.NoError(t, err)
	t.Cleanup(func() { _ = querier.Close() })
	from, to := base, base.Add(2*time.Hour)

	response, err := querier.ExecuteAggregation(projectID, &spans_core.AggregationRequestDTO{
		TimeRange: &spans_core.TimeRangeDTO{From: &from, To: &to}, ServiceName: "svc-b",
		Metric: spans_core.AggregationMetricP95Duration,
	})
	require.NoError(t, err)
	require.Len(t, response.Buckets, 1)
	// svc-b's durations are 5..904 ms, evenly spread: p95 is about 859 ms.
	require.InEpsilon(t, 859, response.Buckets[0].Value, 0.05)
}

// A trace search reads the newest minutes first and widens only while it
// lacks traces, with the same answer as one search over the whole range.
func Test_ListTraces_NewestFirstWindows_MatchOneSearchOverTheRange(t *testing.T) {
	base := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Minute)
	projectID, _ := seedRollupTraces(t, base, 1)
	querier, err := NewSpanQuerier(WithQueryDir(filepath.Join(t.TempDir(), "query")))
	require.NoError(t, err)
	t.Cleanup(func() { _ = querier.Close() })
	from, to := base, base.Add(5*time.Hour)
	window := &spans_core.TimeRangeDTO{From: &from, To: &to}
	minDuration := 1500.0

	for _, request := range []*spans_core.ListTracesRequestDTO{
		{TimeRange: window, Limit: 5},
		{TimeRange: window, ErrorsOnly: true, Limit: 7},
		{TimeRange: window, MinDurationMs: &minDuration, Limit: 3},
		{TimeRange: window, OperationName: "GET /rare", Limit: 20},
	} {
		got, err := querier.ListTraces(context.Background(), projectID, request)
		require.NoError(t, err)
		want, err := querier.listTracesIn(context.Background(), projectID, request, from, to, request.Limit)
		require.NoError(t, err)
		require.NotEmpty(t, want)
		require.Equal(t, sortedServices(want), sortedServices(got.Traces), "trace search %+v", request)
	}
}

func Test_NewestFirstWindows_WidenToTheWholeRangeWithoutOverflow(t *testing.T) {
	from := time.Unix(0, 0).UTC()
	to := time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)

	starts := newestFirstWindows(from, to)

	require.Equal(t, from, starts[len(starts)-1])
	require.Equal(t, to.Add(-10*time.Minute), starts[0])
	for i := 1; i < len(starts); i++ {
		require.True(t, starts[i].Before(starts[i-1]), "window %d starts before window %d", i, i-1)
	}
	require.Equal(t, []time.Time{to.Add(-time.Minute)}, newestFirstWindows(to.Add(-time.Minute), to),
		"a range shorter than the first window is one search")
}

// Matching-traces counts with a service and/or errors filter come from
// spans_1h_traces (whole hours) and spans_1m_traces (other whole minutes)
// once segments are rolled up, equal to the windowed raw
// count (which an operation filter still uses); duration bounds there are
// at histogram resolution.
func Test_MatchingTraces_FromTraceRollups_MatchRawCount(t *testing.T) {
	base := time.Now().UTC().Add(-7 * time.Hour).Truncate(time.Hour)
	projectID, dataDir := seedRollupTraces(t, base, 3)
	querier, err := NewSpanQuerier(WithQueryDir(filepath.Join(t.TempDir(), "query")))
	require.NoError(t, err)
	t.Cleanup(func() { _ = querier.Close() })
	from := base.Add(17*time.Second + 250*time.Millisecond)
	to := base.Add(4*time.Hour + 41*time.Minute + 3*time.Second)
	minDuration := 1200.0
	requests := []*spans_core.TraceSummaryQuery{
		{From: from, To: to, ErrorsOnly: true},
		{From: from, To: to, ServiceName: "svc-a"},
		{From: from, To: to, ServiceName: "svc-b", ErrorsOnly: true},
		{From: from, To: to, ServiceName: "svc-c", ErrorsOnly: true, MinDurationMs: &minDuration},
	}
	raw := make([]int64, len(requests))
	for i, request := range requests {
		request.MatchCap = 10_000
		raw[i], err = querier.matchingTracesRaw(context.Background(), projectID, request)
		require.NoError(t, err)
		require.NotZero(t, raw[i], "request %d", i)
		fromRaw, err := querier.matchingTraces(context.Background(), projectID, request)
		require.NoError(t, err)
		require.Equal(t, raw[i], fromRaw, "request %d before rollup", i)
	}

	rollUpAll(t, projectID, dataDir)
	inputs, err := querier.statsInputs(projectID, []interval{{start: from, end: to, includeEnd: true}},
		segments.PruningHints{}, traceStats.tiers())
	require.NoError(t, err)
	require.Empty(t, inputs[0].records, "every segment is rolled up")
	kinds := map[rangeKind]string{}
	for _, input := range inputs {
		kinds[input.kind] = input.stream
	}
	require.Equal(t, spans_core.StreamSpansTraceHourRollup, kinds[wholeHours])
	require.Equal(t, spans_core.StreamSpansTraceRollup, kinds[minutesOutsideHours])
	require.Equal(t, spans_core.StreamSpans, kinds[minuteEdges])

	for i, request := range requests {
		got, err := querier.matchingTraces(context.Background(), projectID, request)
		require.NoError(t, err)
		if request.MinDurationMs != nil {
			require.InEpsilon(t, raw[i], got, 0.1, "request %d", i)
			continue
		}
		require.Equal(t, raw[i], got, "request %d", i)
	}
	require.Equal(t, int64(120), raw[0], "every seventh of the window's 843 traces fails")
}

func rollUpAll(t *testing.T, projectID uuid.UUID, dataDir string) {
	t.Helper()
	repo := &segments.SegmentRepository{}
	records, err := repo.ListByProject(projectID, spans_core.StreamSpans)
	require.NoError(t, err)
	ids := make([]uuid.UUID, len(records))
	for i, record := range records {
		ids[i] = record.ID
	}
	require.NoError(t, repo.SetLevel(ids, segments.LevelCompacted))
	compactor, err := spans_compactor.NewSpanCompactor(dataDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = compactor.Close() })
	require.NoError(t, compactor.RollupSegments(projectID))
}

func collectRollupAnswers(
	t *testing.T, querier *SpanQuerier, projectID uuid.UUID, from, to time.Time,
) rollupAnswers {
	t.Helper()
	ctx := context.Background()
	window := &spans_core.TimeRangeDTO{From: &from, To: &to}
	var answers rollupAnswers
	for _, request := range []*spans_core.TraceSummaryQuery{
		{From: from.Add(2 * time.Hour), To: to, PreviousFrom: from, StepMs: 600_000, RootSpansOnly: true,
			MatchCap: 10_000},
		{From: from.Add(2 * time.Hour), To: to, PreviousFrom: from, StepMs: 3_600_000, ServiceName: "svc-b",
			ErrorsOnly: true, MatchCap: 10_000},
	} {
		summary, err := querier.TraceSummary(ctx, projectID, request)
		require.NoError(t, err)
		require.NotZero(t, summary.Current.Count)
		slices.SortFunc(summary.Buckets, func(a, b spans_core.TraceSummaryBucket) int {
			return cmp.Compare(a.Index, b.Index)
		})
		answers.summaries = append(answers.summaries, summary)
	}
	for _, request := range []*spans_core.AggregationRequestDTO{
		{TimeRange: window},
		{TimeRange: window, GroupField: spans_core.GroupFieldServiceName},
		{TimeRange: window, Metric: spans_core.AggregationMetricErrorCount, GroupField: spans_core.GroupFieldName},
		{TimeRange: window, Metric: spans_core.AggregationMetricP95Duration, GroupField: spans_core.GroupFieldKind},
		{TimeRange: window, Metric: spans_core.AggregationMetricP50Duration, BucketMs: 3_600_000},
		{TimeRange: window, RootSpansOnly: true, BucketMs: 600_000, GroupField: spans_core.GroupFieldStatusCode},
		{TimeRange: window, Metric: spans_core.AggregationMetricAvgDuration, ServiceName: "svc-a"},
	} {
		response, err := querier.ExecuteAggregation(projectID, request)
		require.NoError(t, err)
		require.NotEmpty(t, response.Buckets)
		for i := range response.Buckets {
			response.Buckets[i].Value = roundTo(response.Buckets[i].Value, 6)
		}
		answers.aggregations = append(answers.aggregations, response)
	}
	var err error
	answers.errorRates, err = querier.ServiceErrorRates(ctx, projectID, &spans_core.ServiceErrorRatesQuery{
		From: from.Add(90 * time.Minute), To: to, PreviousFrom: from, Limit: 10,
	})
	require.NoError(t, err)
	require.NotEmpty(t, answers.errorRates)
	answers.nodes, err = querier.ServiceNodeStats(context.Background(), projectID, window, 0)
	require.NoError(t, err)
	require.NotEmpty(t, answers.nodes.Peers)
	for i := range answers.nodes.PeerEdges {
		answers.nodes.PeerEdges[i].AvgDurationMs = roundTo(answers.nodes.PeerEdges[i].AvgDurationMs, 6)
	}
	answers.edges, err = querier.ServiceCallEdges(context.Background(), projectID, window, 0)
	require.NoError(t, err)
	require.NotEmpty(t, answers.edges)
	for i := range answers.edges {
		answers.edges[i].AvgDurationMs = roundTo(answers.edges[i].AvgDurationMs, 6)
	}
	answers.k8s, err = querier.ServiceK8sMetadata(context.Background(), projectID, window, 0)
	require.NoError(t, err)
	answers.services, err = querier.ListServices(context.Background(), projectID, window, 0)
	require.NoError(t, err)
	answers.operations, err = querier.ListOperations(context.Background(), projectID, "gateway", window, 0)
	require.NoError(t, err)
	return answers
}

// seedRollupTraces writes a trace every 20 s for five hours from base, split
// over segments by time (no trace straddles two): gateway -> svc-a ->
// svc-c (a client span calling Postgres) and gateway -> svc-b, every
// seventh svc-b span failing, svc-b lasting 5..904 ms. svc-a's namespace
// is ns-<hour>. One trace in 50 is GET /rare.
func seedRollupTraces(t *testing.T, base time.Time, segmentCount int) (uuid.UUID, string) {
	t.Helper()
	dataDir := t.TempDir()
	ingester, err := spans_ingester.NewSpanIngester(dataDir, segments.WithMaxRows(1_000_000))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ingester.Close() })
	projectID := uuid.New()
	const traces = 900
	bySegment := make([][]*spans_core.Span, segmentCount)
	for trace := range traces {
		start := base.Add(time.Duration(trace)*20*time.Second + time.Duration(trace%13)*time.Millisecond)
		bDuration := time.Duration(5+(trace*37)%900) * time.Millisecond
		status := spans_core.StatusCodeOK
		if trace%7 == 0 {
			status = spans_core.StatusCodeError
		}
		rootName := "GET /"
		if trace%50 == 0 {
			rootName = "GET /rare"
		}
		traceID := fmt.Sprintf("trace-%04d", trace)
		spanList := []*spans_core.Span{
			{SpanID: "root", Name: rootName, ServiceName: "gateway", Kind: spans_core.SpanKindServer,
				StartTime: start, EndTime: start.Add(bDuration + time.Duration(trace%5)*400*time.Millisecond)},
			{SpanID: "a", ParentSpanID: "root", Name: "GET /a", ServiceName: "svc-a", Kind: spans_core.SpanKindServer,
				StartTime: start.Add(time.Millisecond), EndTime: start.Add(time.Duration(2+trace%40) * time.Millisecond),
				ResourceAttributes: map[string]any{"k8s.namespace.name": fmt.Sprintf("ns-%d", trace/180)}},
			{SpanID: "b", ParentSpanID: "root", Name: "GET /b", ServiceName: "svc-b", Kind: spans_core.SpanKindServer,
				StartTime: start.Add(time.Millisecond), EndTime: start.Add(time.Millisecond + bDuration), StatusCode: status},
			{SpanID: "c", ParentSpanID: "a", Name: "SELECT", ServiceName: "svc-c", Kind: spans_core.SpanKindClient,
				StartTime: start.Add(time.Millisecond), EndTime: start.Add(time.Duration(2+trace%3) * time.Millisecond),
				Attributes: map[string]any{"db.system": "postgresql", "server.address": "db.example.com"}},
		}
		segment := trace * segmentCount / traces
		for _, span := range spanList {
			span.ID, span.ProjectID, span.TraceID = uuid.New(), projectID, traceID
			if span.StatusCode == "" {
				span.StatusCode = spans_core.StatusCodeOK
			}
		}
		bySegment[segment] = append(bySegment[segment], spanList...)
	}
	for _, batch := range bySegment {
		require.NoError(t, ingester.StoreSpansBatch(map[uuid.UUID][]*spans_core.Span{projectID: batch}))
		_, err = ingester.FlushAll()
		require.NoError(t, err)
	}
	return projectID, dataDir
}

func roundTo(value float64, digits int) float64 {
	scale := math.Pow(10, float64(digits))
	return math.Round(value*scale) / scale
}
