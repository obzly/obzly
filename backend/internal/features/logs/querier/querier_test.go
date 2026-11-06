package logs_querier_test

import (
	"context"
	"os"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"
	logs_ingester "obzly/internal/features/logs/ingester"
	logs_querier "obzly/internal/features/logs/querier"
	logs_uploader "obzly/internal/features/logs/uploader"
	"obzly/internal/storage/segments"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "gocloud.dev/blob/memblob"
)

// seedProject ingests logItems for a fresh project through the real ingester
// (write, catalog, read end to end) and returns the project ID and a querier.
func seedProject(t *testing.T, logItems []*logs_core.LogItem) (uuid.UUID, *logs_querier.LogQuerier) {
	t.Helper()

	ingester, err := logs_ingester.NewLogIngester(t.TempDir(), segments.WithMaxRows(1_000_000))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ingester.Close() })

	projectID := uuid.New()
	for _, item := range logItems {
		item.ProjectID = projectID
		if item.ID == uuid.Nil {
			item.ID = uuid.New()
		}
	}

	require.NoError(t, ingester.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{projectID: logItems}))
	_, err = ingester.FlushAll()
	require.NoError(t, err)

	querier, err := logs_querier.NewLogQuerier()
	require.NoError(t, err)
	t.Cleanup(func() { _ = querier.Close() })

	return projectID, querier
}

func condition(field string, op logs_core.ConditionOperator, value any) *logs_core.QueryNode {
	return &logs_core.QueryNode{
		Type:      logs_core.QueryNodeTypeCondition,
		Condition: &logs_core.ConditionNode{Field: field, Operator: op, Value: value},
	}
}

func logical(op logs_core.LogicalOperator, children ...*logs_core.QueryNode) *logs_core.QueryNode {
	flat := make([]logs_core.QueryNode, len(children))
	for i, c := range children {
		flat[i] = *c
	}
	return &logs_core.QueryNode{
		Type:  logs_core.QueryNodeTypeLogical,
		Logic: &logs_core.LogicalNode{Operator: op, Children: flat},
	}
}

func queryRequest(query *logs_core.QueryNode, from, to time.Time) *logs_core.LogQueryRequestDTO {
	return &logs_core.LogQueryRequestDTO{
		Query:     query,
		TimeRange: &logs_core.TimeRangeDTO{From: &from, To: &to},
	}
}

func messagesOf(logs []logs_core.LogItemDTO) []string {
	messages := make([]string, len(logs))
	for i, l := range logs {
		messages[i] = l.Message
	}
	return messages
}

func Test_ExecuteQuery_NoQuery_ReturnsAllLogsInTimeRange(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "one"},
		{Timestamp: now.Add(time.Second), Level: logs_core.LogLevelInfo, Message: "two"},
	})

	response, err := querier.ExecuteQuery(projectID, queryRequest(nil, now.Add(-time.Minute), now.Add(time.Minute)))

	require.NoError(t, err)
	require.Equal(t, int64(2), response.Total)
	require.Len(t, response.Logs, 2)
}

func Test_ExecuteQuery_NilTimeRange_FindsRecentLogsByCustomFieldCondition(t *testing.T) {
	now := time.Now().UTC()
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "a", Fields: map[string]any{
			"test_session": "abc123", "sequence_num": 1, "service": "api",
		}},
	})

	request := &logs_core.LogQueryRequestDTO{
		Query: condition("test_session", logs_core.ConditionOperatorEquals, "abc123"),
		Limit: 10,
	}
	response, err := querier.ExecuteQuery(projectID, request)

	require.NoError(t, err)
	require.Equal(t, int64(1), response.Total)
}

func Test_ExecuteQuery_TimeRange_ExcludesLogsOutsideWindow(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: now.Add(-time.Hour), Level: logs_core.LogLevelInfo, Message: "old"},
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "recent"},
	})

	response, err := querier.ExecuteQuery(projectID, queryRequest(nil, now.Add(-time.Minute), now.Add(time.Minute)))

	require.NoError(t, err)
	require.Equal(t, int64(1), response.Total)
	require.Equal(t, "recent", response.Logs[0].Message)
}

