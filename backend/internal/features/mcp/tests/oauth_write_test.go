package mcp_server_tests

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"obzly/internal/features/oauth"
	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_OAuth_Discovery_AdvertisesTheWriteScope(t *testing.T) {
	server := httptest.NewServer(createWriteRouter())
	defer server.Close()

	for _, path := range []string{"/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-authorization-server"} {
		assert.Equal(t, []any{"mcp:read", "mcp:write"}, getJSON(t, server.URL+path)["scopes_supported"], path)
	}
}

func Test_OAuth_WriteScope_IsGrantedOnlyWhenRequestedAndApproved(t *testing.T) {
	router := createWriteRouter()
	server := httptest.NewServer(router)
	defer server.Close()
	clientID := registerClient(t, server.URL)
	// Test admins have MFA on; the Member hasn't.
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	member := users_testing.CreateTestUser(users_enums.UserRoleMember)

	cases := []struct {
		name       string
		user       *users_dto.SignInResponseDTO
		scope      string
		allowWrite bool
		wantScope  string
	}{
		{"write requested, toggle left off", admin, "mcp:read mcp:write", false, "mcp:read"},
		{"toggle on, write not requested", admin, "mcp:read", true, "mcp:read"},
		{"no scope, toggle on", admin, "", true, "mcp:read"},
		{"write requested and approved", admin, "mcp:write", true, "mcp:read mcp:write"},
		{"member without MFA, toggle left off", member, "mcp:read mcp:write", false, "mcp:read"},
	}
	for _, tc := range cases {
		verifier := strings.Repeat("w", 50)
		params := scopedParams(server.URL, clientID, verifier, tc.scope)

		var info oauth.ConsentInfo
		postJSON(t, server.URL+"/api/v1/oauth/authorize/validate", tc.user.Token, params, http.StatusOK, &info)
		assert.Equal(t, strings.Contains(tc.scope, "mcp:write"), info.WriteRequested, tc.name)

		tokens := decideAndExchange(t, server.URL, tc.user, clientID, verifier, params, tc.allowWrite)
		assert.Equal(t, tc.wantScope, tokens["scope"], tc.name)

		tools, err := connect(t, server.URL, tokens["access_token"].(string)).ListTools(context.Background(), nil)
		require.NoError(t, err)
		hasWrite := false
		for _, tool := range tools.Tools {
			hasWrite = hasWrite || tool.Name == "create_silence"
		}
		assert.Equal(t, strings.Contains(tc.wantScope, "mcp:write"), hasWrite, tc.name)

		if strings.Contains(tc.wantScope, "mcp:write") {
			refreshed := exchange(t, server.URL, url.Values{
				"grant_type": {"refresh_token"}, "client_id": {clientID},
				"refresh_token": {tokens["refresh_token"].(string)},
			}, http.StatusOK)
			assert.Equal(t, "mcp:read mcp:write", refreshed["scope"], "a refresh keeps the grant")
		}
	}
}

func Test_OAuth_WriteApprovedWithoutMFA_IsRefused(t *testing.T) {
	router := createWriteRouter()
	server := httptest.NewServer(router)
	defer server.Close()
	clientID := registerClient(t, server.URL)
	member := users_testing.CreateTestUser(users_enums.UserRoleMember)
	params := scopedParams(server.URL, clientID, strings.Repeat("m", 50), "mcp:read mcp:write")

	var info oauth.ConsentInfo
	postJSON(t, server.URL+"/api/v1/oauth/authorize/validate", member.Token, params, http.StatusOK, &info)
	assert.True(t, info.WriteRequested)
	assert.True(t, info.WriteNeedsMFA)

	params["approve"], params["allowWrite"] = true, true
	var body map[string]any
	postJSON(t, server.URL+"/api/v1/oauth/authorize/decision", member.Token, params, http.StatusBadRequest, &body)
	assert.Contains(t, body["error"], "two-factor")
}

func Test_OAuth_UnknownScope_IsSentBackToTheClient(t *testing.T) {
	router := createWriteRouter()
	server := httptest.NewServer(router)
	defer server.Close()
	clientID := registerClient(t, server.URL)
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)

	var body map[string]any
	postJSON(t, server.URL+"/api/v1/oauth/authorize/validate", user.Token,
		scopedParams(server.URL, clientID, strings.Repeat("u", 50), "mcp:admin"), http.StatusBadRequest, &body)
	assert.Equal(t, false, body["fatal"])
	assert.Contains(t, body["redirectTo"], "error=invalid_scope")
}

func scopedParams(baseURL, clientID, verifier, scope string) map[string]any {
	params := authorizeParams(clientID, redirectURI, verifier)
	params["scope"] = scope
	params["resource"] = baseURL + "/mcp"
	return params
}

func decideAndExchange(
	t *testing.T, baseURL string, user *users_dto.SignInResponseDTO, clientID, verifier string,
	params map[string]any, allowWrite bool,
) map[string]any {
	t.Helper()
	decision := map[string]any{"approve": true, "allowWrite": allowWrite}
	for key, value := range params {
		decision[key] = value
	}
	var response struct {
		RedirectTo string `json:"redirectTo"`
	}
	postJSON(t, baseURL+"/api/v1/oauth/authorize/decision", user.Token, decision, http.StatusOK, &response)
	redirect, err := url.Parse(response.RedirectTo)
	require.NoError(t, err)
	return exchange(t, baseURL, url.Values{
		"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {redirect.Query().Get("code")},
		"redirect_uri": {redirectURI}, "code_verifier": {verifier},
	}, http.StatusOK)
}
