package logs_querier_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_ExecuteAggregation_AvgMeasureByService_AveragesTheFieldPerGroup(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "a", Fields: map[string]any{"service_name": "checkout", "duration_ms": 100}},
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "b", Fields: map[string]any{"service_name": "checkout", "duration_ms": 300}},
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "c", Fields: map[string]any{"service_name": "checkout"}}, // no number: left out
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "d", Fields: map[string]any{"service_name": "search", "duration_ms": "50"}},
	})

	request := aggregationRequest(nil, now.Add(-time.Hour), now.Add(time.Hour))
	request.GroupField = "service_name"
	request.Measure = logs_core.AggregationMeasureAvg
	request.MeasureField = "duration_ms"
	response, err := querier.ExecuteAggregation(context.Background(), projectID, request)
	require.NoError(t, err)

	require.Len(t, response.Buckets, 2)
	assert.Equal(t, "checkout", response.Buckets[0].GroupKey, "largest value first")
	require.NotNil(t, response.Buckets[0].Value)
	assert.InDelta(t, 200, *response.Buckets[0].Value, 0.001)
	assert.Equal(t, "search", response.Buckets[1].GroupKey)
	assert.InDelta(t, 50, *response.Buckets[1].Value, 0.001)
}

func Test_ExecuteAggregation_DistinctAndP95Measures_ComputeOverTheRange(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	var items []*logs_core.LogItem
	for i := 1; i <= 100; i++ {
		traceID := "t1"
		if i%2 == 0 {
			traceID = "t2"
		}
		items = append(items, &logs_core.LogItem{
			Timestamp: now, Level: logs_core.LogLevelInfo, Message: "m", TraceID: traceID,
			Fields: map[string]any{"duration_ms": i},
		})
	}
	projectID, querier := seedProject(t, items)

	distinct := aggregationRequest(nil, now.Add(-time.Hour), now.Add(time.Hour))
	distinct.Measure = logs_core.AggregationMeasureDistinct
	distinct.MeasureField = "trace_id"
	response, err := querier.ExecuteAggregation(context.Background(), projectID, distinct)
	require.NoError(t, err)
	require.Len(t, response.Buckets, 1)
	assert.Equal(t, int64(2), response.Buckets[0].Count)

	p95 := aggregationRequest(nil, now.Add(-time.Hour), now.Add(time.Hour))
	p95.Measure = logs_core.AggregationMeasureP95
	p95.MeasureField = "duration_ms"
	response, err = querier.ExecuteAggregation(context.Background(), projectID, p95)
	require.NoError(t, err)
	require.Len(t, response.Buckets, 1)
	require.NotNil(t, response.Buckets[0].Value)
	assert.InDelta(t, 95.05, *response.Buckets[0].Value, 0.01)
}

func Test_ExecuteAggregation_TopGroupsOnATimeSeries_KeepsOnlyTheLargestGroups(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Minute)
	var items []*logs_core.LogItem
	add := func(service string, n int) {
		for i := 0; i < n; i++ {
			items = append(items, &logs_core.LogItem{
				Timestamp: now.Add(-time.Duration(i%3) * time.Minute), Level: logs_core.LogLevelInfo,
				Message: "m", Fields: map[string]any{"service_name": service},
			})
		}
	}
	add("checkout", 9)
	add("search", 6)
	add("mailer", 1)
	projectID, querier := seedProject(t, items)

	request := aggregationRequest(nil, now.Add(-time.Hour), now.Add(time.Minute))
	request.GroupField = "service_name"
	request.BucketMs = time.Minute.Milliseconds()
	request.TopGroups = 2
	response, err := querier.ExecuteAggregation(context.Background(), projectID, request)
	require.NoError(t, err)

	totals := map[string]int64{}
	for _, bucket := range response.Buckets {
		require.NotNil(t, bucket.Timestamp)
		totals[bucket.GroupKey] += bucket.Count
	}
	assert.Equal(t, map[string]int64{"checkout": 9, "search": 6}, totals)
}

