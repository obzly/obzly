package logs_core_tests

import (
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	logs_core "obzly/internal/features/logs/core"
)

func Test_GetProjectLogStats_WithMultipleLogs_ReturnsCorrectStats(t *testing.T) {
	repository := logs_core.GetLogCoreRepository()
	projectID := uuid.New()
	uniqueTestSession := uuid.New().String()[:8]
	baseTime := time.Now().UTC()

	oldTime := baseTime.Add(-2 * time.Hour)
	recentTime := baseTime.Add(-1 * time.Hour)
	newestTime := baseTime.Add(-30 * time.Minute)

	oldLogEntries := CreateTestLogEntriesWithUniqueFields(projectID, oldTime,
		"Old log for stats test", map[string]any{
			"test_session": uniqueTestSession,
			"log_order":    1,
		})

	recentLogEntries := CreateTestLogEntriesWithUniqueFields(projectID, recentTime,
		"Recent log for stats test", map[string]any{
			"test_session": uniqueTestSession,
			"log_order":    2,
		})

	newestLogEntries := CreateTestLogEntriesWithUniqueFields(projectID, newestTime,
		"Newest log for stats test", map[string]any{
			"test_session": uniqueTestSession,
			"log_order":    3,
		})

	allEntries := MergeLogEntries(oldLogEntries, recentLogEntries)
	allEntries = MergeLogEntries(allEntries, newestLogEntries)
	StoreTestLogsAndFlush(t, repository, allEntries)

	stats := WaitForLogsToAppear(t, repository, projectID, 3, 30000)

	assert.Equal(t, int64(3), stats.TotalLogs, "Should have 3 total logs")
	// Not exactly 0: Parquet overhead shifts with the schema; the intent is
	// "near zero for 3 small logs".
	assert.Less(t, stats.TotalSizeMB, 0.1, "TotalSizeMB should be small for 3 tiny log entries")

	// Tolerance for timestamp precision.
	timeTolerance := 10 * time.Second
	assert.WithinDuration(t, oldTime, stats.OldestLogTime, timeTolerance,
		"Oldest log time should match the earliest log timestamp")
	assert.WithinDuration(t, newestTime, stats.NewestLogTime, timeTolerance,
		"Newest log time should match the latest log timestamp")
}

func Test_GetProjectLogStats_WithNoLogs_ReturnsZeroStats(t *testing.T) {
	repository := logs_core.GetLogCoreRepository()
	projectID := uuid.New()

	err := repository.ForceFlush()
	assert.NoError(t, err)

	stats, err := repository.GetProjectLogStats(projectID)
	assert.NoError(t, err)
	assert.NotNil(t, stats)

	assert.Equal(t, int64(0), stats.TotalLogs, "Should have 0 total logs for empty project")
	assert.Equal(t, float64(0), stats.TotalSizeMB, "TotalSizeMB should be 0")
	assert.True(t, stats.OldestLogTime.IsZero(), "OldestLogTime should be zero time for empty project")
	assert.True(t, stats.NewestLogTime.IsZero(), "NewestLogTime should be zero time for empty project")
}

func Test_GetProjectLogStats_WithSingleLog_ReturnsCorrectStats(t *testing.T) {
	repository := logs_core.GetLogCoreRepository()
	projectID := uuid.New()
	uniqueTestSession := uuid.New().String()[:8]
	logTime := time.Now().UTC()

	singleLogEntries := CreateTestLogEntriesWithUniqueFields(projectID, logTime,
		"Single log for stats test", map[string]any{
			"test_session": uniqueTestSession,
			"single_log":   true,
		})

	StoreTestLogsAndFlush(t, repository, singleLogEntries)

	stats := WaitForLogsToAppear(t, repository, projectID, 1, 30000)

	assert.Equal(t, int64(1), stats.TotalLogs, "Should have 1 total log")
	assert.Equal(t, float64(0), math.Round(stats.TotalSizeMB*100)/100, "TotalSizeMB should be 0")
	assert.Positive(t, stats.RawSizeMB,
		"RawSizeMB (uncompressed row accounting) should be nonzero even when "+
			"the tiny compressed Parquet file itself rounds to 0MB")

	// For single log, oldest and newest should be the same
	timeTolerance := 10 * time.Second
	assert.WithinDuration(t, logTime, stats.OldestLogTime, timeTolerance,
		"Oldest log time should match the single log timestamp")
	assert.WithinDuration(t, logTime, stats.NewestLogTime, timeTolerance,
		"Newest log time should match the single log timestamp")
}

