package logs_cleanup_tests

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	logs_core "obzly/internal/features/logs/core"
)

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

func WaitForLogDeletion(
	t *testing.T,
	repository logs_core.LogRepository,
	projectID uuid.UUID,
	expectedCount int64,
	timeoutMs int,
) *logs_core.LogsStatsDTO {
	const pollIntervalMs = 50
	maxAttempts := timeoutMs / pollIntervalMs

	for attempt := 0; attempt < maxAttempts; attempt++ {
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
		"Expected %d logs after deletion, but found %d (timeout after %dms)",
		expectedCount, stats.TotalLogs, timeoutMs)

	return stats
}

func WaitForLogDeletionWithMaxCount(
	t *testing.T,
	repository logs_core.LogRepository,
	projectID uuid.UUID,
	maxCount int64,
	timeoutMs int,
) *logs_core.LogsStatsDTO {
	const pollIntervalMs = 50
	maxAttempts := timeoutMs / pollIntervalMs

	for attempt := range maxAttempts {
		err := repository.ForceFlush()
		assert.NoError(t, err, "Force flush should not fail on attempt %d", attempt+1)

		stats, err := repository.GetProjectLogStats(projectID)
		assert.NoError(t, err, "GetProjectLogStats should not fail on attempt %d", attempt+1)

		if stats.TotalLogs <= maxCount {
			return stats
		}

		time.Sleep(pollIntervalMs * time.Millisecond)
	}

	err := repository.ForceFlush()
	assert.NoError(t, err, "Final force flush should not fail")

	stats, err := repository.GetProjectLogStats(projectID)
	assert.NoError(t, err, "Final GetProjectLogStats should not fail")

	assert.LessOrEqual(t, stats.TotalLogs, maxCount,
		"Expected at most %d logs after deletion, but found %d (timeout after %dms)",
		maxCount, stats.TotalLogs, timeoutMs)

	return stats
}
