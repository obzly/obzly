package users_controllers

import (
	"net/http"
	"testing"
	"time"

	"obzly/internal/features/audit_logs"
	projects_controllers "obzly/internal/features/projects/controllers"
	projects_dto "obzly/internal/features/projects/dto"
	projects_testing "obzly/internal/features/projects/testing"
	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	users_middleware "obzly/internal/features/users/middleware"
	users_services "obzly/internal/features/users/services"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_GetUsersList_WhenUserIsAdmin_ReturnsUsers(t *testing.T) {
	router := createManagementTestRouter()

	testUser := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	var response users_dto.ListUsersResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t,
		router,
		"/api/v1/users",
		"Bearer "+testUser.Token,
		http.StatusOK,
		&response,
	)

	assert.NotNil(t, response.Users)
	assert.GreaterOrEqual(t, response.Total, int64(1)) // At least the test user should exist
}

func Test_GetUsersList_WhenUserIsMember_ReturnsForbidden(t *testing.T) {
	router := createManagementTestRouter()

	testUser := users_testing.CreateTestUser(users_enums.UserRoleMember)

	resp := test_utils.MakeGetRequest(t, router, "/api/v1/users", "Bearer "+testUser.Token, http.StatusForbidden)
	assert.Contains(t, string(resp.Body), "permissions")
}

func Test_GetUsersList_WithPagination_RespectsLimits(t *testing.T) {
	router := createManagementTestRouter()

	testUser := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	var response users_dto.ListUsersResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t,
		router,
		"/api/v1/users?limit=5&offset=0",
		"Bearer "+testUser.Token,
		http.StatusOK,
		&response,
	)

	assert.NotNil(t, response.Users)
	assert.LessOrEqual(t, len(response.Users), 5) // Should respect limit
}

func Test_GetUsersList_WithBeforeDateFilter_ReturnsFilteredUsers(t *testing.T) {
	router := createManagementTestRouter()

	testUser := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	beforeDate := "2024-01-01T00:00:00Z"
	var response users_dto.ListUsersResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t,
		router,
		"/api/v1/users?beforeDate="+beforeDate,
		"Bearer "+testUser.Token,
		http.StatusOK,
		&response,
	)

	assert.NotNil(t, response.Users)
	for _, user := range response.Users {
		assert.True(t, user.CreatedAt.Before(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)))
	}
}

func Test_GetUsersList_WithInvalidDateFilter_ReturnsBadRequest(t *testing.T) {
	router := createManagementTestRouter()

	testUser := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	resp := test_utils.MakeGetRequest(
		t,
		router,
		"/api/v1/users?beforeDate=invalid-date",
		"Bearer "+testUser.Token,
		http.StatusBadRequest,
	)
	assert.Contains(t, string(resp.Body), "Invalid query parameters")
}

func Test_GetUsersList_WithSearchQuery_ReturnsFilteredUsers(t *testing.T) {
	router := createManagementTestRouter()

	adminUser := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	user1 := users_testing.CreateTestUser(users_enums.UserRoleMember)
	user2 := users_testing.CreateTestUser(users_enums.UserRoleMember)

	emailPart := user1.Email[:5]
	var emailResponse users_dto.ListUsersResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t,
		router,
		"/api/v1/users?query="+emailPart,
		"Bearer "+adminUser.Token,
		http.StatusOK,
		&emailResponse,
	)

	assert.NotNil(t, emailResponse.Users)
	found := false
	for _, u := range emailResponse.Users {
		if u.ID == user1.UserID {
			found = true
			break
		}
	}
	assert.True(t, found, "Expected user1 to be in search results")

	upperEmailPart := user2.Email[:5]
	var caseInsensitiveResponse users_dto.ListUsersResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t,
		router,
		"/api/v1/users?query="+upperEmailPart,
		"Bearer "+adminUser.Token,
		http.StatusOK,
		&caseInsensitiveResponse,
	)

	assert.NotNil(t, caseInsensitiveResponse.Users)
	found = false
	for _, u := range caseInsensitiveResponse.Users {
		if u.ID == user2.UserID {
			found = true
			break
		}
	}
	assert.True(t, found, "Expected user2 to be in case-insensitive search results")

	var nameResponse users_dto.ListUsersResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t,
		router,
		"/api/v1/users?query=Test",
		"Bearer "+adminUser.Token,
		http.StatusOK,
		&nameResponse,
	)

	assert.NotNil(t, nameResponse.Users)
	assert.GreaterOrEqual(t, len(nameResponse.Users), 1, "Should find users with 'Test' in name")
}

