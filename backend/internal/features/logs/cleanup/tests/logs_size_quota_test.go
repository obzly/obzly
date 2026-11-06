package logs_cleanup_tests

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
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

// randomMessage returns byteLen bytes of high-entropy content: low-entropy
// filler compresses away in Parquet and never trips a size quota.
func randomMessage(t *testing.T, byteLen int) string {
	t.Helper()
	buf := make([]byte, byteLen/2)
	_, err := rand.Read(buf)
	assert.NoError(t, err)
	return hex.EncodeToString(buf)
}

func Test_EnforceProjectQuotas_WhenStorageSizeExceedsMaxLogsSizeMB_DeletesOldestLogs(t *testing.T) {
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
	projectName := "Size Quota Test " + uniqueID
	project := projects_testing.CreateTestProject(projectName, owner, router)

	// Update project to set MaxLogsSizeMB to a small value (1 MB)
	updateData := &projects_models.Project{
		Name:                  project.Name,
		IsRateLimitingEnabled: true,
		MaxLogsSizeMB:         1, // 1 MB limit
	}
	projects_testing.UpdateProject(project, updateData, owner.Token, router)

	// Get repository and cleanup service
	repository := logs_core.GetLogCoreRepository()
	cleanupService := logs_cleanup.GetLogCleanupBackgroundService()

	// Create test timestamps
	now := time.Now().UTC()
	oldTime := now.Add(-2 * time.Hour)       // 2 hours ago (should be deleted)
	recentTime := now.Add(-30 * time.Minute) // 30 minutes ago (should remain)

	// Create logs with large messages to exceed size quota
	largeMessage := randomMessage(t, 6*1024) // ~6KB of high-entropy content per message

	// Create multiple logs to exceed the 1MB limit
	var allEntries map[uuid.UUID][]*logs_core.LogItem

	// Create 200 old logs (~1.2MB total)
	for i := range 200 {
		oldLogEntries := logs_core_tests.CreateTestLogEntriesWithUniqueFields(
			project.ID,
			oldTime.Add(time.Duration(i)*time.Second), // Slightly different timestamps
			largeMessage+fmt.Sprintf(" Log #%d", i),
			map[string]any{
				"test_session": uniqueID,
				"log_type":     "old",
				"size_test":    true,
				"log_index":    i,
			},
		)
		if allEntries == nil {
			allEntries = oldLogEntries
		} else {
			allEntries = logs_core_tests.MergeLogEntries(allEntries, oldLogEntries)
		}
	}

	// Create 50 recent logs (~300KB total)
	for i := 0; i < 50; i++ {
		recentLogEntries := logs_core_tests.CreateTestLogEntriesWithUniqueFields(
			project.ID,
			recentTime.Add(time.Duration(i)*time.Second), // Slightly different timestamps
			largeMessage+fmt.Sprintf(" Recent Log #%d", i),
			map[string]any{
				"test_session": uniqueID,
				"log_type":     "recent",
				"size_test":    true,
				"log_index":    200 + i,
			},
		)
		allEntries = logs_core_tests.MergeLogEntries(allEntries, recentLogEntries)
	}

	// Store all logs
	logs_core_tests.StoreTestLogsAndFlush(t, repository, allEntries)

	// Wait for logs to appear
	statsBeforeCleanup := WaitForLogsToAppear(t, repository, project.ID, 250, 30000)
	assert.Equal(t, int64(250), statsBeforeCleanup.TotalLogs, "Should have 250 logs before cleanup")
	assert.Greater(t, statsBeforeCleanup.TotalSizeMB, 1.0, "Should exceed 1MB quota before cleanup")

	t.Logf(
		"Before cleanup: TotalLogs=%d, TotalSizeMB=%.3f",
		statsBeforeCleanup.TotalLogs,
		statsBeforeCleanup.TotalSizeMB,
	)

	// Execute cleanup service
	err := cleanupService.ExecuteAllTasksForTest()
	assert.NoError(t, err, "Cleanup service should execute successfully")

	// Wait for size quota enforcement (wait for reduction below 1MB)
	statsAfterCleanup := WaitForLogDeletionWithMaxCount(
		t,
		repository,
		project.ID,
		statsBeforeCleanup.TotalLogs-1,
		30000,
	)

	t.Logf("After cleanup: TotalLogs=%d, TotalSizeMB=%.3f", statsAfterCleanup.TotalLogs, statsAfterCleanup.TotalSizeMB)

	assert.LessOrEqual(t, statsAfterCleanup.TotalSizeMB, 0.9, "Size should be reduced to ~90% of quota (0.9MB)")
	assert.Less(t, statsAfterCleanup.TotalLogs, statsBeforeCleanup.TotalLogs, "Should have fewer logs after cleanup")

	// Verify the remaining logs are primarily the recent ones
	if !statsAfterCleanup.OldestLogTime.IsZero() && !statsAfterCleanup.NewestLogTime.IsZero() {
		// Most remaining logs should be from the recent time period
		assert.True(t, statsAfterCleanup.OldestLogTime.After(oldTime.Add(30*time.Minute)),
			"Remaining logs should be mostly from the recent time period")
	}
}

