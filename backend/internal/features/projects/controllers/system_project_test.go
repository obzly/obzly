package projects_controllers

import (
	"net/http"
	"sync"
	"testing"

	projects_services "obzly/internal/features/projects/services"
	projects_testing "obzly/internal/features/projects/testing"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_EnsureSystemProject_ConcurrentCalls_ReturnOneMemberlessProject(t *testing.T) {
	service := projects_services.GetProjectService()

	ids := make([]uuid.UUID, 8)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			project, err := service.EnsureSystemProject()
			assert.NoError(t, err)
			if project != nil {
				ids[i] = project.ID
			}
		}(i)
	}
	wg.Wait()

	for _, id := range ids {
		require.Equal(t, ids[0], id, "every replica gets the same system project")
	}
	project, err := service.EnsureSystemProject()
	require.NoError(t, err)
	require.Equal(t, ids[0], project.ID)
	require.True(t, project.IsSystem)
	require.Equal(t, projects_services.SystemProjectName, project.Name)
	require.True(t, project.IsApiKeyRequired, "no outside ingestion without a key it doesn't have")
}

func Test_SystemProject_VisibleToGlobalAdminOnlyAndNotDeletable(t *testing.T) {
	users_testing.CleanupPlans()
	project, err := projects_services.GetProjectService().EnsureSystemProject()
	require.NoError(t, err)
	router := projects_testing.CreateTestRouter(GetProjectController(), GetMembershipController())
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	member := users_testing.CreateTestUser(users_enums.UserRoleMember)

	test_utils.MakeGetRequest(t, router, "/api/v1/projects/"+project.ID.String(), "Bearer "+admin.Token, http.StatusOK)
	test_utils.MakeGetRequest(t, router, "/api/v1/projects/"+project.ID.String(), "Bearer "+member.Token,
		http.StatusForbidden)

	resp := test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method:         "DELETE",
		URL:            "/api/v1/projects/" + project.ID.String(),
		AuthToken:      "Bearer " + admin.Token,
		ExpectedStatus: http.StatusBadRequest,
	})
	assert.Contains(t, string(resp.Body), projects_services.ErrSystemProjectNotDeletable.Error())
}
