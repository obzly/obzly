package spans_querier_test

import (
	"fmt"
	"testing"
	"time"

	spans_core "obzly/internal/features/spans/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func aggregationRequest(from, to time.Time) *spans_core.AggregationRequestDTO {
	return &spans_core.AggregationRequestDTO{TimeRange: timeRange(from, to)}
}

func Test_ExecuteAggregation_Count_NoGrouping_ReturnsTotalSpanCount(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Millisecond)
	projectID, querier := seedProject(t, oneTrace("trace-a", base))

	request := aggregationRequest(base.Add(-time.Hour), base.Add(time.Hour))
	response, err := querier.ExecuteAggregation(projectID, request)
	require.NoError(t, err)

	require.Len(t, response.Buckets, 1)
	assert.Equal(t, float64(3), response.Buckets[0].Value)
}

func Test_ExecuteAggregation_GroupedByService_ReturnsCountPerService(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Millisecond)
	projectID, querier := seedProject(t, oneTrace("trace-a", base))

	request := aggregationRequest(base.Add(-time.Hour), base.Add(time.Hour))
	request.GroupField = spans_core.GroupFieldServiceName
	response, err := querier.ExecuteAggregation(projectID, request)
	require.NoError(t, err)

	counts := map[string]float64{}
	for _, bucket := range response.Buckets {
		counts[bucket.GroupKey] = bucket.Value
	}
	assert.Equal(t, float64(1), counts["gateway"])
	assert.Equal(t, float64(1), counts["payments"])
	assert.Equal(t, float64(1), counts["inventory"])
}

func Test_ExecuteAggregation_ErrorCountMetric_CountsOnlyErrorSpans(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Millisecond)
	projectID, querier := seedProject(t, oneTrace("trace-a", base))

	request := aggregationRequest(base.Add(-time.Hour), base.Add(time.Hour))
	request.Metric = spans_core.AggregationMetricErrorCount
	request.GroupField = spans_core.GroupFieldServiceName
	response, err := querier.ExecuteAggregation(projectID, request)
	require.NoError(t, err)

	counts := map[string]float64{}
	for _, bucket := range response.Buckets {
		counts[bucket.GroupKey] = bucket.Value
	}
	assert.Equal(t, float64(1), counts["inventory"])
	assert.Equal(t, float64(0), counts["gateway"])
}

func Test_ExecuteAggregation_AvgDurationMetric_ComputesAverageLatency(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Millisecond)
	spanList := []*spans_core.Span{
		{TraceID: "t1", SpanID: "s1", ServiceName: "svc", Name: "op",
			StartTime: base, EndTime: base.Add(100 * time.Millisecond), StatusCode: spans_core.StatusCodeOK},
		{TraceID: "t2", SpanID: "s2", ServiceName: "svc", Name: "op",
			StartTime: base, EndTime: base.Add(200 * time.Millisecond), StatusCode: spans_core.StatusCodeOK},
	}
	projectID, querier := seedProject(t, spanList)

	request := aggregationRequest(base.Add(-time.Hour), base.Add(time.Hour))
	request.Metric = spans_core.AggregationMetricAvgDuration
	response, err := querier.ExecuteAggregation(projectID, request)
	require.NoError(t, err)

	require.Len(t, response.Buckets, 1)
	assert.InDelta(t, 150, response.Buckets[0].Value, 1)
}

func Test_ExecuteAggregation_P95DurationMetric_ComputesPercentile(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Millisecond)
	var spanList []*spans_core.Span
	for i := 1; i <= 100; i++ {
		spanList = append(spanList, &spans_core.Span{
			TraceID: "t", SpanID: fmt.Sprintf("span-%d", i), ServiceName: "svc", Name: "op",
			StartTime: base, EndTime: base.Add(time.Duration(i) * time.Millisecond),
			StatusCode: spans_core.StatusCodeOK,
		})
	}
	projectID, querier := seedProject(t, spanList)

	request := aggregationRequest(base.Add(-time.Hour), base.Add(time.Hour))
	request.Metric = spans_core.AggregationMetricP95Duration
	response, err := querier.ExecuteAggregation(projectID, request)
	require.NoError(t, err)

	require.Len(t, response.Buckets, 1)
	assert.InDelta(t, 95, response.Buckets[0].Value, 2)
}

func Test_ExecuteAggregation_RootSpansOnly_ExcludesChildSpans(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Millisecond)
	projectID, querier := seedProject(t, oneTrace("trace-a", base)) // 1 root + 2 children

	request := aggregationRequest(base.Add(-time.Hour), base.Add(time.Hour))
	request.RootSpansOnly = true
	response, err := querier.ExecuteAggregation(projectID, request)
	require.NoError(t, err)

	require.Len(t, response.Buckets, 1)
	assert.Equal(t, float64(1), response.Buckets[0].Value)
}

func Test_ExecuteAggregation_Bucketed_GroupsIntoTimeWindows(t *testing.T) {
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	spanList := []*spans_core.Span{
		{TraceID: "t1", SpanID: "s1", ServiceName: "svc", Name: "op",
			StartTime: base, EndTime: base.Add(time.Millisecond), StatusCode: spans_core.StatusCodeOK},
		{TraceID: "t2", SpanID: "s2", ServiceName: "svc", Name: "op",
			StartTime: base.Add(
				90 * time.Second,
			), EndTime: base.Add(90*time.Second + time.Millisecond), StatusCode: spans_core.StatusCodeOK},
	}
	projectID, querier := seedProject(t, spanList)

	request := aggregationRequest(base.Add(-time.Minute), base.Add(time.Hour))
	request.BucketMs = 60_000
	response, err := querier.ExecuteAggregation(projectID, request)
	require.NoError(t, err)

	require.Len(t, response.Buckets, 2)
	require.NotNil(t, response.Buckets[0].Timestamp)
	assert.True(t, response.Buckets[0].Timestamp.Equal(base))
}

func Test_ExecuteAggregation_InvalidGroupField_ReturnsError(t *testing.T) {
	base := time.Now().UTC()
	projectID, querier := seedProject(t, oneTrace("trace-a", base))

	request := aggregationRequest(base.Add(-time.Hour), base.Add(time.Hour))
	request.GroupField = "attributes.something; DROP TABLE spans"
	_, err := querier.ExecuteAggregation(projectID, request)
	assert.Error(t, err)
}