func Test_EnforceProjectQuotas_WhenStorageSizeIsWithinMaxLogsSizeMB_NoLogsDeleted(t *testing.T) {
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
	projectName := "Size Within Quota Test " + uniqueID
	project := projects_testing.CreateTestProject(projectName, owner, router)

	// Update project to set MaxLogsSizeMB to a large value (10 MB)
	updateData := &projects_models.Project{
		Name:                  project.Name,
		IsRateLimitingEnabled: true,
		MaxLogsSizeMB:         10, // 10 MB limit - large enough to not trigger cleanup
	}
	projects_testing.UpdateProject(project, updateData, owner.Token, router)

	// Get repository and cleanup service
	repository := logs_core.GetLogCoreRepository()
	cleanupService := logs_cleanup.GetLogCleanupBackgroundService()

	// Create test timestamps
	now := time.Now().UTC()
	oldTime := now.Add(-2 * time.Hour)       // 2 hours ago
	recentTime := now.Add(-30 * time.Minute) // 30 minutes ago

	// Create small logs that won't exceed quota
	smallMessage := strings.Repeat("Small log message. ", 10) // ~200 bytes per message

	// Create only 50 logs (~10KB total, well below 10MB limit)
	var allEntries map[uuid.UUID][]*logs_core.LogItem

	for i := range 25 {
		oldLogEntries := logs_core_tests.CreateTestLogEntriesWithUniqueFields(
			project.ID,
			oldTime.Add(time.Duration(i)*time.Second),
			smallMessage+fmt.Sprintf(" Old Log #%d", i),
			map[string]any{
				"test_session": uniqueID,
				"log_type":     "old",
				"size_test":    true,
				"log_index":    i,
			},
		)
		if allEntries == nil {
			allEntries = oldLogEntries
		} else {
			allEntries = logs_core_tests.MergeLogEntries(allEntries, oldLogEntries)
		}
	}

	for i := range 25 {
		recentLogEntries := logs_core_tests.CreateTestLogEntriesWithUniqueFields(
			project.ID,
			recentTime.Add(time.Duration(i)*time.Second),
			smallMessage+fmt.Sprintf(" Recent Log #%d", i),
			map[string]any{
				"test_session": uniqueID,
				"log_type":     "recent",
				"size_test":    true,
				"log_index":    25 + i,
			},
		)
		allEntries = logs_core_tests.MergeLogEntries(allEntries, recentLogEntries)
	}

	// Store all logs
	logs_core_tests.StoreTestLogsAndFlush(t, repository, allEntries)

	// Wait for logs to appear
	statsBeforeCleanup := WaitForLogsToAppear(t, repository, project.ID, 50, 30000)
	assert.Equal(t, int64(50), statsBeforeCleanup.TotalLogs, "Should have 50 logs before cleanup")
	assert.Less(t, statsBeforeCleanup.TotalSizeMB, 1.0, "Should be well below 10MB quota before cleanup")

	t.Logf(
		"Before cleanup: TotalLogs=%d, TotalSizeMB=%.3f",
		statsBeforeCleanup.TotalLogs,
		statsBeforeCleanup.TotalSizeMB,
	)

	// Execute cleanup service
	err := cleanupService.ExecuteAllTasksForTest()
	assert.NoError(t, err, "Cleanup service should execute successfully")

	// Wait for any operations to complete (should remain 50)
	statsAfterCleanup := WaitForLogDeletion(t, repository, project.ID, 50, 30000)

	t.Logf("After cleanup: TotalLogs=%d, TotalSizeMB=%.3f", statsAfterCleanup.TotalLogs, statsAfterCleanup.TotalSizeMB)

	assert.Equal(
		t,
		statsBeforeCleanup.TotalLogs,
		statsAfterCleanup.TotalLogs,
		"No logs should be deleted when within quota",
	)
	// LessOrEqual: ExecuteAllTasksForTest also compacts, which can shrink
	// on-disk size with no quota deletion.
	assert.LessOrEqual(
		t,
		statsAfterCleanup.TotalSizeMB,
		statsBeforeCleanup.TotalSizeMB,
		"Size should not increase when within quota",
	)
}

