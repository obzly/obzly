package users_controllers

import (
	"net/http"
	"testing"

	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/stretchr/testify/assert"
)

func Test_GetUserSettings_WhenUserIsAdmin_ReturnsSettings(t *testing.T) {
	users_testing.ResetSettingsToDefaults()
	router := createSettingsTestRouter()

	testUser := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	var response users_models.UsersSettings
	test_utils.MakeGetRequestAndUnmarshal(
		t,
		router,
		"/api/v1/users/settings",
		"Bearer "+testUser.Token,
		http.StatusOK,
		&response,
	)

	assert.True(t, response.IsAllowExternalRegistrations)
	assert.True(t, response.IsAllowMemberInvitations)
	assert.True(t, response.IsMemberAllowedToCreateProjects)
}

func Test_GetUserSettings_WhenUserIsMember_ReturnsSettings(t *testing.T) {
	users_testing.ResetSettingsToDefaults()
	router := createSettingsTestRouter()

	testUser := users_testing.CreateTestUser(users_enums.UserRoleMember)

	_ = test_utils.MakeGetRequest(
		t,
		router,
		"/api/v1/users/settings",
		"Bearer "+testUser.Token,
		http.StatusOK,
	)
}

func Test_GetUserSettings_WithoutAuth_ReturnsUnauthorized(t *testing.T) {
	users_testing.ResetSettingsToDefaults()
	router := createSettingsTestRouter()

	test_utils.MakeGetRequest(t, router, "/api/v1/users/settings", "", http.StatusUnauthorized)
}

func Test_UpdateUserSettings_WhenUserIsAdmin_SettingsUpdated(t *testing.T) {
	users_testing.ResetSettingsToDefaults()
	router := createSettingsTestRouter()

	testUser := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	request := users_models.UsersSettings{
		IsAllowExternalRegistrations:    false,
		IsAllowMemberInvitations:        true,
		IsMemberAllowedToCreateProjects: false,
	}

	var response users_models.UsersSettings
	test_utils.MakePutRequestAndUnmarshal(
		t,
		router,
		"/api/v1/users/settings",
		"Bearer "+testUser.Token,
		request,
		http.StatusOK,
		&response,
	)

	assert.False(t, response.IsAllowExternalRegistrations)
	assert.True(t, response.IsAllowMemberInvitations)
	assert.False(t, response.IsMemberAllowedToCreateProjects)
}

func Test_UpdateUserSettings_WithPartialData_SettingsUpdated(t *testing.T) {
	users_testing.ResetSettingsToDefaults()
	router := createSettingsTestRouter()

	testUser := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	request := users_models.UsersSettings{
		IsAllowExternalRegistrations:    false,
		IsAllowMemberInvitations:        true,
		IsMemberAllowedToCreateProjects: true,
	}

	var response users_models.UsersSettings
	test_utils.MakePutRequestAndUnmarshal(
		t,
		router,
		"/api/v1/users/settings",
		"Bearer "+testUser.Token,
		request,
		http.StatusOK,
		&response,
	)

	assert.False(t, response.IsAllowExternalRegistrations)
	assert.True(t, response.IsAllowMemberInvitations)
	assert.True(t, response.IsMemberAllowedToCreateProjects)
}

func Test_UpdateUserSettings_WhenUserIsMember_ReturnsForbidden(t *testing.T) {
	users_testing.ResetSettingsToDefaults()
	router := createSettingsTestRouter()

	testUser := users_testing.CreateTestUser(users_enums.UserRoleMember)

	request := users_models.UsersSettings{
		IsAllowExternalRegistrations: false,
	}

	resp := test_utils.MakePutRequest(
		t,
		router,
		"/api/v1/users/settings",
		"Bearer "+testUser.Token,
		request,
		http.StatusForbidden,
	)
	assert.Contains(t, string(resp.Body), "Insufficient permissions")
}

func Test_UpdateUserSettings_WithInvalidJSON_ReturnsBadRequest(t *testing.T) {
	users_testing.ResetSettingsToDefaults()
	router := createSettingsTestRouter()

	testUser := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	resp := test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method:         "PUT",
		URL:            "/api/v1/users/settings",
		Body:           "invalid json",
		AuthToken:      "Bearer " + testUser.Token,
		ExpectedStatus: http.StatusBadRequest,
	})

	assert.Contains(t, string(resp.Body), "Invalid request format")
}

func Test_UpdateUserSettings_WithoutAuth_ReturnsUnauthorized(t *testing.T) {
	users_testing.ResetSettingsToDefaults()
	router := createSettingsTestRouter()

	request := users_models.UsersSettings{
		IsAllowExternalRegistrations: false,
	}

	test_utils.MakePutRequest(t, router, "/api/v1/users/settings", "", request, http.StatusUnauthorized)
}

