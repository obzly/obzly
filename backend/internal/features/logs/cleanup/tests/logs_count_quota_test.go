package logs_cleanup_tests

import (
	"testing"
	"time"

	logs_cleanup "obzly/internal/features/logs/cleanup"
	logs_core "obzly/internal/features/logs/core"
	logs_core_tests "obzly/internal/features/logs/core/tests"
	projects_controllers "obzly/internal/features/projects/controllers"
	projects_models "obzly/internal/features/projects/models"
	projects_testing "obzly/internal/features/projects/testing"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func Test_EnforceProjectQuotas_WhenLogCountExceedsMaxLogsAmount_DeletesOldestLogs(t *testing.T) {
	users_testing.CleanupPlans()
	// No uploader here, so these segments stay pending upload: keep other
	// packages' global-queue resets out for the whole test.
	test_utils.AcquireExclusiveGlobalQueue(t, "logs-pending-queue")

	router := projects_testing.CreateTestRouter(
		projects_controllers.GetProjectController(),
		projects_controllers.GetMembershipController(),
	)
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	uniqueID := uuid.New().String()[:8]

	// Create test project
	projectName := "Count Quota Test " + uniqueID
	project := projects_testing.CreateTestProject(projectName, owner, router)

	// Update project to set MaxLogsAmount to 10 logs
	updateData := &projects_models.Project{
		Name:                  project.Name,
		IsRateLimitingEnabled: true,
		MaxLogsAmount:         10, // 10 logs limit
	}
	projects_testing.UpdateProject(project, updateData, owner.Token, router)

	// Get repository and cleanup service
	repository := logs_core.GetLogCoreRepository()
	cleanupService := logs_cleanup.GetLogCleanupBackgroundService()

	// Create test timestamps
	now := time.Now().UTC()
	oldTime := now.Add(-2 * time.Hour)       // 2 hours ago (should be deleted)
	recentTime := now.Add(-30 * time.Minute) // 30 minutes ago (should remain)

	// Create old logs (should be deleted)
	var allEntries map[uuid.UUID][]*logs_core.LogItem

	// Create 8 old logs
	for i := range 8 {
		oldLogEntries := logs_core_tests.CreateTestLogEntriesWithUniqueFields(
			project.ID,
			oldTime.Add(time.Duration(i)*time.Second),
			"Old log message for count test",
			map[string]any{
				"test_session": uniqueID,
				"log_type":     "old",
				"log_index":    i,
			},
		)
		if allEntries == nil {
			allEntries = oldLogEntries
		} else {
			allEntries = logs_core_tests.MergeLogEntries(allEntries, oldLogEntries)
		}
	}

	// Create 7 recent logs (total: 15 logs, exceeds limit of 10)
	for i := range 7 {
		recentLogEntries := logs_core_tests.CreateTestLogEntriesWithUniqueFields(
			project.ID,
			recentTime.Add(time.Duration(i)*time.Second),
			"Recent log message for count test",
			map[string]any{
				"test_session": uniqueID,
				"log_type":     "recent",
				"log_index":    8 + i,
			},
		)
		allEntries = logs_core_tests.MergeLogEntries(allEntries, recentLogEntries)
	}

	// Store all logs
	logs_core_tests.StoreTestLogsAndFlush(t, repository, allEntries)

	// Wait for logs to appear
	statsBeforeCleanup := WaitForLogsToAppear(t, repository, project.ID, 15, 30000)
	assert.Equal(t, int64(15), statsBeforeCleanup.TotalLogs, "Should have 15 logs before cleanup")

	t.Logf(
		"Before cleanup: TotalLogs=%d, OldestTime=%v, NewestTime=%v",
		statsBeforeCleanup.TotalLogs,
		statsBeforeCleanup.OldestLogTime,
		statsBeforeCleanup.NewestLogTime,
	)

	// Execute cleanup service
	err := cleanupService.ExecuteAllTasksForTest()
	assert.NoError(t, err, "Cleanup service should execute successfully")

	// Wait for delete operations to complete
	targetLogs := int64(10)
	statsAfterCleanup := WaitForLogDeletion(t, repository, project.ID, targetLogs, 30000)

	t.Logf("After cleanup: TotalLogs=%d, OldestTime=%v, NewestTime=%v",
		statsAfterCleanup.TotalLogs, statsAfterCleanup.OldestLogTime, statsAfterCleanup.NewestLogTime)

	assert.LessOrEqual(t, statsAfterCleanup.TotalLogs, int64(10), "Log count should not exceed quota after cleanup")
	assert.Less(t, statsAfterCleanup.TotalLogs, statsBeforeCleanup.TotalLogs, "Should have fewer logs after cleanup")

	assert.Equal(
		t,
		targetLogs,
		statsAfterCleanup.TotalLogs,
		"Should have exactly the target number of logs after cleanup",
	)

	if !statsAfterCleanup.OldestLogTime.IsZero() && !statsAfterCleanup.NewestLogTime.IsZero() {
		assert.True(t, statsAfterCleanup.OldestLogTime.After(oldTime) || statsAfterCleanup.OldestLogTime.Equal(oldTime),
			"Oldest remaining log should be at or after the old time boundary")
		assert.True(
			t,
			statsAfterCleanup.NewestLogTime.After(recentTime) || statsAfterCleanup.NewestLogTime.Equal(recentTime),
			"Newest remaining log should be from the recent time period",
		)
	}
}

func Test_EnforceProjectQuotas_WhenLogCountIsWithinMaxLogsAmount_NoLogsDeleted(t *testing.T) {
	users_testing.CleanupPlans()
	// No uploader here, so these segments stay pending upload: keep other
	// packages' global-queue resets out for the whole test.
	test_utils.AcquireExclusiveGlobalQueue(t, "logs-pending-queue")

	router := projects_testing.CreateTestRouter(
		projects_controllers.GetProjectController(),
		projects_controllers.GetMembershipController(),
	)
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	uniqueID := uuid.New().String()[:8]

	// Create test project
	projectName := "Count Within Quota Test " + uniqueID
	project := projects_testing.CreateTestProject(projectName, owner, router)

	// Update project to set MaxLogsAmount to 50 logs (large enough to not trigger cleanup)
	updateData := &projects_models.Project{
		Name:                  project.Name,
		IsRateLimitingEnabled: true,
		MaxLogsAmount:         50, // 50 logs limit - large enough to not trigger cleanup
	}
	projects_testing.UpdateProject(project, updateData, owner.Token, router)

	// Get repository and cleanup service
	repository := logs_core.GetLogCoreRepository()
	cleanupService := logs_cleanup.GetLogCleanupBackgroundService()

	// Create test timestamps
	now := time.Now().UTC()
	oldTime := now.Add(-2 * time.Hour)       // 2 hours ago
	recentTime := now.Add(-30 * time.Minute) // 30 minutes ago

	// Create only 20 logs (well below 50 limit)
	var allEntries map[uuid.UUID][]*logs_core.LogItem

	for i := range 10 {
		oldLogEntries := logs_core_tests.CreateTestLogEntriesWithUniqueFields(
			project.ID,
			oldTime.Add(time.Duration(i)*time.Second),
			"Old log for within quota test",
			map[string]any{
				"test_session": uniqueID,
				"log_type":     "old",
				"log_index":    i,
			},
		)
		if allEntries == nil {
			allEntries = oldLogEntries
		} else {
			allEntries = logs_core_tests.MergeLogEntries(allEntries, oldLogEntries)
		}
	}

	for i := range 10 {
		recentLogEntries := logs_core_tests.CreateTestLogEntriesWithUniqueFields(
			project.ID,
			recentTime.Add(time.Duration(i)*time.Second),
			"Recent log for within quota test",
			map[string]any{
				"test_session": uniqueID,
				"log_type":     "recent",
				"log_index":    10 + i,
			},
		)
		allEntries = logs_core_tests.MergeLogEntries(allEntries, recentLogEntries)
	}

	// Store all logs
	logs_core_tests.StoreTestLogsAndFlush(t, repository, allEntries)

	// Wait for logs to appear
	statsBeforeCleanup := WaitForLogsToAppear(t, repository, project.ID, 20, 30000)
	assert.Equal(t, int64(20), statsBeforeCleanup.TotalLogs, "Should have 20 logs before cleanup")
	assert.Less(t, statsBeforeCleanup.TotalLogs, int64(50), "Should be well below 50 logs quota before cleanup")

	t.Logf(
		"Before cleanup: TotalLogs=%d, OldestTime=%v, NewestTime=%v",
		statsBeforeCleanup.TotalLogs,
		statsBeforeCleanup.OldestLogTime,
		statsBeforeCleanup.NewestLogTime,
	)

	// Execute cleanup service
	err := cleanupService.ExecuteAllTasksForTest()
	assert.NoError(t, err, "Cleanup service should execute successfully")

	// Wait for any operations to complete (should remain 20)
	statsAfterCleanup := WaitForLogDeletion(t, repository, project.ID, 20, 30000)

	t.Logf("After cleanup: TotalLogs=%d, OldestTime=%v, NewestTime=%v",
		statsAfterCleanup.TotalLogs, statsAfterCleanup.OldestLogTime, statsAfterCleanup.NewestLogTime)

	assert.Equal(
		t,
		statsBeforeCleanup.TotalLogs,
		statsAfterCleanup.TotalLogs,
		"No logs should be deleted when within quota",
	)
}

func Test_EnforceProjectQuotas_WhenMaxLogsAmountIsZero_NoQuotaEnforcement(t *testing.T) {
	users_testing.CleanupPlans()
	// No uploader here, so these segments stay pending upload: keep other
	// packages' global-queue resets out for the whole test.
	test_utils.AcquireExclusiveGlobalQueue(t, "logs-pending-queue")

	router := projects_testing.CreateTestRouter(
		projects_controllers.GetProjectController(),
		projects_controllers.GetMembershipController(),
	)
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	uniqueID := uuid.New().String()[:8]

	// Create test project
	projectName := "Zero Count Quota Test " + uniqueID
	project := projects_testing.CreateTestProject(projectName, owner, router)

	// Update project to set MaxLogsAmount to 0 (no count-based quota)
	updateData := &projects_models.Project{
		Name:                  project.Name,
		IsRateLimitingEnabled: true,
		MaxLogsAmount:         0, // No count quota enforcement
	}
	projects_testing.UpdateProject(project, updateData, owner.Token, router)

	// Get repository and cleanup service
	repository := logs_core.GetLogCoreRepository()
	cleanupService := logs_cleanup.GetLogCleanupBackgroundService()

	// Create test timestamps
	now := time.Now().UTC()
	oldTime := now.Add(-2 * time.Hour)       // 2 hours ago
	recentTime := now.Add(-30 * time.Minute) // 30 minutes ago

	// Create many logs that would normally trigger cleanup
	var allEntries map[uuid.UUID][]*logs_core.LogItem

	// Create 50 old logs (would trigger cleanup if quota was set)
	for i := range 50 {
		oldLogEntries := logs_core_tests.CreateTestLogEntriesWithUniqueFields(
			project.ID,
			oldTime.Add(time.Duration(i)*time.Second),
			"Old log for zero quota test",
			map[string]any{
				"test_session": uniqueID,
				"log_type":     "old",
				"log_index":    i,
			},
		)
		if allEntries == nil {
			allEntries = oldLogEntries
		} else {
			allEntries = logs_core_tests.MergeLogEntries(allEntries, oldLogEntries)
		}
	}

	// Create 25 recent logs (total: 75 logs)
	for i := range 25 {
		recentLogEntries := logs_core_tests.CreateTestLogEntriesWithUniqueFields(
			project.ID,
			recentTime.Add(time.Duration(i)*time.Second),
			"Recent log for zero quota test",
			map[string]any{
				"test_session": uniqueID,
				"log_type":     "recent",
				"log_index":    50 + i,
			},
		)
		allEntries = logs_core_tests.MergeLogEntries(allEntries, recentLogEntries)
	}

	// Store all logs
	logs_core_tests.StoreTestLogsAndFlush(t, repository, allEntries)

	// Wait for logs to appear
	statsBeforeCleanup := WaitForLogsToAppear(t, repository, project.ID, 75, 30000)
	assert.Equal(t, int64(75), statsBeforeCleanup.TotalLogs, "Should have 75 logs before cleanup")

	t.Logf(
		"Before cleanup: TotalLogs=%d, OldestTime=%v, NewestTime=%v",
		statsBeforeCleanup.TotalLogs,
		statsBeforeCleanup.OldestLogTime,
		statsBeforeCleanup.NewestLogTime,
	)

	// Execute cleanup service
	err := cleanupService.ExecuteAllTasksForTest()
	assert.NoError(t, err, "Cleanup service should execute successfully")

	// Wait for any operations to complete (should remain 75)
	statsAfterCleanup := WaitForLogDeletion(t, repository, project.ID, 75, 30000)

	t.Logf("After cleanup: TotalLogs=%d, OldestTime=%v, NewestTime=%v",
		statsAfterCleanup.TotalLogs, statsAfterCleanup.OldestLogTime, statsAfterCleanup.NewestLogTime)

	assert.Equal(
		t,
		statsBeforeCleanup.TotalLogs,
		statsAfterCleanup.TotalLogs,
		"No logs should be deleted with zero count quota",
	)
}

func Test_EnforceProjectQuotas_WithDifferentProjectsCountQuotas_DeletesOnlyTargetProjectLogs(t *testing.T) {
	users_testing.CleanupPlans()
	// No uploader here, so these segments stay pending upload: keep other
	// packages' global-queue resets out for the whole test.
	test_utils.AcquireExclusiveGlobalQueue(t, "logs-pending-queue")

	router := projects_testing.CreateTestRouter(
		projects_controllers.GetProjectController(),
		projects_controllers.GetMembershipController(),
	)

	owner1 := users_testing.CreateTestUser(users_enums.UserRoleMember)
	owner2 := users_testing.CreateTestUser(users_enums.UserRoleMember)
	uniqueID1 := uuid.New().String()[:8]
	uniqueID2 := uuid.New().String()[:8]

	// Create test projects
	project1Name := "Project1 Different Count Quota Test " + uniqueID1
	project2Name := "Project2 Different Count Quota Test " + uniqueID2

	project1 := projects_testing.CreateTestProject(project1Name, owner1, router)
	project2 := projects_testing.CreateTestProject(project2Name, owner2, router)

	// Set different MaxLogsAmount for each project
	// Project 1: 10 logs quota (will exceed and trigger cleanup)
	updateData1 := &projects_models.Project{
		Name:                  project1.Name,
		IsRateLimitingEnabled: true,
		MaxLogsAmount:         10, // 10 logs limit - will be exceeded
	}
	projects_testing.UpdateProject(project1, updateData1, owner1.Token, router)

	// Project 2: 100 logs quota (will NOT exceed)
	updateData2 := &projects_models.Project{
		Name:                  project2.Name,
		IsRateLimitingEnabled: true,
		MaxLogsAmount:         100, // 100 logs limit - will not be exceeded
	}
	projects_testing.UpdateProject(project2, updateData2, owner2.Token, router)

	// Get repository and cleanup service
	repository := logs_core.GetLogCoreRepository()
	cleanupService := logs_cleanup.GetLogCleanupBackgroundService()

	// Create test timestamps
	now := time.Now().UTC()
	oldTime := now.Add(-2 * time.Hour)       // 2 hours ago
	recentTime := now.Add(-30 * time.Minute) // 30 minutes ago

	// Create logs for Project 1 - exceed 10 log quota
	var project1Entries map[uuid.UUID][]*logs_core.LogItem

	// Create 10 old logs for project 1
	for i := range 10 {
		oldLogEntries := logs_core_tests.CreateTestLogEntriesWithUniqueFields(
			project1.ID,
			oldTime.Add(time.Duration(i)*time.Second),
			"Project1 old log for different count quota test",
			map[string]any{
				"test_session": uniqueID1,
				"log_type":     "old",
				"project_name": project1Name,
				"log_index":    i,
			},
		)
		if project1Entries == nil {
			project1Entries = oldLogEntries
		} else {
			project1Entries = logs_core_tests.MergeLogEntries(project1Entries, oldLogEntries)
		}
	}

	// Create 8 recent logs for project 1 - Total: 18 logs, exceeds 10 log quota
	for i := range 8 {
		recentLogEntries := logs_core_tests.CreateTestLogEntriesWithUniqueFields(
			project1.ID,
			recentTime.Add(time.Duration(i)*time.Second),
			"Project1 recent log for different count quota test",
			map[string]any{
				"test_session": uniqueID1,
				"log_type":     "recent",
				"project_name": project1Name,
				"log_index":    10 + i,
			},
		)
		project1Entries = logs_core_tests.MergeLogEntries(project1Entries, recentLogEntries)
	}

	// Create logs for Project 2 (will NOT exceed 100 log quota)
	var project2Entries map[uuid.UUID][]*logs_core.LogItem

	// Create 25 logs for project 2 (well below 100 log limit)
	for i := range 25 {
		logEntries := logs_core_tests.CreateTestLogEntriesWithUniqueFields(
			project2.ID,
			oldTime.Add(time.Duration(i)*time.Second),
			"Project2 log for different count quota test",
			map[string]any{
				"test_session": uniqueID2,
				"log_type":     "normal",
				"project_name": project2Name,
				"log_index":    i,
			},
		)
		if project2Entries == nil {
			project2Entries = logEntries
		} else {
			project2Entries = logs_core_tests.MergeLogEntries(project2Entries, logEntries)
		}
	}

	// Store all logs for both projects
	logs_core_tests.StoreTestLogsAndFlush(t, repository, project1Entries)
	logs_core_tests.StoreTestLogsAndFlush(t, repository, project2Entries)

	// Wait for logs to appear for both projects
	project1StatsBeforeCleanup := WaitForLogsToAppear(t, repository, project1.ID, 18, 30000)
	assert.Equal(t, int64(18), project1StatsBeforeCleanup.TotalLogs, "Project1 should have 18 logs before cleanup")
	assert.Greater(t, project1StatsBeforeCleanup.TotalLogs, int64(10), "Project1 should exceed 10 log quota")

	t.Logf(
		"Project1 stats before cleanup: TotalLogs=%d, OldestTime=%v, NewestTime=%v",
		project1StatsBeforeCleanup.TotalLogs,
		project1StatsBeforeCleanup.OldestLogTime,
		project1StatsBeforeCleanup.NewestLogTime,
	)

	project2StatsBeforeCleanup := WaitForLogsToAppear(t, repository, project2.ID, 25, 30000)
	assert.Equal(t, int64(25), project2StatsBeforeCleanup.TotalLogs, "Project2 should have 25 logs before cleanup")
	assert.Less(t, project2StatsBeforeCleanup.TotalLogs, int64(100), "Project2 should be well below 100 log quota")

	t.Logf(
		"Project2 stats before cleanup: TotalLogs=%d, OldestTime=%v, NewestTime=%v",
		project2StatsBeforeCleanup.TotalLogs,
		project2StatsBeforeCleanup.OldestLogTime,
		project2StatsBeforeCleanup.NewestLogTime,
	)

	// Execute cleanup service
	err := cleanupService.ExecuteAllTasksForTest()
	assert.NoError(t, err, "Cleanup service should execute successfully")

	// Wait for delete operations to complete for both projects
	project1StatsAfterCleanup := WaitForLogDeletionWithMaxCount(t, repository, project1.ID, 10, 30000)
	t.Logf(
		"Project1 stats after cleanup: TotalLogs=%d, OldestTime=%v, NewestTime=%v",
		project1StatsAfterCleanup.TotalLogs,
		project1StatsAfterCleanup.OldestLogTime,
		project1StatsAfterCleanup.NewestLogTime,
	)
	assert.Less(
		t,
		project1StatsAfterCleanup.TotalLogs,
		project1StatsBeforeCleanup.TotalLogs,
		"Project1 should have fewer logs after cleanup (quota exceeded)",
	)
	assert.LessOrEqual(
		t,
		project1StatsAfterCleanup.TotalLogs,
		int64(10), // Should be reduced to or below the quota
		"Project1 log count should be reduced after cleanup",
	)

	// Wait for project 2 (should remain unchanged)
	project2StatsAfterCleanup := WaitForLogDeletion(t, repository, project2.ID, 25, 30000)
	t.Logf(
		"Project2 stats after cleanup: TotalLogs=%d, OldestTime=%v, NewestTime=%v",
		project2StatsAfterCleanup.TotalLogs,
		project2StatsAfterCleanup.OldestLogTime,
		project2StatsAfterCleanup.NewestLogTime,
	)
	assert.Equal(
		t,
		project2StatsBeforeCleanup.TotalLogs,
		project2StatsAfterCleanup.TotalLogs,
		"Project2 logs should remain unchanged (quota not exceeded)",
	)
}

func Test_EnforceProjectQuotas_WhenLogsCreatedWithNanosecondPrecision_KeepsNewestLogs(t *testing.T) {
	t.Skip(
		"Known limitation, not a regression - see ARCHITECTURE.md's " +
			"\"Known limitation: sub-microsecond query-time precision\" section: " +
			"the stream_segments catalog's min_time/max_time are Postgres " +
			"TIMESTAMPTZ (microsecond precision), so segment-level pruning can't " +
			"distinguish rows within the same microsecond even though the " +
			"underlying Parquet data is true nanosecond precision.",
	)

	users_testing.CleanupPlans()
	// No uploader here, so these segments stay pending upload: keep other
	// packages' global-queue resets out for the whole test.
	test_utils.AcquireExclusiveGlobalQueue(t, "logs-pending-queue")

	router := projects_testing.CreateTestRouter(
		projects_controllers.GetProjectController(),
		projects_controllers.GetMembershipController(),
	)
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	uniqueID := uuid.New().String()[:8]

	projectName := "Nanosecond Precision Test " + uniqueID
	project := projects_testing.CreateTestProject(projectName, owner, router)

	updateData := &projects_models.Project{
		Name:                  project.Name,
		IsRateLimitingEnabled: true,
		MaxLogsAmount:         10,
	}
	updatedProject := projects_testing.UpdateProject(project, updateData, owner.Token, router)
	project = updatedProject

	t.Logf("Project MaxLogsAmount after update: %d", project.MaxLogsAmount)

	repository := logs_core.GetLogCoreRepository()
	cleanupService := logs_cleanup.GetLogCleanupBackgroundService()

	now := time.Now().UTC()
	var allEntries map[uuid.UUID][]*logs_core.LogItem

	for i := range 15 {
		logEntries := logs_core_tests.CreateTestLogEntriesWithUniqueFields(
			project.ID,
			now.Add(time.Duration(i)*10*time.Nanosecond),
			"Log with nanosecond precision",
			map[string]any{
				"test_session": uniqueID,
				"log_index":    i,
			},
		)
		if allEntries == nil {
			allEntries = logEntries
		} else {
			allEntries = logs_core_tests.MergeLogEntries(allEntries, logEntries)
		}
	}

	logs_core_tests.StoreTestLogsAndFlush(t, repository, allEntries)

	statsBeforeCleanup := WaitForLogsToAppear(t, repository, project.ID, 15, 30000)
	assert.Equal(t, int64(15), statsBeforeCleanup.TotalLogs, "Should have 15 logs before cleanup")

	t.Logf(
		"Before cleanup: TotalLogs=%d, OldestTime=%v, NewestTime=%v",
		statsBeforeCleanup.TotalLogs,
		statsBeforeCleanup.OldestLogTime,
		statsBeforeCleanup.NewestLogTime,
	)

	err := cleanupService.ExecuteAllTasksForTest()
	assert.NoError(t, err, "Cleanup service should execute successfully")

	targetLogs := int64(10)
	statsAfterCleanup := WaitForLogDeletion(t, repository, project.ID, targetLogs, 30000)

	t.Logf("After cleanup: TotalLogs=%d, OldestTime=%v, NewestTime=%v",
		statsAfterCleanup.TotalLogs, statsAfterCleanup.OldestLogTime, statsAfterCleanup.NewestLogTime)

	assert.Equal(
		t,
		targetLogs,
		statsAfterCleanup.TotalLogs,
		"Should have exactly 10 logs after cleanup - exactly 5 logs deleted (indices 0-4)",
	)

	expectedOldestRemainingTime := now.Add(time.Duration(5) * 10 * time.Nanosecond)
	expectedNewestTime := now.Add(time.Duration(14) * 10 * time.Nanosecond)

	assert.True(
		t,
		statsAfterCleanup.OldestLogTime.Sub(expectedOldestRemainingTime).Abs() < 1*time.Microsecond,
		"Oldest remaining log should be around index 5 (time: %v, expected: %v)",
		statsAfterCleanup.OldestLogTime,
		expectedOldestRemainingTime,
	)

	assert.True(
		t,
		statsAfterCleanup.NewestLogTime.Sub(expectedNewestTime).Abs() < 1*time.Microsecond,
		"Newest log should be around index 14 (time: %v, expected: %v)",
		statsAfterCleanup.NewestLogTime,
		expectedNewestTime,
	)
}

func Test_EnforceProjectQuotas_WhenLogsCreatedWithinSameNanosecond_CannotDeleteLogs(t *testing.T) {
	t.Skip(
		"Known limitation, not a regression - see ARCHITECTURE.md's " +
			"\"Known limitation: sub-microsecond query-time precision\" section: " +
			"the stream_segments catalog's min_time/max_time are Postgres " +
			"TIMESTAMPTZ (microsecond precision), so segment-level pruning can't " +
			"distinguish rows within the same microsecond even though the " +
			"underlying Parquet data is true nanosecond precision.",
	)

	users_testing.CleanupPlans()
	// No uploader here, so these segments stay pending upload: keep other
	// packages' global-queue resets out for the whole test.
	test_utils.AcquireExclusiveGlobalQueue(t, "logs-pending-queue")

	router := projects_testing.CreateTestRouter(
		projects_controllers.GetProjectController(),
		projects_controllers.GetMembershipController(),
	)
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	uniqueID := uuid.New().String()[:8]

	projectName := "Same Nanosecond Test " + uniqueID
	project := projects_testing.CreateTestProject(projectName, owner, router)

	updateData := &projects_models.Project{
		Name:                  project.Name,
		IsRateLimitingEnabled: true,
		MaxLogsAmount:         10,
	}
	updatedProject := projects_testing.UpdateProject(project, updateData, owner.Token, router)
	project = updatedProject

	t.Logf("Project MaxLogsAmount after update: %d", project.MaxLogsAmount)

	repository := logs_core.GetLogCoreRepository()
	cleanupService := logs_cleanup.GetLogCleanupBackgroundService()

	now := time.Now().UTC()
	var allEntries map[uuid.UUID][]*logs_core.LogItem

	for i := range 15 {
		logEntries := logs_core_tests.CreateTestLogEntriesWithUniqueFields(
			project.ID,
			now,
			"Log within same nanosecond",
			map[string]any{
				"test_session": uniqueID,
				"log_index":    i,
			},
		)
		if allEntries == nil {
			allEntries = logEntries
		} else {
			allEntries = logs_core_tests.MergeLogEntries(allEntries, logEntries)
		}
	}

	logs_core_tests.StoreTestLogsAndFlush(t, repository, allEntries)

	statsBeforeCleanup := WaitForLogsToAppear(t, repository, project.ID, 15, 30000)
	assert.Equal(t, int64(15), statsBeforeCleanup.TotalLogs, "Should have 15 logs before cleanup")
	assert.Equal(
		t,
		statsBeforeCleanup.OldestLogTime,
		statsBeforeCleanup.NewestLogTime,
		"All logs should have the same timestamp",
	)

	t.Logf(
		"Before cleanup: TotalLogs=%d, OldestTime=%v, NewestTime=%v (all same)",
		statsBeforeCleanup.TotalLogs,
		statsBeforeCleanup.OldestLogTime,
		statsBeforeCleanup.NewestLogTime,
	)

	err := cleanupService.ExecuteAllTasksForTest()
	assert.NoError(t, err, "Cleanup service should execute successfully")

	statsAfterCleanup := WaitForLogDeletion(t, repository, project.ID, 15, 5000)

	t.Logf("After cleanup: TotalLogs=%d, OldestTime=%v, NewestTime=%v",
		statsAfterCleanup.TotalLogs, statsAfterCleanup.OldestLogTime, statsAfterCleanup.NewestLogTime)

	assert.Equal(
		t,
		statsBeforeCleanup.TotalLogs,
		statsAfterCleanup.TotalLogs,
		"No logs should be deleted when all logs have identical timestamps - cleanup algorithm cannot calculate proper cutoff time",
	)

	t.Logf(
		"Edge case confirmed: When all logs share the same timestamp, the cleanup algorithm cannot calculate a proper cutoff time, so no logs are deleted despite exceeding the quota",
	)
}