func Test_GetUsersList_WithoutAuth_ReturnsUnauthorized(t *testing.T) {
	router := createManagementTestRouter()

	test_utils.MakeGetRequest(t, router, "/api/v1/users", "", http.StatusUnauthorized)
}

func Test_GetUserProfile_WhenAccessingOwnProfile_ReturnsProfile(t *testing.T) {
	router := createManagementTestRouter()

	testUser := users_testing.CreateTestUser(users_enums.UserRoleMember)

	var response users_dto.UserProfileResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t,
		router,
		"/api/v1/users/"+testUser.UserID.String(),
		"Bearer "+testUser.Token,
		http.StatusOK,
		&response,
	)

	assert.Equal(t, testUser.UserID, response.ID)
	assert.Equal(t, users_enums.UserRoleMember, response.Role)
}

func Test_GetUserProfile_WhenUserIsAdmin_ReturnsProfile(t *testing.T) {
	router := createManagementTestRouter()

	adminUser := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	regularUser := users_testing.CreateTestUser(users_enums.UserRoleMember)

	var response users_dto.UserProfileResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t,
		router,
		"/api/v1/users/"+regularUser.UserID.String(),
		"Bearer "+adminUser.Token,
		http.StatusOK,
		&response,
	)

	assert.Equal(t, regularUser.UserID, response.ID)
}

func Test_GetUserProfile_WhenAccessingOtherUserAsMember_ReturnsForbidden(t *testing.T) {
	router := createManagementTestRouter()

	user1 := users_testing.CreateTestUser(users_enums.UserRoleMember)
	user2 := users_testing.CreateTestUser(users_enums.UserRoleMember)

	test_utils.MakeGetRequest(
		t,
		router,
		"/api/v1/users/"+user2.UserID.String(),
		"Bearer "+user1.Token,
		http.StatusForbidden,
	)
}

func Test_GetUserProfile_WithNonExistentUser_ReturnsForbidden(t *testing.T) {
	router := createManagementTestRouter()

	testUser := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	test_utils.MakeGetRequest(
		t,
		router,
		"/api/v1/users/00000000-0000-0000-0000-000000000000",
		"Bearer "+testUser.Token,
		http.StatusForbidden,
	)
}

func Test_GetUserProfile_WithInvalidUserID_ReturnsBadRequest(t *testing.T) {
	router := createManagementTestRouter()

	testUser := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	resp := test_utils.MakeGetRequest(
		t,
		router,
		"/api/v1/users/invalid-uuid",
		"Bearer "+testUser.Token,
		http.StatusBadRequest,
	)
	assert.Contains(t, string(resp.Body), "Invalid user ID")
}

func Test_DeactivateUser_WhenUserIsAdmin_UserDeactivated(t *testing.T) {
	router := createManagementTestRouter()

	adminUser := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	targetUser := users_testing.CreateTestUser(users_enums.UserRoleMember)

	resp := test_utils.MakePostRequest(
		t,
		router,
		"/api/v1/users/"+targetUser.UserID.String()+"/deactivate",
		"Bearer "+adminUser.Token,
		nil,
		http.StatusOK,
	)
	assert.Contains(t, string(resp.Body), "User deactivated successfully")
}

func Test_DeactivateUser_WhenUserIsMember_ReturnsForbidden(t *testing.T) {
	router := createManagementTestRouter()

	user1 := users_testing.CreateTestUser(users_enums.UserRoleMember)
	user2 := users_testing.CreateTestUser(users_enums.UserRoleMember)

	test_utils.MakePostRequest(
		t,
		router,
		"/api/v1/users/"+user2.UserID.String()+"/deactivate",
		"Bearer "+user1.Token,
		nil,
		http.StatusForbidden,
	)
}

func Test_DeactivateUser_WhenDeactivatingOwnAccount_ReturnsBadRequest(t *testing.T) {
	router := createManagementTestRouter()

	adminUser := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	resp := test_utils.MakePostRequest(
		t,
		router,
		"/api/v1/users/"+adminUser.UserID.String()+"/deactivate",
		"Bearer "+adminUser.Token,
		nil,
		http.StatusBadRequest,
	)
	assert.Contains(t, string(resp.Body), "cannot deactivate your own account")
}

