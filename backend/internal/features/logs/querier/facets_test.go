package logs_querier_test

import (
	"context"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedServices(
	t *testing.T, now time.Time,
) (*logs_core.FieldValuesRequestDTO, func(*logs_core.FieldValuesRequestDTO) *logs_core.FieldValuesResponseDTO) {
	t.Helper()
	var items []*logs_core.LogItem
	add := func(at time.Time, service string, n int) {
		for range n {
			items = append(items, &logs_core.LogItem{Timestamp: at, Level: logs_core.LogLevelInfo,
				Message: "request from " + service, Fields: map[string]any{"service": service}})
		}
	}
	add(now, "api", 3)
	add(now.Add(-30*time.Minute), "web", 2)
	add(now.Add(-3*time.Hour), "batch", 5)
	items = append(items, &logs_core.LogItem{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "no service"})
	projectID, querier := seedProject(t, items)

	from, to := now.Add(-6*time.Hour), now
	request := &logs_core.FieldValuesRequestDTO{
		Field: "service", TimeRange: &logs_core.TimeRangeDTO{From: &from, To: &to},
	}
	return request, func(request *logs_core.FieldValuesRequestDTO) *logs_core.FieldValuesResponseDTO {
		response, err := querier.ExecuteFieldValues(context.Background(), projectID, request)
		require.NoError(t, err)
		return response
	}
}

func Test_ExecuteFieldValues_WholeRange_CountsEveryValueMostFrequentFirst(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	request, run := seedServices(t, now)

	response := run(request)

	assert.Equal(t, []logs_core.FieldValueDTO{
		{Value: "batch", Count: 5}, {Value: "api", Count: 3}, {Value: "web", Count: 2},
	}, response.Values, "a log without the field is not a value")
	assert.Nil(t, response.SampledFrom)
	assert.Equal(t, int64(11), response.MatchedRows)

	request.Limit = 1
	assert.Equal(t, []logs_core.FieldValueDTO{{Value: "batch", Count: 5}}, run(request).Values)
}

// A sampled facet reads the newest windows only: the newest 5 minutes hold
// 4 matching logs, so a sample of 5 reads on to the 30-minute-old web logs
// and stops there, before the 3-hour-old batch logs.
func Test_ExecuteFieldValues_Sampled_StopsAtTheNewestWindowsWithEnoughRows(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	request, run := seedServices(t, now)
	request.SampleRows = 5

	response := run(request)

	assert.Equal(t, []logs_core.FieldValueDTO{{Value: "api", Count: 3}, {Value: "web", Count: 2}}, response.Values)
	require.NotNil(t, response.SampledFrom)
	assert.True(t, response.SampledFrom.Before(now.Add(-30*time.Minute)), "covers the web logs")
	assert.True(t, response.SampledFrom.After(now.Add(-3*time.Hour)), "stops before the batch logs")
	assert.Equal(t, int64(6), response.MatchedRows)

	// A sample larger than the range has is the whole range.
	request.SampleRows = 1000
	whole := run(request)
	assert.Nil(t, whole.SampledFrom)
	assert.Len(t, whole.Values, 3)
}

func Test_ExecuteFieldValues_QueryScopesTheValues(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	request, run := seedServices(t, now)
	request.Query = condition("message", logs_core.ConditionOperatorContains, "web")

	assert.Equal(t, []logs_core.FieldValueDTO{{Value: "web", Count: 2}}, run(request).Values)
}

func Test_ExecutePatterns_Sampled_ReportsWhereTheSampleStarts(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "user 1 logged in"},
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "user 2 logged in"},
		{Timestamp: now.Add(-3 * time.Hour), Level: logs_core.LogLevelInfo, Message: "cache flushed"},
	})
	from, to := now.Add(-6*time.Hour), now

	response, err := querier.ExecutePatterns(projectID, &logs_core.PatternsRequestDTO{
		TimeRange: &logs_core.TimeRangeDTO{From: &from, To: &to}, SampleRows: 2,
	})
	require.NoError(t, err)
	require.Len(t, response.Patterns, 1)
	assert.Equal(t, "user <num> logged in", response.Patterns[0].Pattern)
	assert.Equal(t, int64(2), response.Patterns[0].Count)
	assert.Contains(t, response.Patterns[0].Sample, "logged in")
	assert.NotNil(t, response.SampledFrom)
	assert.Equal(t, int64(2), response.MatchedRows)
}
