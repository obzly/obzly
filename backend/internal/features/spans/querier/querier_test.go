package spans_querier_test

import (
	"context"
	"testing"
	"time"

	spans_core "obzly/internal/features/spans/core"
	spans_ingester "obzly/internal/features/spans/ingester"
	spans_querier "obzly/internal/features/spans/querier"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedProject ingests spans for a fresh project through the real ingester
// and returns the project ID and a querier.
func seedProject(t *testing.T, spanList []*spans_core.Span) (uuid.UUID, *spans_querier.SpanQuerier) {
	t.Helper()

	ingester, err := spans_ingester.NewSpanIngester(t.TempDir(), segments.WithMaxRows(1_000_000))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ingester.Close() })

	projectID := uuid.New()
	for _, span := range spanList {
		span.ProjectID = projectID
		if span.ID == uuid.Nil {
			span.ID = uuid.New()
		}
	}

	require.NoError(t, ingester.StoreSpansBatch(map[uuid.UUID][]*spans_core.Span{projectID: spanList}))
	_, err = ingester.FlushAll()
	require.NoError(t, err)

	querier, err := spans_querier.NewSpanQuerier()
	require.NoError(t, err)
	t.Cleanup(func() { _ = querier.Close() })

	return projectID, querier
}

// oneTrace builds a small, realistic 3-span trace: a gateway root spanning
// two children (one erroring), starting at base.
func oneTrace(traceID string, base time.Time) []*spans_core.Span {
	return []*spans_core.Span{
		{
			TraceID: traceID, SpanID: "s1", ParentSpanID: "",
			Name: "GET /checkout", ServiceName: "gateway", Kind: spans_core.SpanKindServer,
			StartTime: base, EndTime: base.Add(100 * time.Millisecond),
			StatusCode: spans_core.StatusCodeOK,
		},
		{
			TraceID: traceID, SpanID: "s2", ParentSpanID: "s1",
			Name: "POST /charge", ServiceName: "payments", Kind: spans_core.SpanKindClient,
			StartTime: base.Add(10 * time.Millisecond), EndTime: base.Add(60 * time.Millisecond),
			StatusCode: spans_core.StatusCodeOK,
		},
		{
			TraceID: traceID, SpanID: "s3", ParentSpanID: "s1",
			Name: "SELECT inventory", ServiceName: "inventory", Kind: spans_core.SpanKindClient,
			StartTime: base.Add(20 * time.Millisecond), EndTime: base.Add(50 * time.Millisecond),
			StatusCode: spans_core.StatusCodeError, StatusMessage: "connection refused",
		},
	}
}

func timeRange(from, to time.Time) *spans_core.TimeRangeDTO {
	return &spans_core.TimeRangeDTO{From: &from, To: &to}
}

func Test_ListTraces_ReturnsOneSummaryPerTrace(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Millisecond)
	var allSpans []*spans_core.Span
	allSpans = append(allSpans, oneTrace("trace-a", base)...)
	allSpans = append(allSpans, &spans_core.Span{
		TraceID:     "trace-b",
		SpanID:      "s4",
		Name:        "GET /health",
		ServiceName: "gateway",
		Kind:        spans_core.SpanKindServer,
		StartTime:   base.Add(time.Minute),
		EndTime:     base.Add(time.Minute + 5*time.Millisecond),
		StatusCode:  spans_core.StatusCodeOK,
	})
	projectID, querier := seedProject(t, allSpans)

	response, err := querier.ListTraces(context.Background(), projectID, &spans_core.ListTracesRequestDTO{
		TimeRange: timeRange(base.Add(-time.Minute), base.Add(time.Hour)),
	})
	require.NoError(t, err)
	require.Len(t, response.Traces, 2)

	// Newest trace first (trace-b started a minute after trace-a).
	assert.Equal(t, "trace-b", response.Traces[0].TraceID)
	assert.Equal(t, int64(1), response.Traces[0].SpanCount)

	assert.Equal(t, "trace-a", response.Traces[1].TraceID)
	assert.Equal(t, int64(3), response.Traces[1].SpanCount)
	assert.Equal(t, int64(1), response.Traces[1].ErrorCount)
	assert.Equal(t, "gateway", response.Traces[1].RootServiceName)
	assert.Equal(t, "GET /checkout", response.Traces[1].RootName)
	assert.InDelta(t, 100, response.Traces[1].DurationMs, 1)
	assert.ElementsMatch(t, []string{"gateway", "payments", "inventory"}, response.Traces[1].Services)
}

