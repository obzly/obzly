package dashboards_controllers_tests

import (
	"fmt"
	"net/http"
	"testing"

	dashboards_dto "obzly/internal/features/dashboards/dto"
	dashboards_models "obzly/internal/features/dashboards/models"
	projects_testing "obzly/internal/features/projects/testing"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// A PROJECT_VIEWER can read dashboards and run panel queries, but every
// mutation is rejected with 403.
func Test_ProjectViewer_CanReadDashboards_CannotWrite(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	uniqueID := uuid.New().String()
	project, _ := projects_testing.CreateTestProjectWithToken(
		fmt.Sprintf("Viewer Role Test %s", uniqueID[:8]), owner.Token, router,
	)

	viewer := users_testing.CreateTestUser(users_enums.UserRoleMember)
	projects_testing.AddMemberToProject(project, viewer, users_enums.ProjectRoleViewer, owner.Token, router)

	// Owner creates a dashboard the viewer will try to read/write.
	var dashboard dashboards_models.Dashboard
	test_utils.MakePostRequestAndUnmarshal(
		t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s", project.ID.String()),
		"Bearer "+owner.Token,
		dashboards_dto.CreateDashboardRequestDTO{Title: "Owner's dashboard"},
		http.StatusOK, &dashboard,
	)

	// --- Reads: all allowed for a viewer ---

	test_utils.MakeGetRequest(
		t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s", project.ID.String()),
		"Bearer "+viewer.Token, http.StatusOK,
	)
	test_utils.MakeGetRequest(
		t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s/%s", project.ID.String(), dashboard.ID.String()),
		"Bearer "+viewer.Token, http.StatusOK,
	)
	test_utils.MakeGetRequest(
		t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s/folders", project.ID.String()),
		"Bearer "+viewer.Token, http.StatusOK,
	)

	// --- Writes: all rejected for a viewer ---

	writeCases := []struct {
		name   string
		method string
		url    string
		body   any
	}{
		{
			"CreateDashboard", "POST",
			fmt.Sprintf("/api/v1/projects/dashboards/%s", project.ID.String()),
			dashboards_dto.CreateDashboardRequestDTO{Title: "Viewer's dashboard"},
		},
		{
			"UpdateDashboard", "PUT",
			fmt.Sprintf("/api/v1/projects/dashboards/%s/%s", project.ID.String(), dashboard.ID.String()),
			dashboards_dto.UpdateDashboardRequestDTO{Title: "Renamed by viewer"},
		},
		{
			"DeleteDashboard", "DELETE",
			fmt.Sprintf("/api/v1/projects/dashboards/%s/%s", project.ID.String(), dashboard.ID.String()),
			nil,
		},
		{
			"SetPinned", "PUT",
			fmt.Sprintf("/api/v1/projects/dashboards/%s/%s/pin", project.ID.String(), dashboard.ID.String()),
			dashboards_dto.SetPinnedRequestDTO{Pinned: true},
		},
		{
			"GenerateOverview", "POST",
			fmt.Sprintf("/api/v1/projects/dashboards/%s/overview", project.ID.String()),
			nil,
		},
		{
			"ImportDashboard", "POST",
			fmt.Sprintf("/api/v1/projects/dashboards/%s/import", project.ID.String()),
			dashboards_dto.ImportDashboardRequestDTO{Format: "grafana", JSON: `{"title":"x","panels":[]}`},
		},
		{
			"CreateFolder", "POST",
			fmt.Sprintf("/api/v1/projects/dashboards/%s/folders", project.ID.String()),
			dashboards_dto.CreateFolderRequestDTO{Title: "Viewer's folder"},
		},
	}

	for _, tc := range writeCases {
		t.Run(tc.name, func(t *testing.T) {
			resp := test_utils.MakeRequest(t, router, test_utils.RequestOptions{
				Method:         tc.method,
				URL:            tc.url,
				Body:           tc.body,
				AuthToken:      "Bearer " + viewer.Token,
				ExpectedStatus: http.StatusForbidden,
			})
			require.Contains(t, string(resp.Body), "insufficient permissions")
		})
	}
}