func Test_GetUserSettings_ThemeDefaults_ReturnsSafeDefaults(t *testing.T) {
	users_testing.ResetSettingsToDefaults()
	router := createSettingsTestRouter()
	testUser := users_testing.CreateTestUser(users_enums.UserRoleMember)

	var response users_models.UsersSettings
	test_utils.MakeGetRequestAndUnmarshal(
		t, router, "/api/v1/users/settings", "Bearer "+testUser.Token, http.StatusOK, &response,
	)

	assert.Equal(t, "system", response.DefaultThemeMode)
	assert.Equal(t, "emerald-light", response.DefaultLightTheme)
	assert.Equal(t, "emerald-dark", response.DefaultDarkTheme)
	assert.True(t, response.IsUserThemeChoiceAllowed)
}

func Test_UpdateUserSettings_WithThemeDefaults_SavedAndReadBack(t *testing.T) {
	users_testing.ResetSettingsToDefaults()
	defer users_testing.ResetSettingsToDefaults()
	router := createSettingsTestRouter()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	request := map[string]any{
		"isAllowExternalRegistrations":    true,
		"isAllowMemberInvitations":        true,
		"isMemberAllowedToCreateProjects": true,
		"defaultThemeMode":                "dark",
		"defaultLightTheme":               "soft-light",
		"defaultDarkTheme":                "black",
		"isUserThemeChoiceAllowed":        false,
	}
	test_utils.MakePutRequest(t, router, "/api/v1/users/settings", "Bearer "+admin.Token, request, http.StatusOK)

	var response users_models.UsersSettings
	test_utils.MakeGetRequestAndUnmarshal(
		t, router, "/api/v1/users/settings", "Bearer "+admin.Token, http.StatusOK, &response,
	)
	assert.Equal(t, "dark", response.DefaultThemeMode)
	assert.Equal(t, "soft-light", response.DefaultLightTheme)
	assert.Equal(t, "black", response.DefaultDarkTheme)
	assert.False(t, response.IsUserThemeChoiceAllowed)
}

func Test_UpdateUserSettings_WithoutThemeFields_KeepsStoredThemeDefaults(t *testing.T) {
	users_testing.ResetSettingsToDefaults()
	defer users_testing.ResetSettingsToDefaults()
	router := createSettingsTestRouter()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	test_utils.MakePutRequest(t, router, "/api/v1/users/settings", "Bearer "+admin.Token, map[string]any{
		"isAllowExternalRegistrations": true, "defaultDarkTheme": "midnight", "isUserThemeChoiceAllowed": false,
	}, http.StatusOK)

	var response users_models.UsersSettings
	test_utils.MakePutRequestAndUnmarshal(t, router, "/api/v1/users/settings", "Bearer "+admin.Token,
		map[string]any{"isAllowExternalRegistrations": true}, http.StatusOK, &response)

	assert.Equal(t, "midnight", response.DefaultDarkTheme)
	assert.Equal(t, "system", response.DefaultThemeMode)
	assert.False(t, response.IsUserThemeChoiceAllowed)
}

func Test_UpdateUserSettings_WithInvalidThemeDefaults_ReturnsBadRequest(t *testing.T) {
	users_testing.ResetSettingsToDefaults()
	defer users_testing.ResetSettingsToDefaults()
	router := createSettingsTestRouter()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	cases := map[string]map[string]any{
		"unknown mode":     {"defaultThemeMode": "sepia"},
		"dark id as light": {"defaultLightTheme": "black"},
		"light id as dark": {"defaultDarkTheme": "semi-dark"},
		"unknown theme":    {"defaultDarkTheme": "no-such"},
	}
	for name, request := range cases {
		t.Run(name, func(t *testing.T) {
			test_utils.MakePutRequest(t, router, "/api/v1/users/settings", "Bearer "+admin.Token, request,
				http.StatusBadRequest)
		})
	}

	var response users_models.UsersSettings
	test_utils.MakeGetRequestAndUnmarshal(
		t, router, "/api/v1/users/settings", "Bearer "+admin.Token, http.StatusOK, &response,
	)
	assert.Equal(t, "system", response.DefaultThemeMode)
	assert.Equal(t, "emerald-light", response.DefaultLightTheme)
	assert.Equal(t, "emerald-dark", response.DefaultDarkTheme)
}

func Test_UpdateUserSettings_ThemeDefaultsAsMember_ReturnsForbidden(t *testing.T) {
	users_testing.ResetSettingsToDefaults()
	defer users_testing.ResetSettingsToDefaults()
	router := createSettingsTestRouter()
	member := users_testing.CreateTestUser(users_enums.UserRoleMember)

	test_utils.MakePutRequest(t, router, "/api/v1/users/settings", "Bearer "+member.Token,
		map[string]any{"defaultDarkTheme": "black", "isUserThemeChoiceAllowed": false}, http.StatusForbidden)

	var response users_models.UsersSettings
	test_utils.MakeGetRequestAndUnmarshal(
		t, router, "/api/v1/users/settings", "Bearer "+member.Token, http.StatusOK, &response,
	)
	assert.Equal(t, "emerald-dark", response.DefaultDarkTheme)
	assert.True(t, response.IsUserThemeChoiceAllowed)
}