func Test_ExecuteQuery_EqualsOnSystemField_FiltersByLevel(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: now, Level: logs_core.LogLevelError, Message: "boom"},
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "fine"},
	})

	query := condition("level", logs_core.ConditionOperatorEquals, "ERROR")
	response, err := querier.ExecuteQuery(projectID, queryRequest(query, now.Add(-time.Minute), now.Add(time.Minute)))

	require.NoError(t, err)
	require.Equal(t, int64(1), response.Total)
	require.Equal(t, "boom", response.Logs[0].Message)
}

func Test_ExecuteQuery_ContainsOnMessage_MatchesSubstring(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: now, Level: logs_core.LogLevelError, Message: "connection timeout while calling upstream"},
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "request completed successfully"},
	})

	query := condition("message", logs_core.ConditionOperatorContains, "timeout")
	response, err := querier.ExecuteQuery(projectID, queryRequest(query, now.Add(-time.Minute), now.Add(time.Minute)))

	require.NoError(t, err)
	require.Equal(t, int64(1), response.Total)
	require.Contains(t, response.Logs[0].Message, "timeout")
}

func Test_ExecuteQuery_NotContains_ExcludesMatchingMessage(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: now, Level: logs_core.LogLevelError, Message: "connection timeout"},
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "request completed"},
	})

	query := condition("message", logs_core.ConditionOperatorNotContains, "timeout")
	response, err := querier.ExecuteQuery(projectID, queryRequest(query, now.Add(-time.Minute), now.Add(time.Minute)))

	require.NoError(t, err)
	require.Equal(t, int64(1), response.Total)
	require.Equal(t, "request completed", response.Logs[0].Message)
}

func Test_ExecuteQuery_CustomField_EqualsMatchesAndFieldSurfacedInResponse(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "a", Fields: map[string]any{"env": "production"}},
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "b", Fields: map[string]any{"env": "staging"}},
	})

	query := condition("env", logs_core.ConditionOperatorEquals, "production")
	response, err := querier.ExecuteQuery(projectID, queryRequest(query, now.Add(-time.Minute), now.Add(time.Minute)))

	require.NoError(t, err)
	require.Equal(t, int64(1), response.Total)
	require.Equal(t, "a", response.Logs[0].Message)
	require.Equal(t, "production", response.Logs[0].Fields["env"])
}

func Test_ExecuteQuery_CustomField_ExistsExcludesLogsWithoutField(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "has field", Fields: map[string]any{"user_id": "1"}},
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "no field"},
	})

	query := condition("user_id", logs_core.ConditionOperatorExists, nil)
	response, err := querier.ExecuteQuery(projectID, queryRequest(query, now.Add(-time.Minute), now.Add(time.Minute)))

	require.NoError(t, err)
	require.Equal(t, int64(1), response.Total)
	require.Equal(t, "has field", response.Logs[0].Message)
}

func Test_ExecuteQuery_CustomField_NotExistsIncludesLogsWithoutField(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "has field", Fields: map[string]any{"user_id": "1"}},
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "no field"},
	})

	query := condition("user_id", logs_core.ConditionOperatorNotExists, nil)
	response, err := querier.ExecuteQuery(projectID, queryRequest(query, now.Add(-time.Minute), now.Add(time.Minute)))

	require.NoError(t, err)
	require.Equal(t, int64(1), response.Total)
	require.Equal(t, "no field", response.Logs[0].Message)
}

func Test_ExecuteQuery_CustomField_NotEqualsIncludesLogsWhereFieldIsAbsent(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{
			Timestamp: now,
			Level:     logs_core.LogLevelInfo,
			Message:   "matches other value",
			Fields:    map[string]any{"env": "staging"},
		},
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "field absent"},
		{
			Timestamp: now,
			Level:     logs_core.LogLevelInfo,
			Message:   "equals production",
			Fields:    map[string]any{"env": "production"},
		},
	})

	query := condition("env", logs_core.ConditionOperatorNotEquals, "production")
	response, err := querier.ExecuteQuery(projectID, queryRequest(query, now.Add(-time.Minute), now.Add(time.Minute)))

	require.NoError(t, err)
	require.Equal(t, int64(2), response.Total)
	require.ElementsMatch(t, []string{"matches other value", "field absent"}, messagesOf(response.Logs))
}

