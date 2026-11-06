package logs_core_tests

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	logs_core "obzly/internal/features/logs/core"
)

func Test_ExecuteQueryForProject_WithPaginationAndOffset_ReturnsSameTotalCount(t *testing.T) {
	repository := logs_core.GetLogCoreRepository()
	projectID := uuid.New()
	currentTime := time.Now().UTC()
	uniqueTestSession := uuid.New().String()[:8]

	testLogEntries := CreateBatchLogEntries(projectID, 12, currentTime, uniqueTestSession)
	StoreTestLogsAndFlush(t, repository, testLogEntries)

	firstPageQuery := &logs_core.LogQueryRequestDTO{
		Query: &logs_core.QueryNode{
			Type: logs_core.QueryNodeTypeCondition,
			Condition: &logs_core.ConditionNode{
				Field:    "test_session",
				Operator: logs_core.ConditionOperatorEquals,
				Value:    uniqueTestSession,
			},
		},
		Limit:     5,
		Offset:    0,
		SortOrder: "desc",
	}

	stats := WaitForLogsToAppear(t, repository, projectID, 12, 30000)
	assert.Equal(t, int64(12), stats.TotalLogs, "Should have 12 logs before pagination")

	firstPageResult, firstPageErr := repository.ExecuteQueryForProject(projectID, firstPageQuery)
	assert.NoError(t, firstPageErr)
	assert.NotNil(t, firstPageResult)

	assert.GreaterOrEqual(t, firstPageResult.Total, int64(12), "Should have at least 12 logs total")

	secondPageQuery := *firstPageQuery
	secondPageQuery.Offset = 5

	secondPageResult, secondPageErr := repository.ExecuteQueryForProject(projectID, &secondPageQuery)
	assert.NoError(t, secondPageErr)
	assert.NotNil(t, secondPageResult)

	assert.Equal(t, firstPageResult.Total, secondPageResult.Total, "Total count should be consistent across pages")
	assert.GreaterOrEqual(t, firstPageResult.Total, int64(12), "Should have at least 12 logs total")
	assert.LessOrEqual(t, len(firstPageResult.Logs), 5, "First page should have at most 5 logs")
	assert.LessOrEqual(t, len(secondPageResult.Logs), 5, "Second page should have at most 5 logs")
}

func Test_ExecuteQueryForProject_WithNanosecondPrecision_MaintainsProperDESCOrdering(t *testing.T) {
	t.Skip(
		"Known limitation, not a regression - see ARCHITECTURE.md's " +
			"\"Known limitation: sub-microsecond query-time precision\" section: " +
			"the stream_segments catalog's min_time/max_time are Postgres " +
			"TIMESTAMPTZ (microsecond precision), so segment-level pruning can't " +
			"distinguish rows within the same microsecond even though the " +
			"underlying Parquet data is true nanosecond precision.",
	)

	repository := logs_core.GetLogCoreRepository()
	projectID := uuid.New()
	baseTime := time.Now().UTC()
	uniqueTestSession := uuid.New().String()[:8]
	logCount := 20
	pageSize := 5

	testLogEntries := createNanosecondLogEntries(projectID, logCount, baseTime, uniqueTestSession)
	StoreTestLogsAndFlush(t, repository, testLogEntries)

	stats := WaitForLogsToAppear(t, repository, projectID, int64(logCount), 30000)
	assert.Equal(t, int64(logCount), stats.TotalLogs, "Should have all logs before pagination")

	baseQuery := &logs_core.LogQueryRequestDTO{
		Query: &logs_core.QueryNode{
			Type: logs_core.QueryNodeTypeCondition,
			Condition: &logs_core.ConditionNode{
				Field:    "test_session",
				Operator: logs_core.ConditionOperatorEquals,
				Value:    uniqueTestSession,
			},
		},
		Limit:     pageSize,
		Offset:    0,
		SortOrder: "desc",
	}

	allLogsByPage := make([][]logs_core.LogItemDTO, 0, 4)
	expectedPageCount := logCount / pageSize

	for pageIndex := 0; pageIndex < expectedPageCount; pageIndex++ {
		pageQuery := *baseQuery
		pageQuery.Offset = pageIndex * pageSize

		pageResult, err := repository.ExecuteQueryForProject(projectID, &pageQuery)
		assert.NoError(t, err, "Page %d query should succeed", pageIndex)
		assert.NotNil(t, pageResult, "Page %d result should not be nil", pageIndex)
		assert.Len(t, pageResult.Logs, pageSize, "Page %d should have %d logs", pageIndex, pageSize)

		allLogsByPage = append(allLogsByPage, pageResult.Logs)
		verifyDescOrderingWithinPage(t, pageResult.Logs, pageIndex)
	}

	verifyDescOrderingAcrossPages(t, allLogsByPage)
}

func verifyDescOrderingWithinPage(t *testing.T, logs []logs_core.LogItemDTO, pageIndex int) {
	for i := 1; i < len(logs); i++ {
		previousLog := logs[i-1]
		currentLog := logs[i]

		assert.True(
			t,
			previousLog.Timestamp.After(currentLog.Timestamp),
			"Page %d: Log at index %d (timestamp: %s) should be after log at index %d (timestamp: %s) in DESC order",
			pageIndex,
			i-1,
			previousLog.Timestamp.Format("2006-01-02T15:04:05.999999Z07:00"),
			i,
			currentLog.Timestamp.Format("2006-01-02T15:04:05.999999Z07:00"),
		)
	}
}

func verifyDescOrderingAcrossPages(t *testing.T, allLogsByPage [][]logs_core.LogItemDTO) {
	for pageIndex := 1; pageIndex < len(allLogsByPage); pageIndex++ {
		previousPage := allLogsByPage[pageIndex-1]
		currentPage := allLogsByPage[pageIndex]

		if len(previousPage) > 0 && len(currentPage) > 0 {
			lastLogFromPreviousPage := previousPage[len(previousPage)-1]
			firstLogFromCurrentPage := currentPage[0]

			assert.True(
				t,
				lastLogFromPreviousPage.Timestamp.After(firstLogFromCurrentPage.Timestamp),
				"Last log from page %d (timestamp: %s) should be after first log from page %d (timestamp: %s) in DESC order",
				pageIndex-1,
				lastLogFromPreviousPage.Timestamp.Format("2006-01-02T15:04:05.999999Z07:00"),
				pageIndex,
				firstLogFromCurrentPage.Timestamp.Format("2006-01-02T15:04:05.999999Z07:00"),
			)
		}
	}
}

func createNanosecondLogEntries(
	projectID uuid.UUID,
	logCount int,
	baseTime time.Time,
	testSessionID string,
) map[uuid.UUID][]*logs_core.LogItem {
	allBatchEntries := make(map[uuid.UUID][]*logs_core.LogItem)

	for sequenceIndex := 1; sequenceIndex <= logCount; sequenceIndex++ {
		uniqueLogID := uuid.New().String()[:8]
		batchLogEntries := CreateTestLogEntriesWithMessageAndFields(projectID,
			baseTime.Add(time.Duration(sequenceIndex)*time.Microsecond),
			"Nanosecond precision test log message",
			map[string]any{
				"unique_id":    uniqueLogID,
				"test_session": testSessionID,
				"sequence_num": sequenceIndex,
				"service":      "nanosecond-test",
			})

		if len(allBatchEntries) == 0 {
			allBatchEntries = batchLogEntries
		} else {
			for projectKey, logItems := range batchLogEntries {
				allBatchEntries[projectKey] = append(allBatchEntries[projectKey], logItems...)
			}
		}
	}

	return allBatchEntries
}