func Test_ListTraces_FilterByService_MatchesTraceWithAnySpanInService(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Millisecond)
	projectID, querier := seedProject(t, oneTrace("trace-a", base))

	// "payments" only appears on a child span, not the root - the whole
	// trace (all 3 spans) should still come back.
	response, err := querier.ListTraces(context.Background(), projectID, &spans_core.ListTracesRequestDTO{
		TimeRange:   timeRange(base.Add(-time.Minute), base.Add(time.Hour)),
		ServiceName: "payments",
	})
	require.NoError(t, err)
	require.Len(t, response.Traces, 1)
	assert.Equal(t, int64(3), response.Traces[0].SpanCount)
}

func Test_ListTraces_ErrorsOnly_ExcludesTracesWithNoErrors(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Millisecond)
	var allSpans []*spans_core.Span
	allSpans = append(allSpans, oneTrace("trace-with-error", base)...)
	allSpans = append(allSpans, &spans_core.Span{
		TraceID: "trace-clean", SpanID: "s9", Name: "GET /ok", ServiceName: "gateway",
		StartTime: base.Add(time.Minute), EndTime: base.Add(time.Minute + time.Millisecond),
		StatusCode: spans_core.StatusCodeOK,
	})
	projectID, querier := seedProject(t, allSpans)

	response, err := querier.ListTraces(context.Background(), projectID, &spans_core.ListTracesRequestDTO{
		TimeRange:  timeRange(base.Add(-time.Minute), base.Add(time.Hour)),
		ErrorsOnly: true,
	})
	require.NoError(t, err)
	require.Len(t, response.Traces, 1)
	assert.Equal(t, "trace-with-error", response.Traces[0].TraceID)
}

func Test_ListTraces_DurationBounds_FilterByTotalTraceDuration(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Millisecond)
	projectID, querier := seedProject(t, oneTrace("trace-a", base)) // ~100ms

	minDuration := 200.0
	response, err := querier.ListTraces(context.Background(), projectID, &spans_core.ListTracesRequestDTO{
		TimeRange:     timeRange(base.Add(-time.Minute), base.Add(time.Hour)),
		MinDurationMs: &minDuration,
	})
	require.NoError(t, err)
	assert.Empty(t, response.Traces)
}

func Test_GetTrace_ReturnsAllSpansOrderedByStartTime(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Millisecond)
	projectID, querier := seedProject(t, oneTrace("trace-a", base))

	response, err := querier.GetTrace(projectID, "trace-a", nil)
	require.NoError(t, err)
	require.Len(t, response.Spans, 3)
	assert.Equal(t, "s1", response.Spans[0].SpanID)
	assert.Equal(t, "s2", response.Spans[1].SpanID)
	assert.Equal(t, "s3", response.Spans[2].SpanID)
	// s2 (POST /charge): base+10ms to base+60ms = 50ms.
	assert.InDelta(t, 50, response.Spans[1].DurationMs, 1)
	assert.Equal(t, "ERROR", response.Spans[2].StatusCode)
	assert.Equal(t, "connection refused", response.Spans[2].StatusMessage)
}