func Test_ExecuteQuery_LogicalAnd_RequiresBothConditions(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: now, Level: logs_core.LogLevelError, Message: "a", Fields: map[string]any{"env": "production"}},
		{Timestamp: now, Level: logs_core.LogLevelError, Message: "b", Fields: map[string]any{"env": "staging"}},
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "c", Fields: map[string]any{"env": "production"}},
	})

	query := logical(logs_core.LogicalOperatorAnd,
		condition("level", logs_core.ConditionOperatorEquals, "ERROR"),
		condition("env", logs_core.ConditionOperatorEquals, "production"),
	)
	response, err := querier.ExecuteQuery(projectID, queryRequest(query, now.Add(-time.Minute), now.Add(time.Minute)))

	require.NoError(t, err)
	require.Equal(t, int64(1), response.Total)
	require.Equal(t, "a", response.Logs[0].Message)
}

func Test_ExecuteQuery_LogicalOr_MatchesEitherCondition(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: now, Level: logs_core.LogLevelError, Message: "error one"},
		{Timestamp: now, Level: logs_core.LogLevelFatal, Message: "fatal one"},
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "info one"},
	})

	query := logical(logs_core.LogicalOperatorOr,
		condition("level", logs_core.ConditionOperatorEquals, "ERROR"),
		condition("level", logs_core.ConditionOperatorEquals, "FATAL"),
	)
	response, err := querier.ExecuteQuery(projectID, queryRequest(query, now.Add(-time.Minute), now.Add(time.Minute)))

	require.NoError(t, err)
	require.Equal(t, int64(2), response.Total)
	require.ElementsMatch(t, []string{"error one", "fatal one"}, messagesOf(response.Logs))
}

func Test_ExecuteQuery_LogicalNot_ExcludesMatchingCondition(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: now, Level: logs_core.LogLevelError, Message: "error one"},
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "info one"},
	})

	query := logical(logs_core.LogicalOperatorNot, condition("level", logs_core.ConditionOperatorEquals, "ERROR"))
	response, err := querier.ExecuteQuery(projectID, queryRequest(query, now.Add(-time.Minute), now.Add(time.Minute)))

	require.NoError(t, err)
	require.Equal(t, int64(1), response.Total)
	require.Equal(t, "info one", response.Logs[0].Message)
}

func Test_ExecuteQuery_InOperator_MatchesAnyListedValue(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: now, Level: logs_core.LogLevelError, Message: "a"},
		{Timestamp: now, Level: logs_core.LogLevelWarn, Message: "b"},
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "c"},
	})

	query := condition("level", logs_core.ConditionOperatorIn, []string{"ERROR", "WARN"})
	response, err := querier.ExecuteQuery(projectID, queryRequest(query, now.Add(-time.Minute), now.Add(time.Minute)))

	require.NoError(t, err)
	require.Equal(t, int64(2), response.Total)
	require.ElementsMatch(t, []string{"a", "b"}, messagesOf(response.Logs))
}

func Test_ExecuteQuery_ClientIPEquals_FiltersByClientIP(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "a", ClientIP: "10.0.0.42"},
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "b", ClientIP: "10.0.0.7"},
	})

	query := condition("client_ip", logs_core.ConditionOperatorEquals, "10.0.0.42")
	response, err := querier.ExecuteQuery(projectID, queryRequest(query, now.Add(-time.Minute), now.Add(time.Minute)))

	require.NoError(t, err)
	require.Equal(t, int64(1), response.Total)
	require.Equal(t, "10.0.0.42", response.Logs[0].ClientIP)
}

