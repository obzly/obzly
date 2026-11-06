package users_controllers

import (
	"net/http"
	"testing"
	"time"

	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func Test_GetUsersList_AfterASignIn_ShowsLastLoginAndLastActive(t *testing.T) {
	router := createManagementTestRouter()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	member := users_testing.CreateTestUser(users_enums.UserRoleMember)

	listed := findListedUser(t, router, admin.Token, member.Email, member.UserID)

	require.NotNil(t, listed.LastLoginAt)
	require.NotNil(t, listed.LastActiveAt)
	require.WithinDuration(t, time.Now().UTC(), *listed.LastLoginAt, time.Minute)
}

func Test_GetUsersList_ForAnInvitedUserWhoNeverSignedIn_ShowsNoLastLogin(t *testing.T) {
	inviteRouter := createInviteProjectTestRouter()
	router := createManagementTestRouter()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	email := "never-signed-in-" + uuid.New().String() + "@example.com"
	var invited users_dto.InviteUserResponseDTO
	test_utils.MakePostRequestAndUnmarshal(t, inviteRouter, "/api/v1/users/invite", "Bearer "+admin.Token,
		users_dto.InviteUserRequestDTO{Email: email}, http.StatusOK, &invited)

	listed := findListedUser(t, router, admin.Token, email, invited.ID)

	require.Nil(t, listed.LastLoginAt)
	require.Nil(t, listed.LastActiveAt)
}

func findListedUser(
	t *testing.T, router *gin.Engine, adminToken, email string, id uuid.UUID,
) users_dto.UserProfileResponseDTO {
	t.Helper()
	var response users_dto.ListUsersResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/users?query="+email, "Bearer "+adminToken,
		http.StatusOK, &response)
	for _, user := range response.Users {
		if user.ID == id {
			return user
		}
	}
	t.Fatalf("user %s not in the list", email)
	return users_dto.UserProfileResponseDTO{}
}
