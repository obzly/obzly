package logs_querier_test

import (
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

func Test_ExecuteSQLQuery_ExternalAccessDisabled_BlocksFileReadEvenIfPastBlocklist(t *testing.T) {
	// Defense in depth: a file-reading table function not in the keyword
	// blocklist still fails, as the sandbox disables external access.
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: time.Now().UTC(), Level: logs_core.LogLevelInfo, Message: "x"},
	})

	_, err := querier.ExecuteSQLQuery(projectID, &logs_core.ExecuteSQLQueryRequestDTO{
		SQL:       "SELECT * FROM read_csv_auto('/etc/passwd')",
		TimeRange: timeRangeCoveringLast24h(),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Permission Error")
}

func Test_ExecuteSQLQuery_SandboxConfiguration_IsLocked(t *testing.T) {
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: time.Now().UTC(), Level: logs_core.LogLevelInfo, Message: "x"},
	})

	response, err := querier.ExecuteSQLQuery(projectID, &logs_core.ExecuteSQLQueryRequestDTO{
		SQL:       "SELECT current_setting('lock_configuration') AS locked",
		TimeRange: timeRangeCoveringLast24h(),
	})
	require.NoError(t, err)
	require.Len(t, response.Rows, 1)
	assert.Equal(t, true, response.Rows[0][0])
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