func Test_ExecuteQuery_TimestampGreaterOrEqual_FiltersByRange(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Millisecond)
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: base, Level: logs_core.LogLevelInfo, Message: "early"},
		{Timestamp: base.Add(time.Hour), Level: logs_core.LogLevelInfo, Message: "late"},
	})

	query := condition(
		"timestamp",
		logs_core.ConditionOperatorGreaterOrEqual,
		base.Add(30*time.Minute).Format(time.RFC3339Nano),
	)
	response, err := querier.ExecuteQuery(projectID, queryRequest(query, base.Add(-time.Minute), base.Add(2*time.Hour)))

	require.NoError(t, err)
	require.Equal(t, int64(1), response.Total)
	require.Equal(t, "late", response.Logs[0].Message)
}

func Test_ExecuteQuery_Pagination_LimitAndOffsetSliceResultsButTotalStaysFull(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Millisecond)
	items := make([]*logs_core.LogItem, 5)
	for i := range items {
		items[i] = &logs_core.LogItem{
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Level:     logs_core.LogLevelInfo,
			Message:   "msg",
		}
	}
	projectID, querier := seedProject(t, items)

	request := queryRequest(nil, base.Add(-time.Minute), base.Add(time.Minute))
	request.Limit = 2
	request.Offset = 1
	request.SortOrder = "asc"

	response, err := querier.ExecuteQuery(projectID, request)

	require.NoError(t, err)
	require.Equal(t, int64(5), response.Total, "total should reflect all matches, not just the returned page")
	require.Len(t, response.Logs, 2)
	// time.Time equality via require.Equal is unreliable across a driver round
	// trip; compare with Equal.
	require.True(t, base.Add(1*time.Second).Equal(response.Logs[0].Timestamp))
	require.True(t, base.Add(2*time.Second).Equal(response.Logs[1].Timestamp))
}

func Test_ExecuteQuery_SortOrderDesc_ReturnsNewestFirst(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Millisecond)
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: base, Level: logs_core.LogLevelInfo, Message: "first"},
		{Timestamp: base.Add(time.Second), Level: logs_core.LogLevelInfo, Message: "second"},
	})

	request := queryRequest(nil, base.Add(-time.Minute), base.Add(time.Minute))
	request.SortOrder = "desc"

	response, err := querier.ExecuteQuery(projectID, request)

	require.NoError(t, err)
	require.Equal(t, "second", response.Logs[0].Message)
	require.Equal(t, "first", response.Logs[1].Message)
}

func Test_ExecuteQuery_ProjectIsolation_DoesNotReturnOtherProjectsLogs(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	projectA, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "from A"},
	})

	// Seed a second, unrelated project through a second ingester instance.
	ingester, err := logs_ingester.NewLogIngester(t.TempDir(), segments.WithMaxRows(1_000_000))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ingester.Close() })
	projectB := uuid.New()
	require.NoError(t, ingester.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{
		projectB: {
			{ID: uuid.New(), ProjectID: projectB, Timestamp: now, Level: logs_core.LogLevelInfo, Message: "from B"},
		},
	}))
	_, err = ingester.FlushAll()
	require.NoError(t, err)

	response, err := querier.ExecuteQuery(projectA, queryRequest(nil, now.Add(-time.Minute), now.Add(time.Minute)))

	require.NoError(t, err)
	require.Equal(t, int64(1), response.Total)
	require.Equal(t, "from A", response.Logs[0].Message)
}

