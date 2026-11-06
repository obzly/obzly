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

// Test_EnforceProjectQuotas_WithRateLimitingDisabled_SkipsQuotasButKeepsRetention:
// turning off rate limiting & quotas stops count/size quota deletion, but log
// retention (MaxLogsLifeDays) still applies.
func Test_EnforceProjectQuotas_WithRateLimitingDisabled_SkipsQuotasButKeepsRetention(t *testing.T) {
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

	projectName := "Rate Limiting Toggle Test " + uniqueID
	project := projects_testing.CreateTestProject(projectName, owner, router)

	// MaxLogsAmount would trigger count cleanup and MaxLogsLifeDays would
	// delete the old logs; only IsRateLimitingEnabled false differs.
	updateData := &projects_models.Project{
		Name:                  project.Name,
		IsRateLimitingEnabled: false,
		MaxLogsAmount:         2, // far below the 5 recent logs seeded below
		MaxLogsLifeDays:       5, // the 2 old logs below are 10 days old
	}
	projects_testing.UpdateProject(project, updateData, owner.Token, router)

	repository := logs_core.GetLogCoreRepository()
	cleanupService := logs_cleanup.GetLogCleanupBackgroundService()

	now := time.Now().UTC()
	oldTime := now.AddDate(0, 0, -10)     // older than MaxLogsLifeDays=5 - retention should delete these regardless
	recentTime := now.Add(-1 * time.Hour) // within retention, but count exceeds MaxLogsAmount=2

	var allEntries map[uuid.UUID][]*logs_core.LogItem
	for i := range 2 {
		entries := logs_core_tests.CreateTestLogEntriesWithUniqueFields(
			project.ID, oldTime.Add(time.Duration(i)*time.Second),
			"Old log for rate-limiting-toggle test",
			map[string]any{"test_session": uniqueID, "log_type": "old", "log_index": i},
		)
		if allEntries == nil {
			allEntries = entries
		} else {
			allEntries = logs_core_tests.MergeLogEntries(allEntries, entries)
		}
	}
	for i := range 5 {
		entries := logs_core_tests.CreateTestLogEntriesWithUniqueFields(
			project.ID, recentTime.Add(time.Duration(i)*time.Second),
			"Recent log for rate-limiting-toggle test",
			map[string]any{"test_session": uniqueID, "log_type": "recent", "log_index": 2 + i},
		)
		allEntries = logs_core_tests.MergeLogEntries(allEntries, entries)
	}

	logs_core_tests.StoreTestLogsAndFlush(t, repository, allEntries)

	statsBeforeCleanup := WaitForLogsToAppear(t, repository, project.ID, 7, 30000)
	assert.Equal(t, int64(7), statsBeforeCleanup.TotalLogs, "should have 7 logs (2 old + 5 recent) before cleanup")

	err := cleanupService.ExecuteAllTasksForTest()
	assert.NoError(t, err, "cleanup service should execute successfully")

	// Only the 2 old logs go (retention); the 5 recent stay despite the quota.
	statsAfterCleanup := WaitForLogDeletion(t, repository, project.ID, 5, 30000)
	assert.Equal(
		t,
		int64(5),
		statsAfterCleanup.TotalLogs,
		"should have exactly the 5 recent logs remaining: retention still ran (deleted the 2 old logs), quota enforcement did not (didn't touch the 5 recent ones despite exceeding MaxLogsAmount=2)",
	)
}