func Test_ExecuteAggregation_LogBands_GroupsANumberIntoOneTwoFiveBands(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	var items []*logs_core.LogItem
	for _, v := range []any{3, 7, 15, 99, 100, 640, 0, "n/a"} {
		items = append(items, &logs_core.LogItem{
			Timestamp: now, Level: logs_core.LogLevelInfo, Message: "m", Fields: map[string]any{"bytes": v},
		})
	}
	items = append(items, &logs_core.LogItem{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "none"})
	projectID, querier := seedProject(t, items)

	request := aggregationRequest(nil, now.Add(-time.Hour), now.Add(time.Hour))
	request.GroupField = "bytes"
	request.GroupBands = logs_core.GroupBandsLog
	response, err := querier.ExecuteAggregation(context.Background(), projectID, request)
	require.NoError(t, err)

	counts := map[float64]int64{}
	for _, bucket := range response.Buckets {
		counts[parseBand(t, bucket.GroupKey)] += bucket.Count
	}
	// 3 -> 2, 7 -> 5, 15 -> 10, 99 -> 50, 100 -> 100, 640 -> 500, 0 -> 0;
	// "n/a" and the line without the field have no number.
	assert.Equal(t, map[float64]int64{0: 1, 2: 1, 5: 1, 10: 1, 50: 1, 100: 1, 500: 1}, counts)
}

func Test_ExecuteAggregation_GroupedCountAcrossHours_MatchesEveryLine(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Hour).Add(30 * time.Minute)
	var items []*logs_core.LogItem
	for i := 0; i < 6; i++ {
		level := logs_core.LogLevelInfo
		if i%3 == 0 {
			level = logs_core.LogLevelError
		}
		items = append(items, &logs_core.LogItem{
			Timestamp: now.Add(-time.Duration(i) * 50 * time.Minute), Level: level, Message: "m",
		})
	}
	projectID, querier := seedProject(t, items)

	request := aggregationRequest(nil, now.Add(-6*time.Hour), now.Add(time.Minute))
	request.GroupField = "level"
	response, err := querier.ExecuteAggregation(context.Background(), projectID, request)
	require.NoError(t, err)

	require.Len(t, response.Buckets, 2)
	assert.Equal(t, "info", response.Buckets[0].GroupKey)
	assert.Equal(t, int64(4), response.Buckets[0].Count)
	assert.Nil(t, response.Buckets[0].Timestamp)
	assert.Equal(t, "error", response.Buckets[1].GroupKey)
	assert.Equal(t, int64(2), response.Buckets[1].Count)
}

func Test_ExecuteAggregation_InvalidOptions_AreRefused(t *testing.T) {
	now := time.Now().UTC()
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "a"},
	})
	cases := map[string]func(*logs_core.AggregationRequestDTO){
		"unknown measure":       func(r *logs_core.AggregationRequestDTO) { r.Measure = "median" },
		"measure without field": func(r *logs_core.AggregationRequestDTO) { r.Measure = logs_core.AggregationMeasureAvg },
		"number of a system column": func(r *logs_core.AggregationRequestDTO) {
			r.Measure, r.MeasureField = logs_core.AggregationMeasureSum, "timestamp"
		},
		"top groups without a group": func(r *logs_core.AggregationRequestDTO) { r.TopGroups = 5 },
		"too many top groups": func(r *logs_core.AggregationRequestDTO) {
			r.GroupField, r.TopGroups = "level", logs_core.MaxTopGroups+1
		},
		"bands without a field": func(r *logs_core.AggregationRequestDTO) { r.GroupBands = logs_core.GroupBandsLog },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			request := aggregationRequest(nil, now.Add(-time.Hour), now.Add(time.Hour))
			mutate(request)
			_, err := querier.ExecuteAggregation(context.Background(), projectID, request)
			require.Error(t, err)
			assert.True(t, errors.Is(err, logs_core.ErrInvalidAggregation))
		})
	}
}

func parseBand(t *testing.T, key string) float64 {
	t.Helper()
	value, err := strconv.ParseFloat(key, 64)
	require.NoError(t, err, "band key %q", key)
	return value
}