// A catalog row can outlive its Parquet file (crash, manual cleanup,
// corruption); one such row must not fail every query for the project.
func Test_ExecuteQuery_OneSegmentFileMissing_ReturnsOtherSegmentsInsteadOfFailing(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Millisecond)
	projectID := uuid.New()

	// maxRows(1): one segment per item, one to delete and one to keep. Distinct
	// timestamps make min_time-ASC ordering put the deleted one first.
	ingester, err := logs_ingester.NewLogIngester(dataDir, segments.WithMaxRows(1))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ingester.Close() })

	toDelete := &logs_core.LogItem{
		ID:        uuid.New(),
		ProjectID: projectID,
		Timestamp: now,
		Level:     logs_core.LogLevelInfo,
		Message:   "delete my file",
	}
	toKeep := &logs_core.LogItem{
		ID:        uuid.New(),
		ProjectID: projectID,
		Timestamp: now.Add(time.Second),
		Level:     logs_core.LogLevelInfo,
		Message:   "keep me",
	}
	require.NoError(t, ingester.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{projectID: {toDelete}}))
	require.NoError(t, ingester.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{projectID: {toKeep}}))

	repo := &segments.SegmentRepository{}
	records, err := repo.ListByProject(projectID, "logs")
	require.NoError(t, err)
	require.Len(t, records, 2)

	// Simulate a stale catalog row: the file is gone, but nothing told
	// stream_segments.
	require.NoError(t, os.Remove(records[0].Path))

	querier, err := logs_querier.NewLogQuerier()
	require.NoError(t, err)
	t.Cleanup(func() { _ = querier.Close() })

	response, err := querier.ExecuteQuery(projectID, queryRequest(nil, now.Add(-time.Minute), now.Add(2*time.Minute)))

	require.NoError(t, err, "one unreadable segment must not fail the whole query")
	require.Equal(t, int64(1), response.Total)
	require.Equal(t, "keep me", response.Logs[0].Message)
}

// All files missing degrades to an empty response, not an error from a query
// over zero files.
func Test_ExecuteQuery_EverySegmentFileMissing_ReturnsEmptyResponse(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Millisecond)
	projectID := uuid.New()

	ingester, err := logs_ingester.NewLogIngester(dataDir, segments.WithMaxRows(1_000_000))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ingester.Close() })
	item := &logs_core.LogItem{
		ID:        uuid.New(),
		ProjectID: projectID,
		Timestamp: now,
		Level:     logs_core.LogLevelInfo,
		Message:   "orphaned",
	}
	require.NoError(t, ingester.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{projectID: {item}}))
	_, err = ingester.FlushAll()
	require.NoError(t, err)

	repo := &segments.SegmentRepository{}
	records, err := repo.ListByProject(projectID, "logs")
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.NoError(t, os.Remove(records[0].Path))

	querier, err := logs_querier.NewLogQuerier()
	require.NoError(t, err)
	t.Cleanup(func() { _ = querier.Close() })

	response, err := querier.ExecuteQuery(projectID, queryRequest(nil, now.Add(-time.Minute), now.Add(time.Minute)))

	require.NoError(t, err)
	require.Equal(t, int64(0), response.Total)
	require.Empty(t, response.Logs)
}

// An out-of-range time range is clamped to TIMESTAMP_NS's bounds, not a
// driver error (500), so time.Time{} still means "since the beginning".
func Test_ExecuteQuery_TimeRangeOutOfRepresentableRange_ClampedNotErrored(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "within the clamped range"},
	})

	tooFarFuture := time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	tooFarPast := time.Date(1000, 1, 1, 0, 0, 0, 0, time.UTC)

	response, err := querier.ExecuteQuery(projectID, queryRequest(nil, tooFarPast, tooFarFuture))
	require.NoError(t, err)
	require.Equal(t, int64(1), response.Total, "the seeded log should still be found once the range is clamped")
	assert.Equal(t, "within the clamped range", response.Logs[0].Message)
}

func Test_ExecuteQuery_NoSegmentsForProject_ReturnsEmptyResponse(t *testing.T) {
	querier, err := logs_querier.NewLogQuerier()
	require.NoError(t, err)
	t.Cleanup(func() { _ = querier.Close() })

	now := time.Now().UTC()
	response, err := querier.ExecuteQuery(uuid.New(), queryRequest(nil, now.Add(-time.Hour), now))

	require.NoError(t, err)
	require.Equal(t, int64(0), response.Total)
	require.Empty(t, response.Logs)
}