func Test_EnforceProjectQuotas_WhenMaxLogsSizeMBIsZero_NoSizeQuotaEnforcement(t *testing.T) {
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
	projectName := "Zero Size Quota Test " + uniqueID
	project := projects_testing.CreateTestProject(projectName, owner, router)

	// Update project to set MaxLogsSizeMB to 0 (no size-based quota)
	updateData := &projects_models.Project{
		Name:                  project.Name,
		IsRateLimitingEnabled: true,
		MaxLogsSizeMB:         0, // No size quota enforcement
	}
	projects_testing.UpdateProject(project, updateData, owner.Token, router)

	// Get repository and cleanup service
	repository := logs_core.GetLogCoreRepository()
	cleanupService := logs_cleanup.GetLogCleanupBackgroundService()

	// Create test timestamps
	now := time.Now().UTC()
	oldTime := now.Add(-2 * time.Hour)       // 2 hours ago
	recentTime := now.Add(-30 * time.Minute) // 30 minutes ago

	// Create large logs that would normally trigger cleanup
	largeMessage := randomMessage(t, 6*1024) // ~6KB of high-entropy content per message

	// Create many logs that would exceed any reasonable size limit
	var allEntries map[uuid.UUID][]*logs_core.LogItem

	// Create 100 old logs (~600KB total - would trigger cleanup if quota was set)
	for i := range 100 {
		oldLogEntries := logs_core_tests.CreateTestLogEntriesWithUniqueFields(
			project.ID,
			oldTime.Add(time.Duration(i)*time.Second),
			largeMessage+fmt.Sprintf(" Old Log #%d", i),
			map[string]any{
				"test_session": uniqueID,
				"log_type":     "old",
				"size_test":    true,
				"log_index":    i,
			},
		)
		if allEntries == nil {
			allEntries = oldLogEntries
		} else {
			allEntries = logs_core_tests.MergeLogEntries(allEntries, oldLogEntries)
		}
	}

	// Create 50 recent logs (~300KB total)
	for i := range 50 {
		recentLogEntries := logs_core_tests.CreateTestLogEntriesWithUniqueFields(
			project.ID,
			recentTime.Add(time.Duration(i)*time.Second),
			largeMessage+fmt.Sprintf(" Recent Log #%d", i),
			map[string]any{
				"test_session": uniqueID,
				"log_type":     "recent",
				"size_test":    true,
				"log_index":    100 + i,
			},
		)
		allEntries = logs_core_tests.MergeLogEntries(allEntries, recentLogEntries)
	}

	// Store all logs
	logs_core_tests.StoreTestLogsAndFlush(t, repository, allEntries)

	// Wait for logs to appear
	statsBeforeCleanup := WaitForLogsToAppear(t, repository, project.ID, 150, 30000)
	assert.Equal(t, int64(150), statsBeforeCleanup.TotalLogs, "Should have 150 logs before cleanup")

	t.Logf(
		"Before cleanup: TotalLogs=%d, TotalSizeMB=%.3f",
		statsBeforeCleanup.TotalLogs,
		statsBeforeCleanup.TotalSizeMB,
	)

	// Execute cleanup service
	err := cleanupService.ExecuteAllTasksForTest()
	assert.NoError(t, err, "Cleanup service should execute successfully")

	// Wait for any operations to complete (should remain 150)
	statsAfterCleanup := WaitForLogDeletion(t, repository, project.ID, 150, 30000)

	t.Logf("After cleanup: TotalLogs=%d, TotalSizeMB=%.3f", statsAfterCleanup.TotalLogs, statsAfterCleanup.TotalSizeMB)

	assert.Equal(
		t,
		statsBeforeCleanup.TotalLogs,
		statsAfterCleanup.TotalLogs,
		"No logs should be deleted with zero size quota",
	)
	// LessOrEqual: compaction can shrink on-disk size (see above).
	assert.LessOrEqual(
		t,
		statsAfterCleanup.TotalSizeMB,
		statsBeforeCleanup.TotalSizeMB,
		"Size should not increase with zero size quota",
	)
}