func Test_ActivateUser_WhenUserIsAdmin_UserActivated(t *testing.T) {
	router := createManagementTestRouter()

	adminUser := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	targetUser := users_testing.CreateTestUser(users_enums.UserRoleMember)

	test_utils.MakePostRequest(
		t,
		router,
		"/api/v1/users/"+targetUser.UserID.String()+"/deactivate",
		"Bearer "+adminUser.Token,
		nil,
		http.StatusOK,
	)

	resp := test_utils.MakePostRequest(
		t,
		router,
		"/api/v1/users/"+targetUser.UserID.String()+"/activate",
		"Bearer "+adminUser.Token,
		nil,
		http.StatusOK,
	)
	assert.Contains(t, string(resp.Body), "User activated successfully")
}

func Test_ActivateUser_WhenUserIsMember_ReturnsForbidden(t *testing.T) {
	router := createManagementTestRouter()

	user1 := users_testing.CreateTestUser(users_enums.UserRoleMember)
	user2 := users_testing.CreateTestUser(users_enums.UserRoleMember)

	test_utils.MakePostRequest(
		t,
		router,
		"/api/v1/users/"+user2.UserID.String()+"/activate",
		"Bearer "+user1.Token,
		nil,
		http.StatusForbidden,
	)
}

func Test_ChangeUserRole_WhenUserIsRootAdmin_RoleChanged(t *testing.T) {
	router := createManagementTestRouter()

	rootAdmin := users_testing.ReacreateInitAdminAndGetAccess()
	targetUser := users_testing.CreateTestUser(users_enums.UserRoleMember)

	request := users_dto.ChangeUserRoleRequestDTO{
		Role: users_enums.UserRoleAdmin,
	}

	resp := test_utils.MakePutRequest(
		t,
		router,
		"/api/v1/users/"+targetUser.UserID.String()+"/role",
		"Bearer "+rootAdmin.Token,
		request,
		http.StatusOK,
	)
	assert.Contains(t, string(resp.Body), "User role changed successfully")
}

func Test_ChangeUserRole_WhenUserIsMember_ReturnsForbidden(t *testing.T) {
	router := createManagementTestRouter()

	user1 := users_testing.CreateTestUser(users_enums.UserRoleMember)
	user2 := users_testing.CreateTestUser(users_enums.UserRoleMember)

	request := users_dto.ChangeUserRoleRequestDTO{
		Role: users_enums.UserRoleAdmin,
	}

	test_utils.MakePutRequest(
		t,
		router,
		"/api/v1/users/"+user2.UserID.String()+"/role",
		"Bearer "+user1.Token,
		request,
		http.StatusForbidden,
	)
}

func Test_ChangeUserRole_WhenChangingOwnRole_ReturnsBadRequest(t *testing.T) {
	router := createManagementTestRouter()

	adminUser := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	request := users_dto.ChangeUserRoleRequestDTO{
		Role: users_enums.UserRoleMember,
	}

	resp := test_utils.MakePutRequest(
		t,
		router,
		"/api/v1/users/"+adminUser.UserID.String()+"/role",
		"Bearer "+adminUser.Token,
		request,
		http.StatusBadRequest,
	)
	assert.Contains(t, string(resp.Body), "cannot change your own role")
}

func Test_ChangeUserRole_WithInvalidRole_ReturnsBadRequest(t *testing.T) {
	router := createManagementTestRouter()

	adminUser := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	targetUser := users_testing.CreateTestUser(users_enums.UserRoleMember)

	resp := test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method:         "PUT",
		URL:            "/api/v1/users/" + targetUser.UserID.String() + "/role",
		Body:           map[string]string{"role": "INVALID_ROLE"},
		AuthToken:      "Bearer " + adminUser.Token,
		ExpectedStatus: http.StatusBadRequest,
	})

	assert.NotEmpty(t, resp.Body)
}

