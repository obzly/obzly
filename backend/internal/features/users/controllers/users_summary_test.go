package users_controllers

import (
	"net/http"
	"testing"
	"time"

	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	"obzly/internal/storage"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_GetUsersSummary_WhenUserIsMember_ReturnsForbidden(t *testing.T) {
	router := createManagementTestRouter()
	member := users_testing.CreateTestUser(users_enums.UserRoleMember)

	test_utils.MakeGetRequest(t, router, "/api/v1/users/summary", "Bearer "+member.Token, http.StatusForbidden)
}

// Other packages add users to the shared database concurrently, so this
// checks lower bounds and the summary's own consistency, not exact counts.
func Test_GetUsersSummary_WhenUserIsAdmin_CountsUsers(t *testing.T) {
	router := createManagementTestRouter()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	seen := users_testing.CreateTestUser(users_enums.UserRoleMember)
	require.NoError(t, storage.GetDb().Exec("UPDATE users SET last_active_at = ? WHERE id = ?",
		time.Now().UTC(), seen.UserID).Error)

	var summary users_dto.UsersSummaryResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/users/summary", "Bearer "+admin.Token,
		http.StatusOK, &summary)

	assert.Equal(t, summary.Total, summary.ActiveAccounts+summary.Invited+summary.Deactivated)
	assert.GreaterOrEqual(t, summary.ActiveAccounts, int64(2))
	assert.GreaterOrEqual(t, summary.ActiveLast7Days, int64(1))
	assert.GreaterOrEqual(t, summary.MFAEnabled, int64(1), "test admins have MFA")
	assert.LessOrEqual(t, summary.MFAEnabled, summary.ActiveAccounts)
	assert.GreaterOrEqual(t, summary.Admins, int64(1))
	assert.LessOrEqual(t, summary.AdminsWithoutMFA, summary.Admins)
	assert.GreaterOrEqual(t, summary.Inactive, summary.Invited+summary.Deactivated)
}

func Test_GetUsersList_WithFilters_FiltersServerSide(t *testing.T) {
	router := createManagementTestRouter()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	tag := "filters-" + uuid.NewString()[:8]
	now := time.Now().UTC()

	// Four users sharing a name tag, so `query` scopes the list to them.
	recentAdmin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)                  // MFA on, seen now
	recentMember := users_testing.CreateTestUserWithoutMFA(users_enums.UserRoleMember)      // no MFA, seen now
	staleMember := users_testing.CreateTestUserWithoutMFA(users_enums.UserRoleMember)       // no MFA, 40 days ago
	deactivatedMember := users_testing.CreateTestUserWithoutMFA(users_enums.UserRoleMember) // no MFA, deactivated
	db := storage.GetDb()
	for _, id := range []uuid.UUID{recentAdmin.UserID, recentMember.UserID, staleMember.UserID, deactivatedMember.UserID} {
		require.NoError(t, db.Exec("UPDATE users SET name = ?, last_active_at = ? WHERE id = ?", tag, now, id).Error)
	}
	require.NoError(t, db.Exec("UPDATE users SET last_active_at = ?, last_login_at = ? WHERE id = ?",
		now.Add(-40*24*time.Hour), now.Add(-40*24*time.Hour), staleMember.UserID).Error)
	require.NoError(t, db.Exec("UPDATE users SET status = 'INACTIVE' WHERE id = ?", deactivatedMember.UserID).Error)

	list := func(filters string) []uuid.UUID {
		var response users_dto.ListUsersResponseDTO
		test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/users?query="+tag+filters,
			"Bearer "+admin.Token, http.StatusOK, &response)
		assert.Equal(t, int64(len(response.Users)), response.Total, filters)
		ids := make([]uuid.UUID, 0, len(response.Users))
		for _, user := range response.Users {
			ids = append(ids, user.ID)
		}
		return ids
	}

	assert.Len(t, list(""), 4)
	assert.ElementsMatch(t, []uuid.UUID{recentAdmin.UserID}, list("&role=ADMIN"))
	assert.ElementsMatch(t, []uuid.UUID{recentAdmin.UserID}, list("&mfa=on"))
	assert.ElementsMatch(t, []uuid.UUID{recentMember.UserID, staleMember.UserID}, list("&mfa=off&status=ACTIVE"))
	assert.ElementsMatch(t, []uuid.UUID{staleMember.UserID, deactivatedMember.UserID}, list("&activity=inactive"))
	assert.ElementsMatch(t, []uuid.UUID{recentAdmin.UserID, recentMember.UserID}, list("&activity=active"))
	var page users_dto.ListUsersResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/users?query="+tag+"&activity=inactive&limit=1",
		"Bearer "+admin.Token, http.StatusOK, &page)
	assert.Len(t, page.Users, 1)
	assert.Equal(t, int64(2), page.Total, "the total counts the filtered list, not the page")

	for _, bad := range []string{"&role=OWNER", "&mfa=maybe", "&status=GONE", "&activity=idle"} {
		test_utils.MakeGetRequest(t, router, "/api/v1/users?query="+tag+bad, "Bearer "+admin.Token,
			http.StatusBadRequest)
	}

	member := users_testing.CreateTestUser(users_enums.UserRoleMember)
	test_utils.MakeGetRequest(t, router, "/api/v1/users?role=ADMIN", "Bearer "+member.Token, http.StatusForbidden)
}
