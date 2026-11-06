package users_controllers

import (
	"net/http"
	"testing"

	projects_testing "obzly/internal/features/projects/testing"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	"obzly/internal/storage"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_DeleteUser_WhenUserIsAdmin_UserDeletedAndTokenRejected(t *testing.T) {
	router := createManagementTestRouter()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	target := users_testing.CreateTestUser(users_enums.UserRoleMember)

	resp := test_utils.MakeDeleteRequest(t, router, "/api/v1/users/"+target.UserID.String(),
		"Bearer "+admin.Token, http.StatusOK)
	assert.Contains(t, string(resp.Body), "User deleted successfully")

	var count int64
	require.NoError(t, storage.GetDb().Raw("SELECT count(*) FROM users WHERE id = ?", target.UserID).Scan(&count).Error)
	assert.Zero(t, count)
	test_utils.MakeGetRequest(t, router, "/api/v1/users/"+target.UserID.String(),
		"Bearer "+target.Token, http.StatusUnauthorized)
}

func Test_DeleteUser_WhenUserIsMember_ReturnsForbidden(t *testing.T) {
	router := createManagementTestRouter()
	member := users_testing.CreateTestUser(users_enums.UserRoleMember)
	target := users_testing.CreateTestUser(users_enums.UserRoleMember)

	test_utils.MakeDeleteRequest(t, router, "/api/v1/users/"+target.UserID.String(),
		"Bearer "+member.Token, http.StatusForbidden)
}

func Test_DeleteUser_WhenDeletingOwnAccount_ReturnsBadRequest(t *testing.T) {
	router := createManagementTestRouter()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	resp := test_utils.MakeDeleteRequest(t, router, "/api/v1/users/"+admin.UserID.String(),
		"Bearer "+admin.Token, http.StatusBadRequest)
	assert.Contains(t, string(resp.Body), "cannot delete your own account")
}

func Test_DeleteUser_WhenRegularAdminDeletesAdmin_ReturnsBadRequest(t *testing.T) {
	router := createManagementTestRouter()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	otherAdmin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	resp := test_utils.MakeDeleteRequest(t, router, "/api/v1/users/"+otherAdmin.UserID.String(),
		"Bearer "+admin.Token, http.StatusBadRequest)
	assert.Contains(t, string(resp.Body), "only the root admin user can delete admin accounts")
}

func Test_DeleteUser_WhenRootAdminDeletesAdmin_UserDeleted(t *testing.T) {
	router := createManagementTestRouter()
	rootAdmin := users_testing.ReacreateInitAdminAndGetAccess()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	test_utils.MakeDeleteRequest(t, router, "/api/v1/users/"+admin.UserID.String(),
		"Bearer "+rootAdmin.Token, http.StatusOK)
}

func Test_DeleteUser_WhenTargetIsRootAdmin_ReturnsBadRequest(t *testing.T) {
	router := createManagementTestRouter()
	rootAdmin := users_testing.ReacreateInitAdminAndGetAccess()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	resp := test_utils.MakeDeleteRequest(t, router, "/api/v1/users/"+rootAdmin.UserID.String(),
		"Bearer "+admin.Token, http.StatusBadRequest)
	assert.Contains(t, string(resp.Body), "root admin account cannot be deleted")
}

func Test_DeleteUser_WhenUserDoesNotExist_ReturnsNotFound(t *testing.T) {
	router := createManagementTestRouter()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	test_utils.MakeDeleteRequest(t, router, "/api/v1/users/"+uuid.NewString(),
		"Bearer "+admin.Token, http.StatusNotFound)
}

// A project needs an owner: deleting its owner is refused until ownership
// moves; a plain membership goes with the user.
func Test_DeleteUser_WhenUserOwnsAProject_ReturnsConflictUntilOwnershipMoves(t *testing.T) {
	router := createInviteProjectTestRouter()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	member := users_testing.CreateTestUser(users_enums.UserRoleMember)
	projectName := "Owned " + uuid.NewString()[:8]
	project, _ := projects_testing.CreateTestProjectViaAPI(projectName, owner, router)
	projects_testing.AddMemberToProject(project, member, users_enums.ProjectRoleMember, owner.Token, router)

	resp := test_utils.MakeDeleteRequest(t, router, "/api/v1/users/"+owner.UserID.String(),
		"Bearer "+admin.Token, http.StatusConflict)
	assert.Contains(t, string(resp.Body), projectName)

	test_utils.MakeDeleteRequest(t, router, "/api/v1/users/"+member.UserID.String(),
		"Bearer "+admin.Token, http.StatusOK)
	var memberships int64
	require.NoError(t, storage.GetDb().Raw("SELECT count(*) FROM project_memberships WHERE user_id = ?",
		member.UserID).Scan(&memberships).Error)
	assert.Zero(t, memberships)
}

// Site grants a deleted user created stay, attributed to the deleting admin
// (created_by can't be NULL and would otherwise block the delete).
func Test_DeleteUser_WhenUserCreatedSiteGrants_GrantsMoveToTheAdmin(t *testing.T) {
	router := createManagementTestRouter()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	target := users_testing.CreateTestUser(users_enums.UserRoleMember)
	grantee := users_testing.CreateTestUser(users_enums.UserRoleMember)
	siteID := "delete-user-test-" + uuid.NewString()[:8]
	db := storage.GetDb()
	require.NoError(t, db.Exec("INSERT INTO sites (id, name, api_url) VALUES (?, ?, ?)",
		siteID, siteID, "https://"+siteID+".example.com").Error)
	t.Cleanup(func() { db.Exec("DELETE FROM sites WHERE id = ?", siteID) })
	grantID := uuid.New()
	require.NoError(t, db.Exec(
		"INSERT INTO site_grants (id, user_id, site_id, role, created_by) VALUES (?, ?, ?, 'MEMBER', ?)",
		grantID, grantee.UserID, siteID, target.UserID).Error)

	test_utils.MakeDeleteRequest(t, router, "/api/v1/users/"+target.UserID.String(),
		"Bearer "+admin.Token, http.StatusOK)

	var createdBy string
	require.NoError(t, db.Raw("SELECT created_by::text FROM site_grants WHERE id = ?", grantID).Row().Scan(&createdBy))
	assert.Equal(t, admin.UserID.String(), createdBy)
}