func Test_ChangeUserRole_WithInvalidJSON_ReturnsBadRequest(t *testing.T) {
	router := createManagementTestRouter()

	adminUser := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	targetUser := users_testing.CreateTestUser(users_enums.UserRoleMember)

	resp := test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method:         "PUT",
		URL:            "/api/v1/users/" + targetUser.UserID.String() + "/role",
		Body:           "invalid json",
		AuthToken:      "Bearer " + adminUser.Token,
		ExpectedStatus: http.StatusBadRequest,
	})

	assert.Contains(t, string(resp.Body), "Invalid request format")
}

func Test_ChangeUserRole_WhenRegularAdminPromotesToAdmin_ReturnsBadRequest(t *testing.T) {
	router := createManagementTestRouter()

	regularAdmin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	targetUser := users_testing.CreateTestUser(users_enums.UserRoleMember)

	request := users_dto.ChangeUserRoleRequestDTO{
		Role: users_enums.UserRoleAdmin,
	}

	resp := test_utils.MakePutRequest(
		t,
		router,
		"/api/v1/users/"+targetUser.UserID.String()+"/role",
		"Bearer "+regularAdmin.Token,
		request,
		http.StatusBadRequest,
	)
	assert.Contains(t, string(resp.Body), "only the root admin user can promote users to admin or demote admin users")
}

func Test_ChangeUserRole_WhenRegularAdminDemotesAdmin_ReturnsBadRequest(t *testing.T) {
	router := createManagementTestRouter()

	regularAdmin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	adminTargetUser := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	request := users_dto.ChangeUserRoleRequestDTO{
		Role: users_enums.UserRoleMember,
	}

	resp := test_utils.MakePutRequest(
		t,
		router,
		"/api/v1/users/"+adminTargetUser.UserID.String()+"/role",
		"Bearer "+regularAdmin.Token,
		request,
		http.StatusBadRequest,
	)
	assert.Contains(t, string(resp.Body), "only the root admin user can promote users to admin or demote admin users")
}

func Test_DeactivateUser_WhenRegularAdminDeactivatesAdmin_ReturnsBadRequest(t *testing.T) {
	router := createManagementTestRouter()

	regularAdmin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	adminTargetUser := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	resp := test_utils.MakePostRequest(
		t,
		router,
		"/api/v1/users/"+adminTargetUser.UserID.String()+"/deactivate",
		"Bearer "+regularAdmin.Token,
		nil,
		http.StatusBadRequest,
	)
	assert.Contains(t, string(resp.Body), "only the root admin user can deactivate admin accounts")
}

func Test_ActivateUser_WhenRegularAdminActivatesAdmin_ReturnsBadRequest(t *testing.T) {
	router := createManagementTestRouter()

	regularAdmin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	adminTargetUser := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	resp := test_utils.MakePostRequest(
		t,
		router,
		"/api/v1/users/"+adminTargetUser.UserID.String()+"/activate",
		"Bearer "+regularAdmin.Token,
		nil,
		http.StatusBadRequest,
	)
	assert.Contains(t, string(resp.Body), "only the root admin user can activate admin accounts")
}

func Test_ChangeUserRole_WhenRootAdminPromotesToAdmin_RoleChanged(t *testing.T) {
	router := createManagementTestRouter()

	rootAdmin := users_testing.ReacreateInitAdminAndGetAccess()
	targetUser := users_testing.CreateTestUser(users_enums.UserRoleMember)

	request := users_dto.ChangeUserRoleRequestDTO{
		Role: users_enums.UserRoleAdmin,
	}

	resp := test_utils.MakePutRequest(
		t,
		router,
		"/api/v1/users/"+targetUser.UserID.String()+"/role",
		"Bearer "+rootAdmin.Token,
		request,
		http.StatusOK,
	)
	assert.Contains(t, string(resp.Body), "User role changed successfully")
}

func Test_ChangeUserRole_WhenRootAdminDemotesAdmin_RoleChanged(t *testing.T) {
	router := createManagementTestRouter()

	rootAdmin := users_testing.ReacreateInitAdminAndGetAccess()
	adminTargetUser := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	request := users_dto.ChangeUserRoleRequestDTO{
		Role: users_enums.UserRoleMember,
	}

	resp := test_utils.MakePutRequest(
		t,
		router,
		"/api/v1/users/"+adminTargetUser.UserID.String()+"/role",
		"Bearer "+rootAdmin.Token,
		request,
		http.StatusOK,
	)
	assert.Contains(t, string(resp.Body), "User role changed successfully")
}

