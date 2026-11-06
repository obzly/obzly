package spans_querier_test

import (
	"context"
	"testing"
	"time"

	spans_core "obzly/internal/features/spans/core"
	spans_ingester "obzly/internal/features/spans/ingester"
	spans_querier "obzly/internal/features/spans/querier"
	"obzly/internal/storage/segments"
	"obzly/internal/telemetry"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func Test_ExecuteAggregation_ServiceFilter_SkipsOtherServicesSegments(t *testing.T) {
	base := time.Now().UTC().Add(-time.Minute)
	projectID, querier := seedTraceAcrossTwoSegments(t, base)
	before := prunedSpans()

	response, err := querier.ExecuteAggregation(projectID, &spans_core.AggregationRequestDTO{
		TimeRange:   timeRange(base.Add(-time.Minute), base.Add(time.Minute)),
		ServiceName: "payments",
		Metric:      spans_core.AggregationMetricCount,
	})

	require.NoError(t, err)
	require.Len(t, response.Buckets, 1)
	require.Equal(t, 1.0, response.Buckets[0].Value)
	require.Equal(t, before+1, prunedSpans(), "gateway's segment is never opened")
}

// A trace-level filter must not prune: the trace's other spans live in
// segments without that service, and its summary needs all of them.
func Test_ListTraces_ServiceFilter_KeepsTheWholeTraceAcrossSegments(t *testing.T) {
	base := time.Now().UTC().Add(-time.Minute)
	projectID, querier := seedTraceAcrossTwoSegments(t, base)
	before := prunedSpans()

	response, err := querier.ListTraces(context.Background(), projectID, &spans_core.ListTracesRequestDTO{
		TimeRange:   timeRange(base.Add(-time.Minute), base.Add(time.Minute)),
		ServiceName: "payments",
	})

	require.NoError(t, err)
	require.Len(t, response.Traces, 1)
	trace := response.Traces[0]
	require.EqualValues(t, 2, trace.SpanCount)
	require.Equal(t, "gateway", trace.RootServiceName)
	require.ElementsMatch(t, []string{"gateway", "payments"}, trace.Services)
	require.Equal(t, before, prunedSpans())
}

// seedTraceAcrossTwoSegments writes one trace whose gateway root span and
// payments child land in separate segments.
func seedTraceAcrossTwoSegments(t *testing.T, base time.Time) (uuid.UUID, *spans_querier.SpanQuerier) {
	t.Helper()
	ingester, err := spans_ingester.NewSpanIngester(t.TempDir(), segments.WithMaxRows(1_000_000))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ingester.Close() })

	projectID := uuid.New()
	for _, span := range []*spans_core.Span{
		{
			TraceID: "a1a1", SpanID: "root", Name: "GET /checkout", ServiceName: "gateway",
			Kind: spans_core.SpanKindServer, StartTime: base, EndTime: base.Add(100 * time.Millisecond),
			StatusCode: spans_core.StatusCodeOK,
		},
		{
			TraceID: "a1a1", SpanID: "child", ParentSpanID: "root", Name: "POST /charge", ServiceName: "payments",
			Kind: spans_core.SpanKindClient, StartTime: base.Add(10 * time.Millisecond),
			EndTime: base.Add(60 * time.Millisecond), StatusCode: spans_core.StatusCodeOK,
		},
	} {
		span.ID, span.ProjectID = uuid.New(), projectID
		require.NoError(t, ingester.StoreSpansBatch(map[uuid.UUID][]*spans_core.Span{projectID: {span}}))
		_, err := ingester.FlushAll()
		require.NoError(t, err)
	}

	querier, err := spans_querier.NewSpanQuerier()
	require.NoError(t, err)
	t.Cleanup(func() { _ = querier.Close() })
	return projectID, querier
}

func prunedSpans() float64 {
	return testutil.ToFloat64(telemetry.QuerySegmentsPruned.WithLabelValues("spans"))
}
