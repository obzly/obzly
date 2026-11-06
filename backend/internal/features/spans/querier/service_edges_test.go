package spans_querier_test

import (
	"testing"
	"time"

	spans_core "obzly/internal/features/spans/core"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_ServiceCallEdges_ReturnsAggregatedParentChildServicePairs(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Millisecond)
	// oneTrace is gateway(s1) -> payments(s2, OK), gateway(s1) -> inventory(s3, ERROR).
	projectID, querier := seedProject(t, oneTrace("trace-a", base))

	edges, err := querier.ServiceCallEdges(projectID, timeRange(base.Add(-time.Minute), base.Add(time.Hour)))
	require.NoError(t, err)
	require.Len(t, edges, 2)

	byDst := make(map[string]spans_core.ServiceCallEdge, len(edges))
	for _, edge := range edges {
		byDst[edge.DstService] = edge
	}

	payments, ok := byDst["payments"]
	require.True(t, ok)
	assert.Equal(t, "gateway", payments.SrcService)
	assert.Equal(t, int64(1), payments.CallCount)
	assert.Equal(t, int64(0), payments.ErrorCount)

	inventory, ok := byDst["inventory"]
	require.True(t, ok)
	assert.Equal(t, "gateway", inventory.SrcService)
	assert.Equal(t, int64(1), inventory.CallCount)
	assert.Equal(t, int64(1), inventory.ErrorCount)
}

func Test_ServiceCallEdges_MultipleCallsBetweenSamePairAreCountedTogether(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Millisecond)
	spanList := []*spans_core.Span{
		{
			TraceID: "trace-a", SpanID: "s1", Name: "GET /checkout", ServiceName: "gateway",
			StartTime: base, EndTime: base.Add(100 * time.Millisecond), StatusCode: spans_core.StatusCodeOK,
		},
		{
			TraceID: "trace-a", SpanID: "s2", ParentSpanID: "s1", Name: "POST /charge",
			ServiceName: "payments", StartTime: base.Add(10 * time.Millisecond),
			EndTime: base.Add(20 * time.Millisecond), StatusCode: spans_core.StatusCodeOK,
		},
		{
			TraceID: "trace-a", SpanID: "s3", ParentSpanID: "s1", Name: "POST /refund",
			ServiceName: "payments", StartTime: base.Add(30 * time.Millisecond),
			EndTime: base.Add(40 * time.Millisecond), StatusCode: spans_core.StatusCodeError,
		},
	}
	projectID, querier := seedProject(t, spanList)

	edges, err := querier.ServiceCallEdges(projectID, timeRange(base.Add(-time.Minute), base.Add(time.Hour)))
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, "gateway", edges[0].SrcService)
	assert.Equal(t, "payments", edges[0].DstService)
	assert.Equal(t, int64(2), edges[0].CallCount)
	assert.Equal(t, int64(1), edges[0].ErrorCount)
	// Both child spans last exactly 10ms, so the average must be exact.
	assert.InDelta(t, 10.0, edges[0].AvgDurationMs, 0.001)
}

func Test_ServiceCallEdges_RootSpanOnly_ReturnsNoEdges(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Millisecond)
	spanList := []*spans_core.Span{
		{
			TraceID: "trace-a", SpanID: "s1", Name: "GET /health", ServiceName: "gateway",
			StartTime: base, EndTime: base.Add(5 * time.Millisecond), StatusCode: spans_core.StatusCodeOK,
		},
	}
	projectID, querier := seedProject(t, spanList)

	edges, err := querier.ServiceCallEdges(projectID, timeRange(base.Add(-time.Minute), base.Add(time.Hour)))
	require.NoError(t, err)
	assert.Empty(t, edges)
}

func Test_ServiceCallEdges_NoSegmentsForProject_ReturnsEmpty(t *testing.T) {
	base := time.Now().UTC()
	_, querier := seedProject(t, oneTrace("trace-a", base))

	edges, err := querier.ServiceCallEdges(uuid.New(), timeRange(base.Add(-time.Minute), base.Add(time.Hour)))
	require.NoError(t, err)
	assert.Empty(t, edges)
}