func Test_DeactivateUser_WhenRootAdminDeactivatesAdmin_UserDeactivated(t *testing.T) {
	router := createManagementTestRouter()

	rootAdmin := users_testing.ReacreateInitAdminAndGetAccess()
	adminTargetUser := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	resp := test_utils.MakePostRequest(
		t,
		router,
		"/api/v1/users/"+adminTargetUser.UserID.String()+"/deactivate",
		"Bearer "+rootAdmin.Token,
		nil,
		http.StatusOK,
	)
	assert.Contains(t, string(resp.Body), "User deactivated successfully")
}

func Test_ActivateUser_WhenRootAdminActivatesAdmin_UserActivated(t *testing.T) {
	router := createManagementTestRouter()

	rootAdmin := users_testing.ReacreateInitAdminAndGetAccess()
	adminTargetUser := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	test_utils.MakePostRequest(
		t,
		router,
		"/api/v1/users/"+adminTargetUser.UserID.String()+"/deactivate",
		"Bearer "+rootAdmin.Token,
		nil,
		http.StatusOK,
	)

	resp := test_utils.MakePostRequest(
		t,
		router,
		"/api/v1/users/"+adminTargetUser.UserID.String()+"/activate",
		"Bearer "+rootAdmin.Token,
		nil,
		http.StatusOK,
	)
	assert.Contains(t, string(resp.Body), "User activated successfully")
}

func Test_ChangeUserRole_WhenRootAdminChangesOwnRole_ReturnsBadRequest(t *testing.T) {
	router := createManagementTestRouter()

	rootAdmin := users_testing.ReacreateInitAdminAndGetAccess()

	request := users_dto.ChangeUserRoleRequestDTO{
		Role: users_enums.UserRoleMember,
	}

	resp := test_utils.MakePutRequest(
		t,
		router,
		"/api/v1/users/"+rootAdmin.UserID.String()+"/role",
		"Bearer "+rootAdmin.Token,
		request,
		http.StatusBadRequest,
	)
	assert.Contains(t, string(resp.Body), "cannot change your own role")
}

func Test_DeactivateUser_WhenRootAdminDeactivatesOwnAccount_ReturnsBadRequest(t *testing.T) {
	router := createManagementTestRouter()

	rootAdmin := users_testing.ReacreateInitAdminAndGetAccess()

	resp := test_utils.MakePostRequest(
		t,
		router,
		"/api/v1/users/"+rootAdmin.UserID.String()+"/deactivate",
		"Bearer "+rootAdmin.Token,
		nil,
		http.StatusBadRequest,
	)
	assert.Contains(t, string(resp.Body), "cannot deactivate your own account")
}

func Test_InviteUserToProject_MembershipReceivedAfterSignUp(t *testing.T) {
	router := createInviteProjectTestRouter()
	users_testing.EnableMemberInvitations()
	users_testing.EnableExternalRegistrations()
	defer users_testing.ResetSettingsToDefaults()

	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectViaAPI("Invite Test Project", owner, router)

	inviteEmail := "invited-" + uuid.New().String() + "@example.com"
	inviteResponse := projects_testing.InviteMemberToProject(
		project,
		inviteEmail,
		users_enums.ProjectRoleMember,
		owner.Token,
		router,
	)

	assert.Equal(t, projects_dto.AddStatusInvited, inviteResponse.Status)
	require.NotEmpty(t, inviteResponse.InviteToken)

	// The invite token, not just the matching email, authorizes claiming the account.
	signUpRequest := users_dto.SignUpRequestDTO{
		Email:       inviteEmail,
		Password:    "testpassword123",
		Name:        "Invited User",
		InviteToken: inviteResponse.InviteToken,
	}

	resp := test_utils.MakePostRequest(
		t,
		router,
		"/api/v1/users/signup",
		"",
		signUpRequest,
		http.StatusOK,
	)
	assert.Contains(t, string(resp.Body), "User created successfully")

	signInRequest := users_dto.SignInRequestDTO{
		Email:    inviteEmail,
		Password: "testpassword123",
	}

	var signInResponse users_dto.SignInResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t,
		router,
		"/api/v1/users/signin",
		"",
		signInRequest,
		http.StatusOK,
		&signInResponse,
	)

	var membersResponse projects_dto.GetMembersResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t,
		router,
		"/api/v1/projects/memberships/"+project.ID.String()+"/members",
		"Bearer "+signInResponse.Token,
		http.StatusOK,
		&membersResponse,
	)

	var foundMember *projects_dto.ProjectMemberResponseDTO
	for _, member := range membersResponse.Members {
		if member.UserID == signInResponse.UserID {
			foundMember = &member
			break
		}
	}

	assert.NotNil(t, foundMember, "Invited user should be automatically added as project member after sign up")
	assert.Equal(t, users_enums.ProjectRoleMember, foundMember.Role)
	assert.Equal(t, inviteEmail, foundMember.Email)
}