func Test_EnforceProjectQuotas_WhenStorageSizeExceedsQuota_DeletesToNinetyPercentOfLimit(t *testing.T) {
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
	projectName := "Exceeds Quota Cleanup Test " + uniqueID
	project := projects_testing.CreateTestProject(projectName, owner, router)

	// Update project to set MaxLogsSizeMB to 1 MB (small quota)
	// According to calculateCleanupPercentage, quotas <= 10MB target 85%
	updateData := &projects_models.Project{
		Name:                  project.Name,
		IsRateLimitingEnabled: true,
		MaxLogsSizeMB:         1, // 1 MB limit, should clean up to 85% = 0.85MB
	}
	projects_testing.UpdateProject(project, updateData, owner.Token, router)

	// Get repository and cleanup service
	repository := logs_core.GetLogCoreRepository()
	cleanupService := logs_cleanup.GetLogCleanupBackgroundService()

	// Create test timestamps
	now := time.Now().UTC()
	oldTime := now.Add(-2 * time.Hour)       // 2 hours ago (will be deleted)
	recentTime := now.Add(-30 * time.Minute) // 30 minutes ago (may remain)

	// Create logs with large messages to exceed size quota
	largeMessage := randomMessage(t, 6*1024) // ~6KB of high-entropy content per message

	// Create logs to exceed the 1MB limit significantly
	var allEntries map[uuid.UUID][]*logs_core.LogItem

	// Create 150 old logs (~900KB total)
	for i := range 150 {
		oldLogEntries := logs_core_tests.CreateTestLogEntriesWithUniqueFields(
			project.ID,
			oldTime.Add(time.Duration(i)*time.Second),
			largeMessage+fmt.Sprintf(" Old Log #%d", i),
			map[string]any{
				"test_session": uniqueID,
				"log_type":     "old",
				"size_test":    true,
				"log_index":    i,
			},
		)
		if allEntries == nil {
			allEntries = oldLogEntries
		} else {
			allEntries = logs_core_tests.MergeLogEntries(allEntries, oldLogEntries)
		}
	}

	// Create 75 recent logs (~450KB total)
	// Total: ~1.35MB, significantly exceeding 1MB quota
	for i := range 75 {
		recentLogEntries := logs_core_tests.CreateTestLogEntriesWithUniqueFields(
			project.ID,
			recentTime.Add(time.Duration(i)*time.Second),
			largeMessage+fmt.Sprintf(" Recent Log #%d", i),
			map[string]any{
				"test_session": uniqueID,
				"log_type":     "recent",
				"size_test":    true,
				"log_index":    150 + i,
			},
		)
		allEntries = logs_core_tests.MergeLogEntries(allEntries, recentLogEntries)
	}

	// Store all logs
	logs_core_tests.StoreTestLogsAndFlush(t, repository, allEntries)

	// Wait for logs to appear
	statsBeforeCleanup := WaitForLogsToAppear(t, repository, project.ID, 225, 30000)
	assert.Equal(t, int64(225), statsBeforeCleanup.TotalLogs, "Should have 225 logs before cleanup")
	assert.Greater(t, statsBeforeCleanup.TotalSizeMB, 1.0, "Should exceed 1MB quota before cleanup")

	t.Logf(
		"Before cleanup: TotalLogs=%d, TotalSizeMB=%.3f",
		statsBeforeCleanup.TotalLogs,
		statsBeforeCleanup.TotalSizeMB,
	)

	// Execute cleanup service
	err := cleanupService.ExecuteAllTasksForTest()
	assert.NoError(t, err, "Cleanup service should execute successfully")

	// Wait for delete operations to complete (wait for reduction below initial count)
	statsAfterCleanup := WaitForLogDeletionWithMaxCount(
		t,
		repository,
		project.ID,
		statsBeforeCleanup.TotalLogs-1,
		30000,
	)

	t.Logf("After cleanup: TotalLogs=%d, TotalSizeMB=%.3f", statsAfterCleanup.TotalLogs, statsAfterCleanup.TotalSizeMB)

	// 1MB quota targets 85%; cutoff by log distribution may delete more.
	expectedMaxSizeMB := 0.85

	assert.Less(t, statsAfterCleanup.TotalLogs, statsBeforeCleanup.TotalLogs, "Should have fewer logs after cleanup")
	assert.LessOrEqual(t, statsAfterCleanup.TotalSizeMB, expectedMaxSizeMB+0.1,
		fmt.Sprintf("Size should be reduced to at most %.2fMB (85%% of 1MB quota)", expectedMaxSizeMB))
	// No lower size bound: the compaction that follows shrinks the footprint
	// further, so row count is the reliable signal.

	// Most importantly, verify that cleanup happened and size was significantly reduced
	assert.Less(t, statsAfterCleanup.TotalSizeMB, 1.0, "Size should be below the 1MB quota after cleanup")
	assert.Less(t, statsAfterCleanup.TotalSizeMB, statsBeforeCleanup.TotalSizeMB*0.7,
		"Size should be reduced by at least 30% to demonstrate quota enforcement")

	// Verify the remaining logs are primarily the recent ones
	if !statsAfterCleanup.OldestLogTime.IsZero() && !statsAfterCleanup.NewestLogTime.IsZero() {
		// Most remaining logs should be from the recent time period or close to it
		assert.True(t, statsAfterCleanup.OldestLogTime.After(oldTime.Add(30*time.Minute)),
			"Remaining logs should be mostly from the recent time period after cleanup")
	}
}