func Test_GetTraces_SeveralTraces_ReturnsEachOnesSpansInOneScan(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Millisecond)
	spanList := append(oneTrace("trace-a", base), oneTrace("trace-b", base.Add(time.Second))...)
	spanList = append(spanList, oneTrace("trace-c", base.Add(2*time.Second))...)
	projectID, querier := seedProject(t, spanList)

	spans, err := querier.GetTraces(context.Background(), projectID, []string{"trace-a", "trace-c", "missing"},
		timeRange(base.Add(-time.Minute), base.Add(time.Minute)), 0)
	require.NoError(t, err)

	require.Len(t, spans, 2)
	for _, traceID := range []string{"trace-a", "trace-c"} {
		require.Len(t, spans[traceID], 3, traceID)
		assert.Equal(t, []string{"s1", "s2", "s3"},
			[]string{spans[traceID][0].SpanID, spans[traceID][1].SpanID, spans[traceID][2].SpanID})
	}
}

func Test_GetTrace_UnknownTraceID_ReturnsEmptySpans(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Millisecond)
	projectID, querier := seedProject(t, oneTrace("trace-a", base))

	response, err := querier.GetTrace(projectID, "does-not-exist", nil)
	require.NoError(t, err)
	assert.Empty(t, response.Spans)
}

func Test_GetTrace_WithAttributesAndEvents_RoundTripsJSON(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Millisecond)
	spanList := []*spans_core.Span{
		{
			TraceID: "trace-a", SpanID: "s1", Name: "GET /x", ServiceName: "svc",
			StartTime: base, EndTime: base.Add(10 * time.Millisecond),
			StatusCode:         spans_core.StatusCodeOK,
			Attributes:         map[string]any{"http.status_code": float64(200)},
			ResourceAttributes: map[string]any{"service.version": "1.2.3"},
			Events: []spans_core.SpanEvent{
				{
					Name:       "cache.miss",
					Timestamp:  base.Add(5 * time.Millisecond),
					Attributes: map[string]any{"key": "abc"},
				},
			},
		},
	}
	projectID, querier := seedProject(t, spanList)

	response, err := querier.GetTrace(projectID, "trace-a", nil)
	require.NoError(t, err)
	require.Len(t, response.Spans, 1)

	span := response.Spans[0]
	assert.Equal(t, float64(200), span.Attributes["http_status_code"])
	assert.Equal(t, "1.2.3", span.ResourceAttributes["service_version"])
	require.Len(t, span.Events, 1)
	assert.Equal(t, "cache.miss", span.Events[0].Name)
}

func Test_ListServices_ReturnsDistinctServiceNamesSorted(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Millisecond)
	projectID, querier := seedProject(t, oneTrace("trace-a", base))

	response, err := querier.ListServices(projectID, timeRange(base.Add(-time.Minute), base.Add(time.Hour)))
	require.NoError(t, err)
	assert.Equal(t, []string{"gateway", "inventory", "payments"}, response.Services)
}

func Test_ListOperations_ReturnsOperationsForOneService(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Millisecond)
	spanList := append(oneTrace("trace-a", base), &spans_core.Span{
		TraceID: "trace-b", SpanID: "s5", Name: "GET /cart", ServiceName: "gateway",
		StartTime: base.Add(time.Second), EndTime: base.Add(time.Second + time.Millisecond),
		StatusCode: spans_core.StatusCodeOK,
	})
	projectID, querier := seedProject(t, spanList)

	response, err := querier.ListOperations(
		projectID,
		"gateway",
		timeRange(base.Add(-time.Minute), base.Add(time.Hour)),
	)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"GET /checkout", "GET /cart"}, response.Operations)
}

func Test_ListTraces_NoSegmentsForProject_ReturnsEmpty(t *testing.T) {
	_, querier := seedProject(t, oneTrace("trace-a", time.Now().UTC()))

	response, err := querier.ListTraces(context.Background(), uuid.New(), &spans_core.ListTracesRequestDTO{})
	require.NoError(t, err)
	assert.Empty(t, response.Traces)
}