func Test_SignUp_ForInvitedEmail_WithoutInviteToken_ReturnsBadRequest(t *testing.T) {
	router := createInviteProjectTestRouter()
	users_testing.EnableMemberInvitations()
	users_testing.EnableExternalRegistrations()
	defer users_testing.ResetSettingsToDefaults()

	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectViaAPI("Invite Token Test Project", owner, router)

	inviteEmail := "invited-" + uuid.New().String() + "@example.com"
	inviteResponse := projects_testing.InviteMemberToProject(
		project, inviteEmail, users_enums.ProjectRoleMember, owner.Token, router,
	)
	require.Equal(t, projects_dto.AddStatusInvited, inviteResponse.Status)

	// Knowing the invited email alone must not claim the account and its pending role.
	resp := test_utils.MakePostRequest(
		t,
		router,
		"/api/v1/users/signup",
		"",
		users_dto.SignUpRequestDTO{Email: inviteEmail, Password: "attackerpassword123", Name: "Attacker"},
		http.StatusBadRequest,
	)
	assert.Contains(t, string(resp.Body), "invalid or expired invite")
}

func Test_SignUp_ForInvitedEmail_WithWrongInviteToken_ReturnsBadRequest(t *testing.T) {
	router := createInviteProjectTestRouter()
	users_testing.EnableMemberInvitations()
	users_testing.EnableExternalRegistrations()
	defer users_testing.ResetSettingsToDefaults()

	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectViaAPI("Invite Token Test Project 2", owner, router)

	inviteEmail := "invited-" + uuid.New().String() + "@example.com"
	inviteResponse := projects_testing.InviteMemberToProject(
		project, inviteEmail, users_enums.ProjectRoleMember, owner.Token, router,
	)
	require.Equal(t, projects_dto.AddStatusInvited, inviteResponse.Status)

	resp := test_utils.MakePostRequest(
		t,
		router,
		"/api/v1/users/signup",
		"",
		users_dto.SignUpRequestDTO{
			Email: inviteEmail, Password: "attackerpassword123", Name: "Attacker",
			InviteToken: "0000000000000000000000000000000000000000000000000000000000000000",
		},
		http.StatusBadRequest,
	)
	assert.Contains(t, string(resp.Body), "invalid or expired invite")
}

func Test_SetUserPassword_WhenUserIsAdmin_PasswordSet(t *testing.T) {
	router := createManagementTestRouter()

	adminUser := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	targetUser := users_testing.CreateTestUser(users_enums.UserRoleMember)

	request := users_dto.SetUserPasswordRequestDTO{
		Password: "brand-new-password-123",
	}

	resp := test_utils.MakePostRequest(
		t,
		router,
		"/api/v1/users/"+targetUser.UserID.String()+"/set-password",
		"Bearer "+adminUser.Token,
		request,
		http.StatusOK,
	)
	assert.Contains(t, string(resp.Body), "Password set successfully")
}

func Test_SetUserPassword_WhenUserIsMember_ReturnsForbidden(t *testing.T) {
	router := createManagementTestRouter()

	member := users_testing.CreateTestUser(users_enums.UserRoleMember)
	targetUser := users_testing.CreateTestUser(users_enums.UserRoleMember)

	request := users_dto.SetUserPasswordRequestDTO{
		Password: "brand-new-password-123",
	}

	resp := test_utils.MakePostRequest(
		t,
		router,
		"/api/v1/users/"+targetUser.UserID.String()+"/set-password",
		"Bearer "+member.Token,
		request,
		http.StatusForbidden,
	)
	assert.Contains(t, string(resp.Body), "permissions")
}

