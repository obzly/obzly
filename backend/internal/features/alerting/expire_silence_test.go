package alerting_test

import (
	"net/http"
	"testing"
	"time"

	"obzly/internal/features/alerting"
	projects_controllers "obzly/internal/features/projects/controllers"
	projects_testing "obzly/internal/features/projects/testing"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	"obzly/internal/storage"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_ExpireSilence_ByWriter_EndsItNowAndKeepsTheRecord(t *testing.T) {
	router := projects_testing.CreateTestRouter(projects_controllers.GetProjectController(),
		projects_controllers.GetMembershipController(), alerting.GetController())
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	viewer := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Expire silence", owner.Token, router)
	projects_testing.AddMemberToProject(project, viewer, users_enums.ProjectRoleViewer, owner.Token, router)
	base := "/api/v1/projects/" + project.ID.String()

	var created alerting.Silence
	test_utils.MakePostRequestAndUnmarshal(t, router, base+"/silences", "Bearer "+owner.Token,
		alerting.SilenceRequest{Matchers: alerting.Matchers{{Name: "alertname", Value: "HighCPU"}},
			EndsAt: time.Now().Add(time.Hour), Comment: "deploy", MCPClient: "Claude"},
		http.StatusOK, &created)
	assert.Equal(t, "Claude", created.MCPClient)

	test_utils.MakePostRequest(t, router, base+"/silences/"+created.ID.String()+"/expire", "Bearer "+viewer.Token,
		nil, http.StatusForbidden)
	var expired alerting.Silence
	test_utils.MakePostRequestAndUnmarshal(t, router, base+"/silences/"+created.ID.String()+"/expire",
		"Bearer "+owner.Token, nil, http.StatusOK, &expired)
	assert.Equal(t, created.ID, expired.ID)
	assert.False(t, expired.EndsAt.After(time.Now().UTC()))
	assert.Equal(t, "deploy", expired.Comment)

	var listed []alerting.Silence
	test_utils.MakeGetRequestAndUnmarshal(t, router, base+"/silences", "Bearer "+owner.Token, http.StatusOK, &listed)
	assert.Empty(t, listed)
	var stored alerting.Silence
	require.NoError(t, storage.GetDb().Where("id = ?", created.ID).First(&stored).Error)
	assert.Equal(t, "Claude", stored.MCPClient)

	test_utils.MakePostRequest(t, router, base+"/silences/"+created.ID.String()+"/expire", "Bearer "+owner.Token,
		nil, http.StatusConflict)
	test_utils.MakePostRequest(t, router, base+"/silences/"+uuid.NewString()+"/expire", "Bearer "+owner.Token,
		nil, http.StatusNotFound)
}

func Test_ExpireSilence_OfAMaintenanceWindow_IsRefused(t *testing.T) {
	router := projects_testing.CreateTestRouter(projects_controllers.GetProjectController(),
		projects_controllers.GetMembershipController(), alerting.GetController())
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Expire managed", owner.Token, router)
	now := time.Now().UTC()
	managed := &alerting.Silence{ID: uuid.New(), ProjectID: project.ID,
		Matchers: alerting.Matchers{{Name: "check_id", Value: "a"}}, StartsAt: now, EndsAt: now.Add(time.Hour),
		CreatedAt: now, ManagedBy: "synthetics-maintenance:" + uuid.NewString()}
	require.NoError(t, storage.GetDb().Create(managed).Error)

	test_utils.MakePostRequest(t, router,
		"/api/v1/projects/"+project.ID.String()+"/silences/"+managed.ID.String()+"/expire", "Bearer "+owner.Token,
		nil, http.StatusConflict)
}
