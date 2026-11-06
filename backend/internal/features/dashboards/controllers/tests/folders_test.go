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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_CreateFolder_ThenListFolders_ReturnsIt(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Folders Create", owner.Token, router)

	var folder dashboards_models.Folder
	test_utils.MakePostRequestAndUnmarshal(
		t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s/folders", project.ID.String()),
		"Bearer "+owner.Token, dashboards_dto.CreateFolderRequestDTO{Title: "Infra"}, http.StatusOK, &folder,
	)
	assert.Equal(t, "Infra", folder.Title)
	assert.Equal(t, project.ID, folder.ProjectID)

	var list dashboards_dto.ListFoldersResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s/folders", project.ID.String()),
		"Bearer "+owner.Token, http.StatusOK, &list,
	)
	require.Len(t, list.Folders, 1)
	assert.Equal(t, "Infra", list.Folders[0].Title)
}

func Test_RenameFolder_ChangesTitle(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Folders Rename", owner.Token, router)

	var folder dashboards_models.Folder
	test_utils.MakePostRequestAndUnmarshal(
		t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s/folders", project.ID.String()),
		"Bearer "+owner.Token, dashboards_dto.CreateFolderRequestDTO{Title: "Old"}, http.StatusOK, &folder,
	)

	var renamed dashboards_models.Folder
	test_utils.MakePutRequestAndUnmarshal(
		t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s/folders/%s", project.ID.String(), folder.ID.String()),
		"Bearer "+owner.Token, dashboards_dto.RenameFolderRequestDTO{Title: "New"}, http.StatusOK, &renamed,
	)
	assert.Equal(t, "New", renamed.Title)
}

func Test_MoveDashboardToFolder_ThenDeleteFolder_UnfilesDashboard(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Folders Move", owner.Token, router)

	var folder dashboards_models.Folder
	test_utils.MakePostRequestAndUnmarshal(
		t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s/folders", project.ID.String()),
		"Bearer "+owner.Token, dashboards_dto.CreateFolderRequestDTO{Title: "Infra"}, http.StatusOK, &folder,
	)

	var dashboard dashboards_models.Dashboard
	test_utils.MakePostRequestAndUnmarshal(
		t,
		router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s", project.ID.String()),
		"Bearer "+owner.Token,
		dashboards_dto.CreateDashboardRequestDTO{Title: "My Dashboard"},
		http.StatusOK,
		&dashboard,
	)
	require.Nil(t, dashboard.FolderID)

	var moved dashboards_models.Dashboard
	test_utils.MakePutRequestAndUnmarshal(
		t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s/%s/folder", project.ID.String(), dashboard.ID.String()),
		"Bearer "+owner.Token, dashboards_dto.SetDashboardFolderRequestDTO{FolderID: &folder.ID}, http.StatusOK, &moved,
	)
	require.NotNil(t, moved.FolderID)
	assert.Equal(t, folder.ID, *moved.FolderID)

	test_utils.MakeDeleteRequest(
		t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s/folders/%s", project.ID.String(), folder.ID.String()),
		"Bearer "+owner.Token, http.StatusOK,
	)

	var afterDelete dashboards_models.Dashboard
	test_utils.MakeGetRequestAndUnmarshal(
		t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s/%s", project.ID.String(), dashboard.ID.String()),
		"Bearer "+owner.Token, http.StatusOK, &afterDelete,
	)
	assert.Nil(t, afterDelete.FolderID, "deleting a folder should un-file its dashboards, not delete them")
}

func Test_CreateFolder_NonMember_ReturnsForbidden(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	outsider := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Folders Forbidden", owner.Token, router)

	test_utils.MakePostRequest(
		t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s/folders", project.ID.String()),
		"Bearer "+outsider.Token, dashboards_dto.CreateFolderRequestDTO{Title: "Nope"}, http.StatusForbidden,
	)
}