func Test_SetUserPassword_WithTooShortPassword_ReturnsBadRequest(t *testing.T) {
	router := createManagementTestRouter()

	adminUser := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	targetUser := users_testing.CreateTestUser(users_enums.UserRoleMember)

	request := users_dto.SetUserPasswordRequestDTO{
		Password: "short",
	}

	resp := test_utils.MakePostRequest(
		t,
		router,
		"/api/v1/users/"+targetUser.UserID.String()+"/set-password",
		"Bearer "+adminUser.Token,
		request,
		http.StatusBadRequest,
	)
	assert.Contains(t, string(resp.Body), "Invalid request format")
}

func Test_SetUserPassword_WhenRegularAdminTargetsAdmin_ReturnsBadRequest(t *testing.T) {
	router := createManagementTestRouter()

	regularAdmin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	adminTargetUser := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	request := users_dto.SetUserPasswordRequestDTO{
		Password: "brand-new-password-123",
	}

	resp := test_utils.MakePostRequest(
		t,
		router,
		"/api/v1/users/"+adminTargetUser.UserID.String()+"/set-password",
		"Bearer "+regularAdmin.Token,
		request,
		http.StatusBadRequest,
	)
	assert.Contains(t, string(resp.Body), "only the root admin user can set an admin account's password")
}

func Test_SetUserPassword_WhenRootAdminTargetsAdmin_PasswordSet(t *testing.T) {
	router := createManagementTestRouter()

	rootAdmin := users_testing.ReacreateInitAdminAndGetAccess()
	adminTargetUser := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	request := users_dto.SetUserPasswordRequestDTO{
		Password: "brand-new-password-123",
	}

	resp := test_utils.MakePostRequest(
		t,
		router,
		"/api/v1/users/"+adminTargetUser.UserID.String()+"/set-password",
		"Bearer "+rootAdmin.Token,
		request,
		http.StatusOK,
	)
	assert.Contains(t, string(resp.Body), "Password set successfully")
}

// Without SMTP, an admin setting the password is how an invited user gets in:
// it activates the account and the password works for sign-in.
func Test_SetUserPassword_ForInvitedUser_ActivatesAndAllowsSignIn(t *testing.T) {
	router := createInviteProjectTestRouter()

	adminUser := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	inviteEmail := "invited-" + uuid.New().String() + "@example.com"

	var inviteResponse users_dto.InviteUserResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t,
		router,
		"/api/v1/users/invite",
		"Bearer "+adminUser.Token,
		users_dto.InviteUserRequestDTO{Email: inviteEmail},
		http.StatusOK,
		&inviteResponse,
	)

	setPasswordRequest := users_dto.SetUserPasswordRequestDTO{
		Password: "admin-assigned-password-123",
	}
	resp := test_utils.MakePostRequest(
		t,
		router,
		"/api/v1/users/"+inviteResponse.ID.String()+"/set-password",
		"Bearer "+adminUser.Token,
		setPasswordRequest,
		http.StatusOK,
	)
	assert.Contains(t, string(resp.Body), "Password set successfully")

	var profile users_dto.UserProfileResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t,
		router,
		"/api/v1/users/"+inviteResponse.ID.String(),
		"Bearer "+adminUser.Token,
		http.StatusOK,
		&profile,
	)
	assert.True(t, profile.IsActive, "Setting a password for an invited user should activate their account")

	var signInResponse users_dto.SignInResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t,
		router,
		"/api/v1/users/signin",
		"",
		users_dto.SignInRequestDTO{Email: inviteEmail, Password: "admin-assigned-password-123"},
		http.StatusOK,
		&signInResponse,
	)
	assert.Equal(t, inviteEmail, signInResponse.Email)
}

func createInviteProjectTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	v1 := router.Group("/api/v1")

	GetUserController().RegisterRoutes(v1)

	protected := v1.Group("").Use(users_middleware.AuthMiddleware(users_services.GetUserService()))

	GetManagementController().RegisterRoutes(protected.(*gin.RouterGroup))
	GetUserController().RegisterProtectedRoutes(protected.(*gin.RouterGroup))
	projects_controllers.GetProjectController().RegisterRoutes(protected.(*gin.RouterGroup))
	projects_controllers.GetMembershipController().RegisterRoutes(protected.(*gin.RouterGroup))
	audit_logs.SetupDependencies()

	return router
}
