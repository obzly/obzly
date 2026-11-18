package users_controllers

import (
	"net/http"
	"testing"

	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_GetCurrentUser_WhenNoPreferencesSaved_OmitsUIPreferences(t *testing.T) {
	router := createUserTestRouter()
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)

	resp := test_utils.MakeGetRequest(t, router, "/api/v1/users/me", "Bearer "+user.Token, http.StatusOK)

	assert.NotContains(t, string(resp.Body), "uiPreferences")
}

func Test_UpdateUIPreferences_WithValidTheme_SavedAndReturnedByUsersMe(t *testing.T) {
	router := createUserTestRouter()
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)

	request := map[string]any{"theme": map[string]any{
		"mode": "dark", "light": "soft-light", "dark": "midnight", "colorBlindCharts": true,
	}}
	var saved users_models.UIPreferences
	test_utils.MakePutRequestAndUnmarshal(
		t, router, "/api/v1/users/me/preferences", "Bearer "+user.Token, request, http.StatusOK, &saved,
	)
	require.NotNil(t, saved.Theme)
	assert.Equal(t, "midnight", saved.Theme.Dark)

	var profile users_dto.UserProfileResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/users/me", "Bearer "+user.Token, http.StatusOK, &profile)

	require.NotNil(t, profile.UIPreferences)
	assert.Equal(t, &users_models.ThemePreferences{
		Mode: "dark", Light: "soft-light", Dark: "midnight", ColorBlindCharts: true,
	}, profile.UIPreferences.Theme)
}

func Test_UpdateUIPreferences_WithUnknownFields_StoresOnlyValidatedFields(t *testing.T) {
	router := createUserTestRouter()
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)

	request := map[string]any{
		"theme": map[string]any{
			"mode": "system", "light": "semi-dark", "dark": "black", "accent": "<script>",
		},
		"somethingElse": "x",
	}
	test_utils.MakePutRequest(t, router, "/api/v1/users/me/preferences", "Bearer "+user.Token, request, http.StatusOK)

	resp := test_utils.MakeGetRequest(t, router, "/api/v1/users/me", "Bearer "+user.Token, http.StatusOK)
	assert.NotContains(t, string(resp.Body), "accent")
	assert.NotContains(t, string(resp.Body), "somethingElse")
	assert.Contains(t, string(resp.Body), `"light":"semi-dark"`)
}

func Test_UpdateUIPreferences_WithInvalidThemes_ReturnsBadRequest(t *testing.T) {
	router := createUserTestRouter()
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)

	cases := map[string]map[string]any{
		"unknown mode":        {"mode": "sepia", "light": "emerald-light", "dark": "emerald-dark"},
		"dark id as light":    {"mode": "light", "light": "midnight", "dark": "emerald-dark"},
		"light id as dark":    {"mode": "dark", "light": "emerald-light", "dark": "soft-light"},
		"unknown theme id":    {"mode": "dark", "light": "emerald-light", "dark": "no-such"},
		"missing theme field": {"mode": "dark"},
	}
	for name, theme := range cases {
		t.Run(name, func(t *testing.T) {
			test_utils.MakePutRequest(
				t, router, "/api/v1/users/me/preferences", "Bearer "+user.Token,
				map[string]any{"theme": theme}, http.StatusBadRequest,
			)
		})
	}
	test_utils.MakePutRequest(
		t, router, "/api/v1/users/me/preferences", "Bearer "+user.Token, map[string]any{}, http.StatusBadRequest,
	)

	resp := test_utils.MakeGetRequest(t, router, "/api/v1/users/me", "Bearer "+user.Token, http.StatusOK)
	assert.NotContains(t, string(resp.Body), "uiPreferences")
}

func Test_UpdateUIPreferences_ChangesOnlyTheCallersRecord(t *testing.T) {
	router := createUserTestRouter()
	caller := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	other := users_testing.CreateTestUser(users_enums.UserRoleMember)

	request := map[string]any{
		"id":     other.UserID,
		"userId": other.UserID,
		"theme":  map[string]any{"mode": "light", "light": "soft-light", "dark": "black"},
	}
	test_utils.MakePutRequest(t, router, "/api/v1/users/me/preferences", "Bearer "+caller.Token, request, http.StatusOK)

	otherResp := test_utils.MakeGetRequest(t, router, "/api/v1/users/me", "Bearer "+other.Token, http.StatusOK)
	assert.NotContains(t, string(otherResp.Body), "uiPreferences")

	callerResp := test_utils.MakeGetRequest(t, router, "/api/v1/users/me", "Bearer "+caller.Token, http.StatusOK)
	assert.Contains(t, string(callerResp.Body), `"light":"soft-light"`)
}

func Test_UpdateUIPreferences_WithoutAuth_ReturnsUnauthorized(t *testing.T) {
	router := createUserTestRouter()

	test_utils.MakePutRequest(t, router, "/api/v1/users/me/preferences", "",
		map[string]any{"theme": map[string]any{"mode": "light", "light": "soft-light", "dark": "black"}},
		http.StatusUnauthorized)
}