func Test_EnforceProjectQuotas_WithDifferentProjectsSizeQuotas_DeletesOnlyTargetProjectLogs(t *testing.T) {
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
	project1Name := "Project1 Different Size Quota Test " + uniqueID1
	project2Name := "Project2 Different Size Quota Test " + uniqueID2

	project1 := projects_testing.CreateTestProject(project1Name, owner1, router)
	project2 := projects_testing.CreateTestProject(project2Name, owner2, router)

	// Set different MaxLogsSizeMB for each project
	// Project 1: 1 MB quota (will exceed and trigger cleanup)
	updateData1 := &projects_models.Project{
		Name:                  project1.Name,
		IsRateLimitingEnabled: true,
		MaxLogsSizeMB:         1, // 1 MB limit - will be exceeded
	}
	projects_testing.UpdateProject(project1, updateData1, owner1.Token, router)

	// Project 2: 10 MB quota (will NOT exceed)
	updateData2 := &projects_models.Project{
		Name:                  project2.Name,
		IsRateLimitingEnabled: true,
		MaxLogsSizeMB:         10, // 10 MB limit - will not be exceeded
	}
	projects_testing.UpdateProject(project2, updateData2, owner2.Token, router)

	// Get repository and cleanup service
	repository := logs_core.GetLogCoreRepository()
	cleanupService := logs_cleanup.GetLogCleanupBackgroundService()

	// Create test timestamps
	now := time.Now().UTC()
	oldTime := now.Add(-2 * time.Hour)       // 2 hours ago
	recentTime := now.Add(-30 * time.Minute) // 30 minutes ago

	// Create large logs for Project 1 (will exceed 1MB quota)
	largeMessage := randomMessage(t, 7*1024) // ~7KB of high-entropy content per message

	// Create logs for Project 1 - exceed 1MB quota
	var project1Entries map[uuid.UUID][]*logs_core.LogItem

	// Create 100 old logs for project 1 (~700KB)
	for i := range 100 {
		oldLogEntries := logs_core_tests.CreateTestLogEntriesWithUniqueFields(
			project1.ID,
			oldTime.Add(time.Duration(i)*time.Second),
			largeMessage+fmt.Sprintf(" Project1 Old Log #%d", i),
			map[string]any{
				"test_session": uniqueID1,
				"log_type":     "old",
				"size_test":    true,
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

	// Create 50 recent logs for project 1 (~350KB) - Total: ~1.05MB, exceeds 1MB
	for i := range 50 {
		recentLogEntries := logs_core_tests.CreateTestLogEntriesWithUniqueFields(
			project1.ID,
			recentTime.Add(time.Duration(i)*time.Second),
			largeMessage+fmt.Sprintf(" Project1 Recent Log #%d", i),
			map[string]any{
				"test_session": uniqueID1,
				"log_type":     "recent",
				"size_test":    true,
				"project_name": project1Name,
				"log_index":    100 + i,
			},
		)
		project1Entries = logs_core_tests.MergeLogEntries(project1Entries, recentLogEntries)
	}

	// Create smaller logs for Project 2 (will NOT exceed 10MB quota)
	smallMessage := strings.Repeat("Small message for project 2. ", 10) // ~300 bytes per message

	var project2Entries map[uuid.UUID][]*logs_core.LogItem

	// Create 50 logs for project 2 (~15KB total, well below 10MB)
	for i := range 50 {
		logEntries := logs_core_tests.CreateTestLogEntriesWithUniqueFields(
			project2.ID,
			oldTime.Add(time.Duration(i)*time.Second),
			smallMessage+fmt.Sprintf(" Project2 Log #%d", i),
			map[string]any{
				"test_session": uniqueID2,
				"log_type":     "normal",
				"size_test":    true,
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
	project1StatsBeforeCleanup := WaitForLogsToAppear(t, repository, project1.ID, 150, 30000)
	t.Logf(
		"Project1 stats before cleanup: TotalLogs=%d, TotalSizeMB=%.3f",
		project1StatsBeforeCleanup.TotalLogs,
		project1StatsBeforeCleanup.TotalSizeMB,
	)
	assert.Equal(t, int64(150), project1StatsBeforeCleanup.TotalLogs, "Project1 should have 150 logs before cleanup")
	assert.Greater(t, project1StatsBeforeCleanup.TotalSizeMB, 1.0, "Project1 should exceed 1MB quota")

	project2StatsBeforeCleanup := WaitForLogsToAppear(t, repository, project2.ID, 50, 30000)
	t.Logf(
		"Project2 stats before cleanup: TotalLogs=%d, TotalSizeMB=%.3f",
		project2StatsBeforeCleanup.TotalLogs,
		project2StatsBeforeCleanup.TotalSizeMB,
	)
	assert.Equal(t, int64(50), project2StatsBeforeCleanup.TotalLogs, "Project2 should have 50 logs before cleanup")
	assert.Less(t, project2StatsBeforeCleanup.TotalSizeMB, 1.0, "Project2 should be well below 10MB quota")

	// Execute cleanup service
	err := cleanupService.ExecuteAllTasksForTest()
	assert.NoError(t, err, "Cleanup service should execute successfully")

	// Wait for delete operations to complete for both projects
	project1StatsAfterCleanup := WaitForLogDeletionWithMaxCount(
		t,
		repository,
		project1.ID,
		project1StatsBeforeCleanup.TotalLogs-1,
		30000,
	)
	t.Logf(
		"Project1 stats after cleanup: TotalLogs=%d, TotalSizeMB=%.3f",
		project1StatsAfterCleanup.TotalLogs,
		project1StatsAfterCleanup.TotalSizeMB,
	)
	assert.Less(
		t,
		project1StatsAfterCleanup.TotalLogs,
		project1StatsBeforeCleanup.TotalLogs,
		"Project1 should have fewer logs after cleanup (quota exceeded)",
	)
	assert.LessOrEqual(
		t,
		project1StatsAfterCleanup.TotalSizeMB,
		0.95, // Should be reduced to ~85% of 1MB = 0.85MB, plus some tolerance
		"Project1 size should be reduced after cleanup",
	)

	// Verify Project 2 has unchanged logs (did not exceed quota)
	project2StatsAfterCleanup := WaitForLogDeletion(t, repository, project2.ID, 50, 30000)
	t.Logf(
		"Project2 stats after cleanup: TotalLogs=%d, TotalSizeMB=%.3f",
		project2StatsAfterCleanup.TotalLogs,
		project2StatsAfterCleanup.TotalSizeMB,
	)
	assert.Equal(
		t,
		project2StatsBeforeCleanup.TotalLogs,
		project2StatsAfterCleanup.TotalLogs,
		"Project2 logs should remain unchanged (quota not exceeded)",
	)
	// LessOrEqual: compaction can shrink on-disk size (see above).
	assert.LessOrEqual(
		t,
		project2StatsAfterCleanup.TotalSizeMB,
		project2StatsBeforeCleanup.TotalSizeMB,
		"Project2 size should not increase (quota not exceeded)",
	)
}
