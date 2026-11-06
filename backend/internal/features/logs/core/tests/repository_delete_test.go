package logs_core_tests

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	logs_core "obzly/internal/features/logs/core"
)

func Test_DeleteOldLogs_WhenOldLogsExist_DeletesLogsOlderThanSpecifiedTime(t *testing.T) {
	repository := logs_core.GetLogCoreRepository()
	projectID := uuid.New()
	uniqueTestSession := uuid.New().String()[:8]
	baseTime := time.Now().UTC()

	oldTime := baseTime.Add(-48 * time.Hour)
	mediumTime := baseTime.Add(-18 * time.Hour)
	recentTime := baseTime.Add(-1 * time.Hour)
	cutoffTime := baseTime.Add(-12 * time.Hour)

	oldLogEntries := CreateTestLogEntriesWithUniqueFields(projectID, oldTime,
		"Old log that should be deleted", map[string]any{
			"test_session": uniqueTestSession,
			"log_type":     "old",
		})

	mediumLogEntries := CreateTestLogEntriesWithUniqueFields(projectID, mediumTime,
		"Medium log that should also be deleted", map[string]any{
			"test_session": uniqueTestSession,
			"log_type":     "medium",
		})

	recentLogEntries := CreateTestLogEntriesWithUniqueFields(projectID, recentTime,
		"Recent log that should remain", map[string]any{
			"test_session": uniqueTestSession,
			"log_type":     "recent",
		})

	allLogEntries := MergeLogEntries(
		MergeLogEntries(oldLogEntries, mediumLogEntries),
		recentLogEntries,
	)
	StoreTestLogsAndFlush(t, repository, allLogEntries)

	beforeDeletionQuery := &logs_core.LogQueryRequestDTO{
		Query: &logs_core.QueryNode{
			Type: logs_core.QueryNodeTypeCondition,
			Condition: &logs_core.ConditionNode{
				Field:    "test_session",
				Operator: logs_core.ConditionOperatorEquals,
				Value:    uniqueTestSession,
			},
		},
		Limit: 10,
	}

	beforeDeletionResult, err := repository.ExecuteQueryForProject(projectID, beforeDeletionQuery)
	assert.NoError(t, err)

	err = repository.DeleteOldLogs(projectID, cutoffTime)
	assert.NoError(t, err)

	var hasRecentLogs func(result *logs_core.LogQueryResponseDTO) bool = func(result *logs_core.LogQueryResponseDTO) bool {
		for _, log := range result.Logs {
			if log.Fields != nil {
				if logType, ok := log.Fields["log_type"].(string); ok && logType == "recent" {
					return true
				}
			}
		}
		return false
	}

	var hasOldLogs func(result *logs_core.LogQueryResponseDTO) bool = func(result *logs_core.LogQueryResponseDTO) bool {
		for _, log := range result.Logs {
			if log.Fields != nil {
				if logType, ok := log.Fields["log_type"].(string); ok && logType == "old" {
					return true
				}
			}
		}
		return false
	}

	var hasMediumLogs func(result *logs_core.LogQueryResponseDTO) bool = func(result *logs_core.LogQueryResponseDTO) bool {
		for _, log := range result.Logs {
			if log.Fields != nil {
				if logType, ok := log.Fields["log_type"].(string); ok && logType == "medium" {
					return true
				}
			}
		}
		return false
	}

	afterDeletionResult := waitForDeletionWithCondition(t, repository, projectID, beforeDeletionQuery,
		func(result *logs_core.LogQueryResponseDTO) bool {
			return hasRecentLogs(result) && !hasOldLogs(result) && !hasMediumLogs(result)
		},
		"logs should be deleted and total count should decrease", 60_000)

	assert.NotNil(t, afterDeletionResult)

	assert.Less(t, afterDeletionResult.Total, beforeDeletionResult.Total)

	assert.True(t, hasRecentLogs(afterDeletionResult), "Recent logs should still exist")
	assert.False(t, hasOldLogs(afterDeletionResult), "Old logs should be deleted")
	assert.False(t, hasMediumLogs(afterDeletionResult), "Medium logs should be deleted")
}

