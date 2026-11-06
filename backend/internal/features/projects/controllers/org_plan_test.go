package projects_controllers

import (
	"net/http"
	"testing"

	"obzly/internal/features/orgplan"
	projects_dto "obzly/internal/features/projects/dto"
	projects_models "obzly/internal/features/projects/models"
	projects_repositories "obzly/internal/features/projects/repositories"
	projects_testing "obzly/internal/features/projects/testing"
	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The organization's plan caps a project's limits, and applies even when
// the project turns its own rate limiting off.
func Test_EffectiveLimits_OrgPlanCapsAndAppliesWithRateLimitingOff(t *testing.T) {
	t.Cleanup(func() { orgplan.Set(orgplan.Limits{}) })
	project := &projects_models.Project{IsRateLimitingEnabled: true, LogsPerSecondLimit: 1000, MaxLogSizeKB: 64,
		MaxLogsAmount: 1000, MaxLogsSizeMB: 500, MaxLogsLifeDays: 90}
	assert.Equal(t, 1000, project.EffectiveLogsPerSecondLimit(), "no plan: the project's own")

	orgplan.Set(orgplan.Limits{LogsPerSecondLimit: 100, MaxLogSizeKB: 128, MaxLogsAmount: 10, MaxLogsSizeMB: 50,
		MaxLogsLifeDays: 30})
	assert.Equal(t, 100, project.EffectiveLogsPerSecondLimit())
	assert.Equal(t, 64, project.EffectiveMaxLogSizeKB(), "the project's is stricter")
	assert.Equal(t, int64(10), project.EffectiveMaxLogsAmount())
	assert.Equal(t, 50, project.EffectiveMaxLogsSizeMB())
	policy := projects_models.DefaultRetentionPolicy(uuid.New(), projects_models.SignalLogs)
	assert.Equal(t, 30, project.EffectiveRetentionDays(policy))
	project.Plan = &users_models.UserPlan{MaxLogsLifeDays: 7}
	assert.Equal(t, 7, project.EffectiveRetentionDays(policy), "the user plan is stricter")

	project.IsRateLimitingEnabled = false
	assert.Equal(t, 100, project.EffectiveLogsPerSecondLimit(), "the plan holds with rate limiting off")
	assert.Equal(t, 128, project.EffectiveMaxLogSizeKB())
	assert.Equal(t, int64(10), project.EffectiveMaxLogsAmount())
	orgplan.Set(orgplan.Limits{})
	assert.Equal(t, 0, project.EffectiveLogsPerSecondLimit(), "off and no plan: unlimited")
}

func Test_CreateProject_AtOrgPlanProjectLimit_ReturnsForbidden(t *testing.T) {
	t.Cleanup(func() { orgplan.Set(orgplan.Limits{}) })
	router := projects_testing.CreateTestRouter(GetProjectController(), GetMembershipController())
	owner := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	project, _ := projects_testing.CreateTestProjectWithToken("Org plan "+uuid.NewString()[:6], owner.Token, router)

	// 1: this test's project always counts, whatever other packages sharing the
	// database create or delete meanwhile.
	count, err := (&projects_repositories.ProjectRepository{}).CountUserProjects()
	require.NoError(t, err)
	require.GreaterOrEqual(t, count, int64(1))
	orgplan.Set(orgplan.Limits{MaxProjects: 1, MaxLogsLifeDays: 30})
	resp := test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method: "POST", URL: "/api/v1/projects", AuthToken: "Bearer " + owner.Token,
		Body:           projects_dto.CreateProjectRequestDTO{Name: "One too many"},
		ExpectedStatus: http.StatusForbidden,
	})
	assert.Contains(t, string(resp.Body), "allows")

	var fetched projects_models.Project
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/projects/"+project.ID.String(),
		"Bearer "+owner.Token, http.StatusOK, &fetched)
	require.NotNil(t, fetched.OrgPlan)
	assert.Equal(t, 30, fetched.OrgPlan.MaxLogsLifeDays)

	orgplan.Set(orgplan.Limits{})
	projects_testing.CreateTestProjectWithToken("Org plan lifted "+uuid.NewString()[:6], owner.Token, router)
}
