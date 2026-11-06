package logs_core_tests

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	logs_core "obzly/internal/features/logs/core"
)

func CreateTestLogEntries() map[uuid.UUID][]*logs_core.LogItem {
	projectID := uuid.New()
	logID := uuid.New()
	currentTime := time.Now().UTC()

	testLogItem := &logs_core.LogItem{
		ID:        logID,
		ProjectID: projectID,
		Timestamp: currentTime,
		Level:     logs_core.LogLevelInfo,
		Message:   "Test log message",
		Fields: map[string]any{
			"component": "test",
			"action":    "test_action",
		},
		ClientIP: "127.0.0.1",
	}

	return map[uuid.UUID][]*logs_core.LogItem{
		projectID: {testLogItem},
	}
}

func CreateTestLogEntriesWithTimestamp(
	projectID uuid.UUID,
	timestamp time.Time,
	message string,
) map[uuid.UUID][]*logs_core.LogItem {
	logEntryID := uuid.New()

	testLogEntry := &logs_core.LogItem{
		ID:        logEntryID,
		ProjectID: projectID,
		Timestamp: timestamp,
		Level:     logs_core.LogLevelInfo,
		Message:   message,
		Fields: map[string]any{
			"component": "test",
			"action":    "test_action",
		},
		ClientIP: "127.0.0.1",
	}

	return map[uuid.UUID][]*logs_core.LogItem{
		projectID: {testLogEntry},
	}
}

func CreateTestLogEntriesWithMessageAndFields(
	projectID uuid.UUID,
	timestamp time.Time,
	message string,
	fields map[string]any,
) map[uuid.UUID][]*logs_core.LogItem {
	logEntryID := uuid.New()

	testLogEntryWithFields := &logs_core.LogItem{
		ID:        logEntryID,
		ProjectID: projectID,
		Timestamp: timestamp,
		Level:     logs_core.LogLevelInfo,
		Message:   message,
		Fields:    fields,
		ClientIP:  "127.0.0.1",
	}

	return map[uuid.UUID][]*logs_core.LogItem{
		projectID: {testLogEntryWithFields},
	}
}

func GetFirstProjectID(testLogEntries map[uuid.UUID][]*logs_core.LogItem) uuid.UUID {
	for projectID := range testLogEntries {
		return projectID
	}
	return uuid.UUID{}
}

// StoreTestLogsAndFlush stores and flushes each log item as its own batch, so
// each lands in its own segment: retention deletes whole segments, so old and
// new logs in one segment couldn't be deleted separately.
func StoreTestLogsAndFlush(
	t *testing.T,
	repository logs_core.LogRepository,
	testLogEntries map[uuid.UUID][]*logs_core.LogItem,
) {
	for projectID, items := range testLogEntries {
		for _, item := range items {
			storeErr := repository.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{projectID: {item}})
			assert.NoError(t, storeErr, "Failed to store test data")

			flushErr := repository.ForceFlush()
			assert.NoError(t, flushErr, "Failed to flush stored logs")
		}
	}
}

func CreateBatchLogEntries(
	projectID uuid.UUID,
	logCount int,
	baseTime time.Time,
	testSessionID string,
) map[uuid.UUID][]*logs_core.LogItem {
	var allBatchEntries map[uuid.UUID][]*logs_core.LogItem

	for sequenceIndex := 1; sequenceIndex <= logCount; sequenceIndex++ {
		uniqueLogID := uuid.New().String()[:8]
		batchLogEntries := CreateTestLogEntriesWithMessageAndFields(projectID,
			baseTime.Add(time.Duration(sequenceIndex)*time.Second),
			"Test batch log message",
			map[string]any{
				"unique_id":    uniqueLogID,
				"test_session": testSessionID,
				"sequence_num": sequenceIndex,
				"service":      "api",
			})

		if allBatchEntries == nil {
			allBatchEntries = batchLogEntries
		} else {
			for projectKey, logItems := range batchLogEntries {
				allBatchEntries[projectKey] = append(allBatchEntries[projectKey], logItems...)
			}
		}
	}

	return allBatchEntries
}

func CreateTestLogEntriesWithUniqueFields(
	projectID uuid.UUID,
	timestamp time.Time,
	message string,
	customFields map[string]any,
) map[uuid.UUID][]*logs_core.LogItem {
	logEntryID := uuid.New()

	uniqueLogEntry := &logs_core.LogItem{
		ID:        logEntryID,
		ProjectID: projectID,
		Timestamp: timestamp,
		Level:     logs_core.LogLevelInfo,
		Message:   message,
		Fields:    customFields,
		ClientIP:  "127.0.0.1",
	}

	return map[uuid.UUID][]*logs_core.LogItem{
		projectID: {uniqueLogEntry},
	}
}

