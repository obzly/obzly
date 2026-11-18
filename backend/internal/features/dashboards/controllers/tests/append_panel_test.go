package dashboards_controllers_tests

import (
	"fmt"
	"net/http"
	"sync"
	"testing"

	dashboards_dto "obzly/internal/features/dashboards/dto"
	dashboards_models "obzly/internal/features/dashboards/models"
	projects_testing "obzly/internal/features/projects/testing"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_AppendPanel_AddsToTheBottomOfTheTabAndKeepsExistingPanels(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	viewer := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Append panel "+uuid.NewString()[:8], owner.Token, router)
	projects_testing.AddMemberToProject(project, viewer, users_enums.ProjectRoleViewer, owner.Token, router)
	base := fmt.Sprintf("/api/v1/projects/dashboards/%s", project.ID)

	var dashboard dashboards_models.Dashboard
	test_utils.MakePostRequestAndUnmarshal(t, router, base, "Bearer "+owner.Token,
		dashboards_dto.CreateDashboardRequestDTO{Title: "Ops", Tabs: []dashboards_models.Tab{
			{Title: "Main", Panels: []dashboards_models.Panel{{ID: "existing", Title: "Errors", Type: "line",
				GridPos: dashboards_models.GridPos{X: 0, Y: 0, W: 12, H: 6}}}},
			{Title: "Second"},
		}}, http.StatusOK, &dashboard)
	url := fmt.Sprintf("%s/%s/panels", base, dashboard.ID)

	var appended dashboards_dto.AppendPanelResponseDTO
	test_utils.MakePostRequestAndUnmarshal(t, router, url, "Bearer "+owner.Token,
		dashboards_dto.AppendPanelRequestDTO{Panel: dashboards_models.Panel{ID: "existing", Title: "Latency",
			Type: "stat", Source: "spans", Metric: "p95_duration_ms"}}, http.StatusOK, &appended)
	assert.Equal(t, dashboard.Tabs[0].ID, appended.TabID)
	assert.NotEqual(t, "existing", appended.Panel.ID, "the server assigns the id")
	assert.Equal(t, dashboards_models.GridPos{X: 0, Y: 6, W: 6, H: 8}, appended.Panel.GridPos)
	require.Len(t, appended.Dashboard.Tabs[0].Panels, 2)
	assert.Equal(t, "existing", appended.Dashboard.Tabs[0].Panels[0].ID)

	test_utils.MakePostRequestAndUnmarshal(t, router, url, "Bearer "+owner.Token,
		dashboards_dto.AppendPanelRequestDTO{TabID: dashboard.Tabs[1].ID, Panel: dashboards_models.Panel{
			Title: "Pods", Type: "table", Source: "metrics", PromQuery: "up"}}, http.StatusOK, &appended)
	assert.Equal(t, dashboard.Tabs[1].ID, appended.TabID)
	assert.Equal(t, 0, appended.Panel.GridPos.Y)

	// Validated like a full save, permission-checked, and only on this project's dashboards.
	test_utils.MakePostRequest(t, router, url, "Bearer "+owner.Token,
		dashboards_dto.AppendPanelRequestDTO{Panel: dashboards_models.Panel{Title: "Bad", Type: "sparkles"}},
		http.StatusBadRequest)
	test_utils.MakePostRequest(t, router, url, "Bearer "+owner.Token,
		dashboards_dto.AppendPanelRequestDTO{TabID: "nope", Panel: dashboards_models.Panel{Title: "x", Type: "line"}},
		http.StatusBadRequest)
	test_utils.MakePostRequest(t, router, url, "Bearer "+viewer.Token,
		dashboards_dto.AppendPanelRequestDTO{Panel: dashboards_models.Panel{Title: "x", Type: "line"}},
		http.StatusForbidden)
	test_utils.MakePostRequest(t, router, fmt.Sprintf("%s/%s/panels", base, uuid.New()), "Bearer "+owner.Token,
		dashboards_dto.AppendPanelRequestDTO{Panel: dashboards_models.Panel{Title: "x", Type: "line"}},
		http.StatusNotFound)
}

func Test_AppendPanel_ConcurrentAppends_KeepEveryPanel(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Append race "+uuid.NewString()[:8], owner.Token, router)
	base := fmt.Sprintf("/api/v1/projects/dashboards/%s", project.ID)
	var dashboard dashboards_models.Dashboard
	test_utils.MakePostRequestAndUnmarshal(t, router, base, "Bearer "+owner.Token,
		dashboards_dto.CreateDashboardRequestDTO{Title: "Race"}, http.StatusOK, &dashboard)

	var wg sync.WaitGroup
	for i := range 5 {
		wg.Go(func() {
			test_utils.MakePostRequest(t, router, fmt.Sprintf("%s/%s/panels", base, dashboard.ID),
				"Bearer "+owner.Token, dashboards_dto.AppendPanelRequestDTO{Panel: dashboards_models.Panel{
					Title: fmt.Sprintf("Panel %d", i), Type: "line"}}, http.StatusOK)
		})
	}
	wg.Wait()

	var stored dashboards_models.Dashboard
	test_utils.MakeGetRequestAndUnmarshal(t, router, fmt.Sprintf("%s/%s", base, dashboard.ID), "Bearer "+owner.Token,
		http.StatusOK, &stored)
	assert.Len(t, stored.Tabs[0].Panels, 5)
}