func Test_DiscoverFields_ReturnsCustomFieldNamesSortedAndDeduplicated(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	projectID, querier := seedProject(t, []*logs_core.LogItem{
		{
			Timestamp: now,
			Level:     logs_core.LogLevelInfo,
			Message:   "a",
			Fields:    map[string]any{"user_id": "1", "env": "production"},
		},
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "b", Fields: map[string]any{"env": "staging"}},
	})

	fields, err := querier.DiscoverFields(projectID)

	require.NoError(t, err)
	require.Equal(t, []string{"env", "user_id"}, fields)
}

func Test_DiscoverFields_NoSegments_ReturnsEmptySlice(t *testing.T) {
	querier, err := logs_querier.NewLogQuerier()
	require.NoError(t, err)
	t.Cleanup(func() { _ = querier.Close() })

	fields, err := querier.DiscoverFields(uuid.New())

	require.NoError(t, err)
	require.Empty(t, fields)
}

func Test_ExecuteQuery_SegmentUploadedAndReclaimed_DownloadsFromObjectStore(t *testing.T) {
	test_utils.UseIsolatedSegmentWriter(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	dataDir := t.TempDir()

	ingester, err := logs_ingester.NewLogIngester(dataDir, segments.WithMaxRows(1_000_000))
	require.NoError(t, err)
	projectID := uuid.New()
	require.NoError(t, ingester.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{
		projectID: {
			{ID: uuid.New(), ProjectID: projectID, Timestamp: now, Level: logs_core.LogLevelInfo, Message: "cold tier"},
		},
	}))
	_, err = ingester.FlushAll()
	require.NoError(t, err)
	require.NoError(t, ingester.Close())

	// Upload and reclaim the local copy: a segment only in cold storage.
	store, err := segments.NewObjectStore(context.Background(), "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	uploader := logs_uploader.NewLogUploader(store)
	uploader.DeleteLocalAfterUpload = true
	uploaded, err := uploader.UploadPending(context.Background(), 10)
	require.NoError(t, err)
	require.GreaterOrEqual(t, uploaded, 1)

	repo := &segments.SegmentRepository{}
	records, err := repo.ListByProject(projectID, "logs")
	require.NoError(t, err)
	require.NoFileExists(t, records[0].Path, "sanity check: local copy really is gone")

	cacheDir := t.TempDir()
	querier, err := logs_querier.NewLogQuerier(logs_querier.WithObjectStore(store, cacheDir))
	require.NoError(t, err)
	t.Cleanup(func() { _ = querier.Close() })

	response, err := querier.ExecuteQuery(projectID, queryRequest(nil, now.Add(-time.Minute), now.Add(time.Minute)))

	require.NoError(t, err)
	require.Equal(t, int64(1), response.Total)
	require.Equal(t, "cold tier", response.Logs[0].Message)
}

// A stale catalog row (file gone, never uploaded) yields an empty result, not
// an error that would fail the project's queries forever.
func Test_ExecuteQuery_SegmentMissingLocallyAndNoObjectStoreConfigured_ReturnsEmptyResponse(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	dataDir := t.TempDir()

	ingester, err := logs_ingester.NewLogIngester(dataDir, segments.WithMaxRows(1_000_000))
	require.NoError(t, err)
	projectID := uuid.New()
	require.NoError(t, ingester.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{
		projectID: {
			{ID: uuid.New(), ProjectID: projectID, Timestamp: now, Level: logs_core.LogLevelInfo, Message: "gone"},
		},
	}))
	_, err = ingester.FlushAll()
	require.NoError(t, err)
	require.NoError(t, ingester.Close())

	// Delete the local file without ever uploading it.
	repo := &segments.SegmentRepository{}
	records, err := repo.ListByProject(projectID, "logs")
	require.NoError(t, err)
	require.NoError(t, os.Remove(records[0].Path))

	querier, err := logs_querier.NewLogQuerier() // no WithObjectStore
	require.NoError(t, err)
	t.Cleanup(func() { _ = querier.Close() })

	response, err := querier.ExecuteQuery(projectID, queryRequest(nil, now.Add(-time.Minute), now.Add(time.Minute)))

	require.NoError(t, err)
	require.Equal(t, int64(0), response.Total)
	require.Empty(t, response.Logs)
}