func MergeLogEntries(
	firstLogEntries, secondLogEntries map[uuid.UUID][]*logs_core.LogItem,
) map[uuid.UUID][]*logs_core.LogItem {
	mergedLogEntries := make(map[uuid.UUID][]*logs_core.LogItem)

	for projectID, logItems := range firstLogEntries {
		mergedLogEntries[projectID] = append(mergedLogEntries[projectID], logItems...)
	}

	for projectID, logItems := range secondLogEntries {
		mergedLogEntries[projectID] = append(mergedLogEntries[projectID], logItems...)
	}

	return mergedLogEntries
}

func WaitForLogsDeletion(
	t *testing.T,
	repository logs_core.LogRepository,
	projectID uuid.UUID,
	query *logs_core.LogQueryRequestDTO,
	timeout time.Duration,
) {
	checkInterval := 50 * time.Millisecond
	startTime := time.Now()

	for {
		result, err := repository.ExecuteQueryForProject(projectID, query)
		assert.NoError(t, err, "Repository query should work during deletion wait")

		if len(result.Logs) == 0 && result.Total == 0 {
			return
		}

		if time.Since(startTime) > timeout {
			t.Fatalf("Timeout: logs still exist after %v. Found %d logs, total: %d",
				timeout, len(result.Logs), result.Total)
		}

		time.Sleep(checkInterval)
	}
}

func WaitForLogsPartialDeletion(
	t *testing.T,
	repository logs_core.LogRepository,
	projectID uuid.UUID,
	query *logs_core.LogQueryRequestDTO,
	expectedCount int64,
	timeout time.Duration,
) {
	checkInterval := 50 * time.Millisecond
	startTime := time.Now()

	for {
		result, err := repository.ExecuteQueryForProject(projectID, query)
		assert.NoError(t, err, "Repository query should work during deletion wait")

		if result.Total == expectedCount {
			return
		}

		if time.Since(startTime) > timeout {
			t.Fatalf("Timeout: expected %d logs but found %d after %v",
				expectedCount, result.Total, timeout)
		}

		time.Sleep(checkInterval)
	}
}

func WaitForLogsToAppear(
	t *testing.T,
	repository logs_core.LogRepository,
	projectID uuid.UUID,
	expectedCount int64,
	timeoutMs int,
) *logs_core.LogsStatsDTO {
	const pollIntervalMs = 50
	maxAttempts := timeoutMs / pollIntervalMs

	for attempt := range maxAttempts {
		err := repository.ForceFlush()
		assert.NoError(t, err, "Force flush should not fail on attempt %d", attempt+1)

		stats, err := repository.GetProjectLogStats(projectID)
		assert.NoError(t, err, "GetProjectLogStats should not fail on attempt %d", attempt+1)

		if stats.TotalLogs == expectedCount {
			return stats
		}

		time.Sleep(pollIntervalMs * time.Millisecond)
	}

	err := repository.ForceFlush()
	assert.NoError(t, err, "Final force flush should not fail")

	stats, err := repository.GetProjectLogStats(projectID)
	assert.NoError(t, err, "Final GetProjectLogStats should not fail")

	assert.Equal(t, expectedCount, stats.TotalLogs,
		"Expected %d logs to appear, but found %d (timeout after %dms)",
		expectedCount, stats.TotalLogs, timeoutMs)

	return stats
}

func WaitForSystemLogsToAppear(
	t *testing.T,
	repository logs_core.LogRepository,
	minExpectedCount int64,
	timeoutMs int,
) *logs_core.LogsStatsDTO {
	const pollIntervalMs = 50
	maxAttempts := timeoutMs / pollIntervalMs

	for attempt := range maxAttempts {
		err := repository.ForceFlush()
		assert.NoError(t, err, "Force flush should not fail on attempt %d", attempt+1)

		stats, err := repository.GetSystemLogStats()
		assert.NoError(t, err, "GetSystemLogStats should not fail on attempt %d", attempt+1)

		if stats.TotalLogs >= minExpectedCount {
			return stats
		}

		time.Sleep(pollIntervalMs * time.Millisecond)
	}

	err := repository.ForceFlush()
	assert.NoError(t, err, "Final force flush should not fail")

	stats, err := repository.GetSystemLogStats()
	assert.NoError(t, err, "Final GetSystemLogStats should not fail")

	assert.GreaterOrEqual(t, stats.TotalLogs, minExpectedCount,
		"Expected at least %d logs to appear, but found %d (timeout after %dms)",
		minExpectedCount, stats.TotalLogs, timeoutMs)

	return stats
}
