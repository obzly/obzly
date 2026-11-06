package logs_querier_test

import (
	"context"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func aggregationRequest(query *logs_core.QueryNode, from, to time.Time) *logs_core.AggregationRequestDTO {
	return &logs_core.AggregationRequestDTO{
		Query:     query,
		TimeRange: &logs_core.TimeRangeDTO{From: &from, To: &to},
	}
}

func Test_ExecuteAggregation_NoGroupingOrBucketing_ReturnsSingleTotal(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "a"},
		{Timestamp: now, Level: logs_core.LogLevelError, Message: "b"},
		{Timestamp: now, Level: logs_core.LogLevelError, Message: "c"},
	})

	request := aggregationRequest(nil, now.Add(-time.Hour), now.Add(time.Hour))
	response, err := querier.ExecuteAggregation(context.Background(), projectID, request)
	require.NoError(t, err)

	require.Len(t, response.Buckets, 1)
	assert.Nil(t, response.Buckets[0].Timestamp)
	assert.Empty(t, response.Buckets[0].GroupKey)
	assert.Equal(t, int64(3), response.Buckets[0].Count)
}

func Test_ExecuteAggregation_GroupedByLevel_ReturnsTopNBreakdown(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "a"},
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "b"},
		{Timestamp: now, Level: logs_core.LogLevelError, Message: "c"},
	})

	request := aggregationRequest(nil, now.Add(-time.Hour), now.Add(time.Hour))
	request.GroupField = "level"
	response, err := querier.ExecuteAggregation(context.Background(), projectID, request)
	require.NoError(t, err)

	require.Len(t, response.Buckets, 2)
	// Highest count first.
	assert.Equal(t, "info", response.Buckets[0].GroupKey)
	assert.Equal(t, int64(2), response.Buckets[0].Count)
	assert.Equal(t, "error", response.Buckets[1].GroupKey)
	assert.Equal(t, int64(1), response.Buckets[1].Count)
	for _, bucket := range response.Buckets {
		assert.Nil(t, bucket.Timestamp)
	}
}

func Test_ExecuteAggregation_GroupedByCustomField_ExtractsFromFieldsJSON(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "a", Fields: map[string]any{"env": "prod"}},
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "b", Fields: map[string]any{"env": "prod"}},
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "c", Fields: map[string]any{"env": "staging"}},
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "d"}, // no "env" field at all
	})

	request := aggregationRequest(nil, now.Add(-time.Hour), now.Add(time.Hour))
	request.GroupField = "env"
	response, err := querier.ExecuteAggregation(context.Background(), projectID, request)
	require.NoError(t, err)

	counts := map[string]int64{}
	for _, bucket := range response.Buckets {
		counts[bucket.GroupKey] = bucket.Count
	}
	assert.Equal(t, int64(2), counts["prod"])
	assert.Equal(t, int64(1), counts["staging"])
	// The row missing "env" entirely groups under "" rather than breaking the scan.
	assert.Equal(t, int64(1), counts[""])
}

func Test_ExecuteAggregation_Bucketed_GroupsIntoTimeWindows(t *testing.T) {
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: base, Level: logs_core.LogLevelInfo, Message: "a"},
		{Timestamp: base.Add(30 * time.Second), Level: logs_core.LogLevelInfo, Message: "b"},
		{Timestamp: base.Add(90 * time.Second), Level: logs_core.LogLevelInfo, Message: "c"},
	})

	request := aggregationRequest(nil, base.Add(-time.Minute), base.Add(time.Hour))
	request.BucketMs = 60_000 // 1 minute buckets
	response, err := querier.ExecuteAggregation(context.Background(), projectID, request)
	require.NoError(t, err)

	require.Len(t, response.Buckets, 2)
	require.NotNil(t, response.Buckets[0].Timestamp)
	assert.True(t, response.Buckets[0].Timestamp.Equal(base))
	assert.Equal(t, int64(2), response.Buckets[0].Count)

	require.NotNil(t, response.Buckets[1].Timestamp)
	assert.True(t, response.Buckets[1].Timestamp.Equal(base.Add(time.Minute)))
	assert.Equal(t, int64(1), response.Buckets[1].Count)

	for _, bucket := range response.Buckets {
		assert.Empty(t, bucket.GroupKey)
	}
}

func Test_ExecuteAggregation_BucketedAndGrouped_ProducesStackedSeries(t *testing.T) {
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: base, Level: logs_core.LogLevelInfo, Message: "a"},
		{Timestamp: base, Level: logs_core.LogLevelError, Message: "b"},
		{Timestamp: base.Add(time.Minute), Level: logs_core.LogLevelInfo, Message: "c"},
	})

	request := aggregationRequest(nil, base.Add(-time.Minute), base.Add(time.Hour))
	request.BucketMs = 60_000
	request.GroupField = "level"
	response, err := querier.ExecuteAggregation(context.Background(), projectID, request)
	require.NoError(t, err)

	require.Len(t, response.Buckets, 3)
	for _, bucket := range response.Buckets {
		require.NotNil(t, bucket.Timestamp)
		require.NotEmpty(t, bucket.GroupKey)
	}
}

func Test_ExecuteAggregation_WithQueryFilter_OnlyCountsMatchingLogs(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "keep me"},
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "drop me"},
	})

	request := aggregationRequest(
		condition("message", logs_core.ConditionOperatorContains, "keep"),
		now.Add(-time.Hour),
		now.Add(time.Hour),
	)
	response, err := querier.ExecuteAggregation(context.Background(), projectID, request)
	require.NoError(t, err)

	require.Len(t, response.Buckets, 1)
	assert.Equal(t, int64(1), response.Buckets[0].Count)
}

func Test_ExecuteAggregation_WithLimit_CapsTopNResults(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "a", Fields: map[string]any{"k": "1"}},
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "b", Fields: map[string]any{"k": "2"}},
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "c", Fields: map[string]any{"k": "3"}},
	})

	request := aggregationRequest(nil, now.Add(-time.Hour), now.Add(time.Hour))
	request.GroupField = "k"
	request.Limit = 2
	response, err := querier.ExecuteAggregation(context.Background(), projectID, request)
	require.NoError(t, err)

	assert.Len(t, response.Buckets, 2)
}

func Test_ExecuteAggregation_NoSegmentsForProject_ReturnsEmptyBuckets(t *testing.T) {
	_, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: time.Now().UTC(), Level: logs_core.LogLevelInfo, Message: "unrelated"},
	})

	emptyProjectID := uuid.New()
	response, err := querier.ExecuteAggregation(context.Background(), emptyProjectID, aggregationRequest(nil, time.Time{}, time.Now()))
	require.NoError(t, err)
	assert.Empty(t, response.Buckets)
}