func Test_DeleteLogsByProject_WhenProjectLogsExist_DeletesAllProjectLogs(t *testing.T) {
	repository := logs_core.GetLogCoreRepository()
	project1ID := uuid.New()
	project2ID := uuid.New()
	uniqueTestSession := uuid.New().String()[:8]
	currentTime := time.Now().UTC()

	project1LogEntries := CreateBatchLogEntries(project1ID, 3, currentTime, uniqueTestSession+"_p1")

	project2LogEntries := CreateBatchLogEntries(project2ID, 3, currentTime, uniqueTestSession+"_p2")

	StoreTestLogsAndFlush(t, repository, project1LogEntries)
	StoreTestLogsAndFlush(t, repository, project2LogEntries)

	project1BeforeDeletionQuery := &logs_core.LogQueryRequestDTO{
		Query: &logs_core.QueryNode{
			Type: logs_core.QueryNodeTypeCondition,
			Condition: &logs_core.ConditionNode{
				Field:    "test_session",
				Operator: logs_core.ConditionOperatorEquals,
				Value:    uniqueTestSession + "_p1",
			},
		},
		Limit: 10,
	}

	project2BeforeDeletionQuery := &logs_core.LogQueryRequestDTO{
		Query: &logs_core.QueryNode{
			Type: logs_core.QueryNodeTypeCondition,
			Condition: &logs_core.ConditionNode{
				Field:    "test_session",
				Operator: logs_core.ConditionOperatorEquals,
				Value:    uniqueTestSession + "_p2",
			},
		},
		Limit: 10,
	}

	project1BeforeDeletionResult, err := repository.ExecuteQueryForProject(project1ID, project1BeforeDeletionQuery)
	assert.NoError(t, err)

	project2BeforeDeletionResult, err := repository.ExecuteQueryForProject(project2ID, project2BeforeDeletionQuery)
	assert.NoError(t, err)

	assert.GreaterOrEqual(
		t,
		project1BeforeDeletionResult.Total,
		int64(3),
		"Project 1 should have at least 3 test logs before deletion",
	)
	assert.GreaterOrEqual(
		t,
		project2BeforeDeletionResult.Total,
		int64(3),
		"Project 2 should have at least 3 test logs before deletion",
	)

	err = repository.DeleteLogsByProject(project1ID)
	assert.NoError(t, err)

	project1AfterDeletionResult := waitForDeletionCompletion(t, repository, project1ID,
		project1BeforeDeletionQuery, 0, 60_000)

	assert.Equal(t, int64(0), project1AfterDeletionResult.Total, "Project 1 logs should be deleted")
	assert.Empty(t, project1AfterDeletionResult.Logs, "Project 1 should have no logs")

	// Project 2's logs must survive project 1's deletion.
	project2AfterDeletionResult := waitForDeletionCompletion(t, repository, project2ID,
		project2BeforeDeletionQuery, project2BeforeDeletionResult.Total, 60_000)

	assert.Equal(
		t,
		project2BeforeDeletionResult.Total,
		project2AfterDeletionResult.Total,
		"Project 2 logs should remain unchanged",
	)
	assert.Len(
		t,
		project2AfterDeletionResult.Logs,
		len(project2BeforeDeletionResult.Logs),
		"Project 2 should still have all logs",
	)
}

func Test_DeleteLogsByProject_WithNonExistentProject_DoesNotFail(t *testing.T) {
	repository := logs_core.GetLogCoreRepository()
	nonExistentProjectID := uuid.New()

	err := repository.DeleteLogsByProject(nonExistentProjectID)
	assert.NoError(t, err, "Deleting logs for non-existent project should not fail")
}

