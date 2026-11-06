package projects_controllers

import (
	"net/http"
	"testing"

	projects_models "obzly/internal/features/projects/models"
	projects_testing "obzly/internal/features/projects/testing"
	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_GetRetentionPolicies_WhenNoneSet_ReturnsDefaultsPerSignal(t *testing.T) {
	router := projects_testing.CreateTestRouter(GetProjectController(), GetMembershipController())
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Retention defaults", owner.Token, router)

	var policies []projects_models.RetentionPolicy
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/projects/"+project.ID.String()+"/retention-policies",
		"Bearer "+owner.Token, http.StatusOK, &policies)

	require.Len(t, policies, 3)
	for i, signal := range projects_models.Signals {
		assert.Equal(t, signal, policies[i].Signal)
		assert.Zero(t, policies[i].RetentionDays)
		assert.Equal(t, 1.0, policies[i].SampleRate)
	}
}

func Test_SetRetentionPolicies_WhenUserIsOwner_PoliciesSavedAndMembersCanRead(t *testing.T) {
	router := projects_testing.CreateTestRouter(GetProjectController(), GetMembershipController())
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	member := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Retention set", owner.Token, router)
	projects_testing.AddMemberToProject(project, member, users_enums.ProjectRoleMember, owner.Token, router)
	url := "/api/v1/projects/" + project.ID.String() + "/retention-policies"

	request := []projects_models.RetentionPolicy{
		{Signal: projects_models.SignalTraces, RetentionDays: 14, SampleRate: 0.1, SlowMs: 2000},
		{Signal: projects_models.SignalMetrics, RetentionDays: 30, RollupDays: 400, SampleRate: 1},
		{Signal: projects_models.SignalLogs, SampleRate: 1, SampleRules: projects_models.LogSampleRules{
			{Service: "checkout", Level: "debug", Rate: 0.05},
		}},
	}
	test_utils.MakePutRequest(t, router, url, "Bearer "+owner.Token, request, http.StatusOK)
	// Setting the same signals again replaces them.
	request[0].SampleRate = 0.2
	test_utils.MakePutRequest(t, router, url, "Bearer "+owner.Token, request[:1], http.StatusOK)

	var policies []projects_models.RetentionPolicy
	test_utils.MakeGetRequestAndUnmarshal(t, router, url, "Bearer "+member.Token, http.StatusOK, &policies)
	bySignal := map[string]projects_models.RetentionPolicy{}
	for _, policy := range policies {
		bySignal[policy.Signal] = policy
	}
	assert.Equal(t, 14, bySignal["traces"].RetentionDays)
	assert.Equal(t, 0.2, bySignal["traces"].SampleRate)
	assert.Equal(t, 2000, bySignal["traces"].SlowMs)
	assert.Equal(t, 400, bySignal["metrics"].RollupDays)
	assert.Equal(t, projects_models.LogSampleRules{{Service: "checkout", Level: "debug", Rate: 0.05}},
		bySignal["logs"].SampleRules)
}

func Test_SetRetentionPolicies_WhenUserIsMember_ReturnsForbidden(t *testing.T) {
	router := projects_testing.CreateTestRouter(GetProjectController(), GetMembershipController())
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	member := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Retention forbidden", owner.Token, router)
	projects_testing.AddMemberToProject(project, member, users_enums.ProjectRoleMember, owner.Token, router)

	test_utils.MakePutRequest(t, router, "/api/v1/projects/"+project.ID.String()+"/retention-policies",
		"Bearer "+member.Token,
		[]projects_models.RetentionPolicy{{Signal: "logs", RetentionDays: 1, SampleRate: 1}},
		http.StatusForbidden)
}

func Test_GetRetentionPolicies_WhenUserIsNotAMember_ReturnsForbidden(t *testing.T) {
	router := projects_testing.CreateTestRouter(GetProjectController(), GetMembershipController())
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	stranger := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Retention stranger", owner.Token, router)

	test_utils.MakeGetRequest(t, router, "/api/v1/projects/"+project.ID.String()+"/retention-policies",
		"Bearer "+stranger.Token, http.StatusForbidden)
}

func Test_SetRetentionPolicies_InvalidPolicies_ReturnBadRequest(t *testing.T) {
	router := projects_testing.CreateTestRouter(GetProjectController(), GetMembershipController())
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Retention invalid", owner.Token, router)
	url := "/api/v1/projects/" + project.ID.String() + "/retention-policies"

	for name, policy := range map[string]projects_models.RetentionPolicy{
		"unknown signal":      {Signal: "events", SampleRate: 1},
		"negative days":       {Signal: "logs", RetentionDays: -1, SampleRate: 1},
		"rate above 1":        {Signal: "traces", SampleRate: 1.5},
		"rollups on logs":     {Signal: "logs", RollupDays: 30, SampleRate: 1},
		"trace rate on logs":  {Signal: "logs", SampleRate: 0.5},
		"log rules on traces": {Signal: "traces", SampleRate: 1, SampleRules: projects_models.LogSampleRules{{Rate: 0.1}}},
		"rule rate above 1":   {Signal: "logs", SampleRate: 1, SampleRules: projects_models.LogSampleRules{{Rate: 2}}},
	} {
		resp := test_utils.MakePutRequest(t, router, url, "Bearer "+owner.Token,
			[]projects_models.RetentionPolicy{policy}, http.StatusBadRequest)
		assert.NotEmpty(t, string(resp.Body), name)
	}
	test_utils.MakePutRequest(t, router, url, "Bearer "+owner.Token, []projects_models.RetentionPolicy{
		{Signal: "logs", SampleRate: 1}, {Signal: "logs", SampleRate: 1},
	}, http.StatusBadRequest)
}

func Test_EffectiveRetentionDays_FallsBackToLogRetentionAndPlanCaps(t *testing.T) {
	project := &projects_models.Project{MaxLogsLifeDays: 90}
	policy := projects_models.DefaultRetentionPolicy(uuid.New(), projects_models.SignalTraces)
	assert.Equal(t, 90, project.EffectiveRetentionDays(policy), "unset: the project's log retention")
	policy.RetentionDays = 400
	assert.Equal(t, 400, project.EffectiveRetentionDays(policy))
	project.Plan = &users_models.UserPlan{MaxLogsLifeDays: 365}
	assert.Equal(t, 365, project.EffectiveRetentionDays(policy), "a plan caps it")
	policy.RetentionDays = 7
	assert.Equal(t, 7, project.EffectiveRetentionDays(policy))
}