func Test_GetProjectLogStats_WithTwelveHourTimeGap_ReturnsCorrectTimestamps(t *testing.T) {
	repository := logs_core.GetLogCoreRepository()
	projectID := uuid.New()
	uniqueTestSession := uuid.New().String()[:8]

	now := time.Now().UTC()
	twelveHoursAgo := now.Add(-12 * time.Hour)

	oldLogEntries := CreateTestLogEntriesWithUniqueFields(projectID, twelveHoursAgo,
		"Log from 12 hours ago", map[string]any{
			"test_session":   uniqueTestSession,
			"timestamp_test": "twelve_hours_ago",
		})

	newLogEntries := CreateTestLogEntriesWithUniqueFields(projectID, now,
		"Log from now", map[string]any{
			"test_session":   uniqueTestSession,
			"timestamp_test": "now",
		})

	allEntries := MergeLogEntries(oldLogEntries, newLogEntries)
	StoreTestLogsAndFlush(t, repository, allEntries)

	stats := WaitForLogsToAppear(t, repository, projectID, 2, 30000)

	assert.Equal(t, int64(2), stats.TotalLogs, "Should have 2 total logs")
	assert.Greater(t, stats.TotalSizeMB, float64(0), "TotalSizeMB should be greater than 0")

	timeTolerance := 10 * time.Second
	assert.WithinDuration(t, twelveHoursAgo, stats.OldestLogTime, timeTolerance,
		"Oldest log time should match the 12-hour-ago timestamp")
	assert.WithinDuration(t, now, stats.NewestLogTime, timeTolerance,
		"Newest log time should match the current timestamp")

	actualGap := stats.NewestLogTime.Sub(stats.OldestLogTime)
	expectedGap := 12 * time.Hour
	gapTolerance := 1 * time.Minute

	assert.InDelta(t, expectedGap.Seconds(), actualGap.Seconds(), gapTolerance.Seconds(),
		"Time gap between oldest and newest log should be approximately 12 hours")
}

func Test_GetSystemLogStats_WithMultipleProjects_ReturnsAggregatedStats(t *testing.T) {
	repository := logs_core.GetLogCoreRepository()
	uniqueTestSession := uuid.New().String()[:8]

	project1 := uuid.New()
	project2 := uuid.New()
	project3 := uuid.New()

	baseTime := time.Now().UTC()
	oldestTime := baseTime.Add(-3 * time.Hour)
	middleTime := baseTime.Add(-1 * time.Hour)
	newestTime := baseTime.Add(-30 * time.Minute)

	statsBefore, err := repository.GetSystemLogStats()
	assert.NoError(t, err)
	assert.NotNil(t, statsBefore)

	project1OldLogEntries := CreateTestLogEntriesWithUniqueFields(project1, oldestTime,
		"Project 1 oldest log", map[string]any{
			"test_session": uniqueTestSession,
			"project":      "project1",
		})

	project2MiddleLogEntries := CreateTestLogEntriesWithUniqueFields(project2, middleTime,
		"Project 2 middle log", map[string]any{
			"test_session": uniqueTestSession,
			"project":      "project2",
		})

	project3NewestLogEntries := CreateTestLogEntriesWithUniqueFields(project3, newestTime,
		"Project 3 newest log", map[string]any{
			"test_session": uniqueTestSession,
			"project":      "project3",
		})

	allEntries := MergeLogEntries(project1OldLogEntries, project2MiddleLogEntries)
	allEntries = MergeLogEntries(allEntries, project3NewestLogEntries)
	StoreTestLogsAndFlush(t, repository, allEntries)

	statsAfter := WaitForSystemLogsToAppear(t, repository, statsBefore.TotalLogs+3, 30000)

	assert.GreaterOrEqual(t, statsAfter.TotalLogs, statsBefore.TotalLogs+3,
		"Should have at least 3 more logs after adding test data")

	assert.False(t, statsAfter.OldestLogTime.IsZero(), "Oldest log time should not be zero")
	assert.False(t, statsAfter.NewestLogTime.IsZero(), "Newest log time should not be zero")
	assert.True(t, statsAfter.NewestLogTime.After(statsAfter.OldestLogTime) ||
		statsAfter.NewestLogTime.Equal(statsAfter.OldestLogTime),
		"Newest log should be after or equal to oldest log")

	assert.GreaterOrEqual(t, statsAfter.TotalSizeMB, float64(0), "TotalSizeMB should be non-negative")
}
