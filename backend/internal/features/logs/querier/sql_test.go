package logs_querier_test

import (
	"context"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"
	"obzly/internal/query"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func timeRangeCoveringLast24h() *logs_core.TimeRangeDTO {
	from := time.Now().UTC().Add(-24 * time.Hour)
	to := time.Now().UTC().Add(time.Hour)
	return &logs_core.TimeRangeDTO{From: &from, To: &to}
}

func Test_ExecuteSQLQuery_PlainSelect_ReturnsRows(t *testing.T) {
	now := time.Now().UTC()
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "user logged in"},
		{Timestamp: now.Add(time.Second), Level: logs_core.LogLevelError, Message: "connection refused"},
	})

	response, err := querier.ExecuteSQLQuery(projectID, &logs_core.ExecuteSQLQueryRequestDTO{
		SQL:       "SELECT level, message FROM logs ORDER BY message",
		TimeRange: timeRangeCoveringLast24h(),
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"level", "message"}, response.Columns)
	require.Len(t, response.Rows, 2)
	assert.Equal(t, []any{"error", "connection refused"}, response.Rows[0])
	assert.Equal(t, []any{"info", "user logged in"}, response.Rows[1])
	assert.Equal(t, 2, response.RowCount)
	assert.False(t, response.Truncated)
}

func Test_ExecuteSQLQuery_Aggregation_ReturnsGroupedResult(t *testing.T) {
	now := time.Now().UTC()
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "a"},
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "b"},
		{Timestamp: now, Level: logs_core.LogLevelError, Message: "c"},
	})

	response, err := querier.ExecuteSQLQuery(projectID, &logs_core.ExecuteSQLQueryRequestDTO{
		SQL:       "SELECT level, count(*) AS cnt FROM logs GROUP BY level ORDER BY level",
		TimeRange: timeRangeCoveringLast24h(),
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"level", "cnt"}, response.Columns)
	require.Len(t, response.Rows, 2)
	assert.Equal(t, "error", response.Rows[0][0])
	assert.EqualValues(t, 1, response.Rows[0][1])
	assert.Equal(t, "info", response.Rows[1][0])
	assert.EqualValues(t, 2, response.Rows[1][1])
}

func Test_ExecuteSQLQuery_QueriesIntoJSONFields(t *testing.T) {
	now := time.Now().UTC()
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{
			Timestamp: now, Level: logs_core.LogLevelInfo, Message: "request handled",
			Fields: map[string]any{"status_code": float64(200), "path": "/api/v1/logs"},
		},
	})

	response, err := querier.ExecuteSQLQuery(projectID, &logs_core.ExecuteSQLQueryRequestDTO{
		SQL:       `SELECT json_extract_string(fields, '$.path') AS path FROM logs`,
		TimeRange: timeRangeCoveringLast24h(),
	})
	require.NoError(t, err)
	require.Len(t, response.Rows, 1)
	assert.Equal(t, "/api/v1/logs", response.Rows[0][0])
}

func Test_ExecuteSQLQuery_TimeRangeFiltersOutOfRangeRows(t *testing.T) {
	now := time.Now().UTC()
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: now.Add(-48 * time.Hour), Level: logs_core.LogLevelInfo, Message: "old"},
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "recent"},
	})

	response, err := querier.ExecuteSQLQuery(projectID, &logs_core.ExecuteSQLQueryRequestDTO{
		SQL:       "SELECT message FROM logs",
		TimeRange: timeRangeCoveringLast24h(),
	})
	require.NoError(t, err)
	require.Len(t, response.Rows, 1)
	assert.Equal(t, "recent", response.Rows[0][0])
}

func Test_ExecuteSQLQuery_NoSegments_ReturnsEmptyNotError(t *testing.T) {
	projectID, querier := seedProject(t, nil)

	response, err := querier.ExecuteSQLQuery(projectID, &logs_core.ExecuteSQLQueryRequestDTO{
		SQL:       "SELECT * FROM logs",
		TimeRange: timeRangeCoveringLast24h(),
	})
	require.NoError(t, err)
	assert.Empty(t, response.Rows)
	assert.Equal(
		t,
		[]string{"timestamp", "level", "message", "client_ip", "trace_id", "span_id", "fields"},
		response.Columns,
	)
}