func Test_DeleteOldLogs_WithNoOldLogs_DoesNotFail(t *testing.T) {
	repository := logs_core.GetLogCoreRepository()
	projectID := uuid.New()
	uniqueTestSession := uuid.New().String()[:8]
	currentTime := time.Now().UTC()

	recentLogEntries := CreateTestLogEntriesWithUniqueFields(projectID, currentTime.Add(-1*time.Hour),
		"Recent log", map[string]any{
			"test_session": uniqueTestSession,
		})

	StoreTestLogsAndFlush(t, repository, recentLogEntries)

	// Nothing older than 48 hours to delete.
	cutoffTime := currentTime.Add(-48 * time.Hour)
	err := repository.DeleteOldLogs(projectID, cutoffTime)
	assert.NoError(t, err, "Deleting old logs when none exist should not fail")

	verificationQuery := &logs_core.LogQueryRequestDTO{
		Query: &logs_core.QueryNode{
			Type: logs_core.QueryNodeTypeCondition,
			Condition: &logs_core.ConditionNode{
				Field:    "test_session",
				Operator: logs_core.ConditionOperatorEquals,
				Value:    uniqueTestSession,
			},
		},
		Limit: 10,
	}

	verificationResult, err := repository.ExecuteQueryForProject(projectID, verificationQuery)
	assert.NoError(t, err)

	assert.GreaterOrEqual(t, verificationResult.Total, int64(1), "Recent logs should still exist")
}
func waitForDeletionCompletion(
	t *testing.T,
	repository logs_core.LogRepository,
	projectID uuid.UUID,
	query *logs_core.LogQueryRequestDTO,
	expectedTotal int64,
	timeoutMs int,
) *logs_core.LogQueryResponseDTO {
	const pollIntervalMs = 50
	maxAttempts := timeoutMs / pollIntervalMs

	for attempt := 0; attempt < maxAttempts; attempt++ {
		err := repository.ForceFlush()
		assert.NoError(t, err, "Force flush should not fail on attempt %d", attempt+1)

		result, err := repository.ExecuteQueryForProject(projectID, query)
		assert.NoError(t, err, "Query should not fail on attempt %d", attempt+1)

		if result.Total == expectedTotal {
			return result
		}

		time.Sleep(pollIntervalMs * time.Millisecond)
	}

	// Final attempt after timeout
	err := repository.ForceFlush()
	assert.NoError(t, err, "Final force flush should not fail")

	result, err := repository.ExecuteQueryForProject(projectID, query)
	assert.NoError(t, err, "Final query should not fail")

	assert.Equal(t, expectedTotal, result.Total,
		"Expected %d logs after deletion, but found %d (timeout after %dms)",
		expectedTotal, result.Total, timeoutMs)

	return result
}

func waitForDeletionWithCondition(
	t *testing.T,
	repository logs_core.LogRepository,
	projectID uuid.UUID,
	query *logs_core.LogQueryRequestDTO,
	conditionCheck func(*logs_core.LogQueryResponseDTO) bool,
	conditionDescription string,
	timeoutMs int,
) *logs_core.LogQueryResponseDTO {
	const pollIntervalMs = 50
	maxAttempts := timeoutMs / pollIntervalMs

	for attempt := range maxAttempts {
		err := repository.ForceFlush()
		assert.NoError(t, err, "Force flush should not fail on attempt %d", attempt+1)

		result, err := repository.ExecuteQueryForProject(projectID, query)
		assert.NoError(t, err, "Query should not fail on attempt %d", attempt+1)

		if conditionCheck(result) {
			return result
		}

		time.Sleep(pollIntervalMs * time.Millisecond)
	}

	// Final attempt after timeout
	err := repository.ForceFlush()
	assert.NoError(t, err, "Final force flush should not fail")

	result, err := repository.ExecuteQueryForProject(projectID, query)
	assert.NoError(t, err, "Final query should not fail")

	assert.True(t, conditionCheck(result),
		"Deletion condition not met: %s (timeout after %dms)",
		conditionDescription, timeoutMs)

	return result
}
