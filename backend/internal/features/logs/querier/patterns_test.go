package logs_querier_test

import (
	"context"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_ExecutePatterns_GroupsByTemplate_AndHashFiltersBack(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: `10.0.0.1 "GET /healthz HTTP/1.1" 200`},
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: `10.0.0.2 "GET /healthz HTTP/1.1" 200`},
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: `10.0.0.3 "GET /healthz HTTP/1.1" 503`},
		{Timestamp: now, Level: logs_core.LogLevelError, Message: "Converting currency"},
	})
	from, to := now.Add(-time.Hour), now.Add(time.Hour)

	response, err := querier.ExecutePatterns(projectID, &logs_core.PatternsRequestDTO{
		TimeRange: &logs_core.TimeRangeDTO{From: &from, To: &to},
	})
	require.NoError(t, err)
	require.Len(t, response.Patterns, 2)

	top := response.Patterns[0]
	assert.Equal(t, `<ip> "GET /healthz HTTP/<num>" <num>`, top.Pattern)
	assert.Equal(t, int64(3), top.Count)
	assert.Contains(t, top.Sample, "GET /healthz")
	assert.Equal(t, logs_core.PatternHash(top.Sample), top.Hash)
	assert.Len(t, top.Hash, logs_core.PatternHashLength)
	assert.Equal(t, int64(1), response.Patterns[1].Count)

	// The returned hash filters stored search back to exactly that pattern.
	request := queryRequest(
		condition(logs_core.PatternHashField, logs_core.ConditionOperatorEquals, top.Hash), from, to,
	)
	request.Limit = 100
	logs, err := querier.ExecuteQuery(projectID, request)
	require.NoError(t, err)
	assert.Len(t, logs.Logs, 3)

	// ...and scopes the patterns query itself, like any other filter.
	scoped, err := querier.ExecutePatterns(projectID, &logs_core.PatternsRequestDTO{
		Query:     condition("level", logs_core.ConditionOperatorEquals, "ERROR"),
		TimeRange: &logs_core.TimeRangeDTO{From: &from, To: &to},
	})
	require.NoError(t, err)
	require.Len(t, scoped.Patterns, 1)
	assert.Equal(t, "Converting currency", scoped.Patterns[0].Pattern)

	// Grouping by the virtual field works through the generic aggregation.
	aggregation := aggregationRequest(nil, from, to)
	aggregation.GroupField = logs_core.PatternHashField
	grouped, err := querier.ExecuteAggregation(context.Background(), projectID, aggregation)
	require.NoError(t, err)
	require.Len(t, grouped.Buckets, 2)
	assert.Equal(t, top.Hash, grouped.Buckets[0].GroupKey)
	assert.Equal(t, int64(3), grouped.Buckets[0].Count)
}

// Every search result carries its pattern.hash, and filtering on it finds
// that row - what the results table's hover "show only / hide logs like
// this" buttons rely on.
func Test_ExecuteQuery_RowsCarryTheirPatternHash(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	message := `10.0.0.7 "GET /healthz HTTP/1.1" 200`
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: message},
		{Timestamp: now, Level: logs_core.LogLevelError, Message: "Converting currency"},
	})
	from, to := now.Add(-time.Hour), now.Add(time.Hour)

	request := queryRequest(nil, from, to)
	request.Limit = 10
	all, err := querier.ExecuteQuery(projectID, request)
	require.NoError(t, err)
	require.Len(t, all.Logs, 2)
	for _, row := range all.Logs {
		assert.Equal(t, logs_core.PatternHash(row.Message), row.PatternHash)
	}

	excludeHealthz := queryRequest(logical(logs_core.LogicalOperatorNot,
		condition(logs_core.PatternHashField, logs_core.ConditionOperatorEquals, logs_core.PatternHash(message)),
	), from, to)
	excludeHealthz.Limit = 10
	rest, err := querier.ExecuteQuery(projectID, excludeHealthz)
	require.NoError(t, err)
	require.Len(t, rest.Logs, 1)
	assert.Equal(t, "Converting currency", rest.Logs[0].Message)
}