func Test_ExecuteSQLQuery_InvalidSQL_ReturnsError(t *testing.T) {
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: time.Now().UTC(), Level: logs_core.LogLevelInfo, Message: "x"},
	})

	for _, badSQL := range []string{
		"DROP TABLE logs",
		"SELECT 1; DROP TABLE logs",
		"SELECT * FROM read_parquet('/etc/passwd')",
	} {
		_, err := querier.ExecuteSQLQuery(projectID, &logs_core.ExecuteSQLQueryRequestDTO{
			SQL:       badSQL,
			TimeRange: timeRangeCoveringLast24h(),
		})
		assert.Error(t, err, "expected %q to be rejected", badSQL)
	}
}

func Test_ExecuteSQLQuery_ServerSettingOrFileFunction_IsRefused(t *testing.T) {
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: time.Now().UTC(), Level: logs_core.LogLevelInfo, Message: "x"},
	})

	for _, statement := range []string{
		"SELECT * FROM read_csv_auto('/etc/passwd')",
		"SELECT current_setting('lock_configuration') AS locked",
		"SELECT * FROM duckdb_settings()",
	} {
		_, err := querier.ExecuteSQLQuery(projectID, &logs_core.ExecuteSQLQueryRequestDTO{
			SQL:       statement,
			TimeRange: timeRangeCoveringLast24h(),
		})
		assert.ErrorContains(t, err, "disallowed function", statement)
	}
}

func Test_ExecuteSQLQuery_RangeOverScanBudget_RefusedBeforeReading(t *testing.T) {
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: time.Now().UTC(), Level: logs_core.LogLevelInfo, Message: "x"},
	})

	_, err := querier.ExecuteSQLQuery(projectID, &logs_core.ExecuteSQLQueryRequestDTO{
		SQL:        "SELECT count(*) FROM logs",
		TimeRange:  timeRangeCoveringLast24h(),
		ScanBudget: 1,
	})
	require.ErrorIs(t, err, query.ErrScanBudget)
}

func Test_ExecuteSQLQueryContext_CancelledContext_StopsTheQuery(t *testing.T) {
	now := time.Now().UTC()
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "a"},
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := querier.ExecuteSQLQueryContext(ctx, projectID, &logs_core.ExecuteSQLQueryRequestDTO{
		SQL:       "SELECT count(*) FROM logs",
		TimeRange: timeRangeCoveringLast24h(),
	})

	require.ErrorIs(t, err, context.Canceled)
}

func Test_ExecuteScopedSQL_WithAShapedTable_FiltersOnItsDerivedColumnsAndDropsRaw(t *testing.T) {
	now := time.Now().UTC()
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "a", Fields: map[string]any{"status": "200"}},
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "b", Fields: map[string]any{"status": "503"}},
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "c", Fields: map[string]any{"other": "x"}},
	})

	// The final WHERE reads a column two projections up, as Web analytics' table does.
	table := "SELECT message, status FROM (SELECT message, TRY_CAST(fv[1] AS INTEGER) AS status FROM " +
		"(SELECT message, json_extract_string(fields, ['$.status']) AS fv FROM raw) l1) l2 WHERE status >= ?"
	results, err := querier.ExecuteScopedSQL(projectID, &logs_core.ScopedSQLRequestDTO{
		From: now.Add(-time.Hour), To: now.Add(time.Hour), Table: table, TableArgs: []any{500},
		Statements: []logs_core.ScopedSQLStatement{{SQL: "SELECT message, status FROM logs"}},
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, [][]any{{"b", int32(503)}}, results[0].Rows)

	_, err = querier.ExecuteScopedSQL(projectID, &logs_core.ScopedSQLRequestDTO{
		From: now.Add(-time.Hour), To: now.Add(time.Hour), Table: "SELECT message FROM raw",
		Statements: []logs_core.ScopedSQLStatement{{SQL: "SELECT count(*) FROM raw"}},
	})
	require.Error(t, err, "statements see only the shaped table")
}
