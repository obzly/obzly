package mcp_server_tests

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"obzly/internal/features/oauth"
	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const redirectURI = "http://127.0.0.1:43110/callback"

func Test_OAuth_Discovery_AdvertisesServerAndResource(t *testing.T) {
	router := createRouter()
	server := httptest.NewServer(router)
	defer server.Close()

	resp, err := http.Post(server.URL+"/mcp", "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("WWW-Authenticate"),
		`resource_metadata="`+server.URL+`/.well-known/oauth-protected-resource/mcp"`)

	prm := getJSON(t, server.URL+"/.well-known/oauth-protected-resource/mcp")
	assert.Equal(t, server.URL+"/mcp", prm["resource"])
	assert.Equal(t, []any{server.URL}, prm["authorization_servers"])

	as := getJSON(t, server.URL+"/.well-known/oauth-authorization-server")
	assert.Equal(t, server.URL, as["issuer"])
	assert.Equal(t, []any{"S256"}, as["code_challenge_methods_supported"])
	assert.Equal(t, true, as["client_id_metadata_document_supported"])
}

func Test_OAuth_FullFlow_IssuesAudienceBoundTokensThatWorkOnMCP(t *testing.T) {
	router := createRouter()
	server := httptest.NewServer(router)
	defer server.Close()
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	clientID := registerClient(t, server.URL)

	verifier := strings.Repeat("v", 50)
	code, state := authorize(t, router, server.URL, user, clientID, verifier)
	assert.Equal(t, "xyz", state)

	tokens := exchange(t, server.URL, url.Values{
		"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {code},
		"redirect_uri": {redirectURI}, "code_verifier": {verifier}, "resource": {server.URL + "/mcp"},
	}, http.StatusOK)
	accessToken := tokens["access_token"].(string)
	assert.True(t, strings.HasPrefix(accessToken, "obz_oat_"))
	assert.Equal(t, "Bearer", tokens["token_type"])

	session := connect(t, server.URL, accessToken)
	assert.Contains(t, callText(t, session, "list_projects", map[string]any{}), "projects")

	// A code works once.
	exchange(t, server.URL, url.Values{
		"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {code},
		"redirect_uri": {redirectURI}, "code_verifier": {verifier},
	}, http.StatusBadRequest)

	// OAuth tokens are only for /mcp.
	test_utils.MakeGetRequest(t, router, "/api/v1/projects", "Bearer "+accessToken, http.StatusUnauthorized)

	// Bound to this server's resource: the same token for another host is rejected.
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader("{}"))
	req.Host = "other.example.com"
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func Test_OAuth_WrongVerifier_IsRejected(t *testing.T) {
	router := createRouter()
	server := httptest.NewServer(router)
	defer server.Close()
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	clientID := registerClient(t, server.URL)

	code, _ := authorize(t, router, server.URL, user, clientID, strings.Repeat("a", 43))
	body := exchange(t, server.URL, url.Values{
		"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {code},
		"redirect_uri": {redirectURI}, "code_verifier": {strings.Repeat("b", 43)},
	}, http.StatusBadRequest)
	assert.Equal(t, "invalid_grant", body["error"])
}

func Test_OAuth_RefreshRotation_ReuseRevokesTheFamily(t *testing.T) {
	router := createRouter()
	server := httptest.NewServer(router)
	defer server.Close()
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	clientID := registerClient(t, server.URL)
	verifier := strings.Repeat("r", 60)
	code, _ := authorize(t, router, server.URL, user, clientID, verifier)
	first := exchange(t, server.URL, url.Values{
		"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {code},
		"redirect_uri": {redirectURI}, "code_verifier": {verifier},
	}, http.StatusOK)

	second := exchange(t, server.URL, url.Values{
		"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {first["refresh_token"].(string)},
	}, http.StatusOK)
	assert.NotEqual(t, first["refresh_token"], second["refresh_token"])
	assert.Equal(t, http.StatusOK, postMCP(t, server.URL, second["access_token"].(string)))

	// Replaying the rotated token revokes every token of the grant.
	exchange(t, server.URL, url.Values{
		"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {first["refresh_token"].(string)},
	}, http.StatusBadRequest)
	assert.Equal(t, http.StatusUnauthorized, postMCP(t, server.URL, second["access_token"].(string)))
	exchange(t, server.URL, url.Values{
		"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {second["refresh_token"].(string)},
	}, http.StatusBadRequest)
}

func Test_OAuth_ConnectedApp_RevokeStopsItsTokens(t *testing.T) {
	router := createRouter()
	server := httptest.NewServer(router)
	defer server.Close()
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	clientID := registerClient(t, server.URL)
	verifier := strings.Repeat("c", 43)
	code, _ := authorize(t, router, server.URL, user, clientID, verifier)
	tokens := exchange(t, server.URL, url.Values{
		"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {code},
		"redirect_uri": {redirectURI}, "code_verifier": {verifier},
	}, http.StatusOK)

	var apps struct {
		Apps []oauth.ConnectedAppDTO `json:"apps"`
	}
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/oauth/connected-apps", "Bearer "+user.Token,
		http.StatusOK, &apps)
	require.Len(t, apps.Apps, 1)
	assert.Equal(t, "Test Assistant", apps.Apps[0].ClientName)

	test_utils.MakeDeleteRequest(t, router, "/api/v1/oauth/connected-apps/"+apps.Apps[0].ID.String(),
		"Bearer "+user.Token, http.StatusOK)
	assert.Equal(t, http.StatusUnauthorized, postMCP(t, server.URL, tokens["access_token"].(string)))
}

func Test_OAuth_UnregisteredRedirect_IsFatalAndNeverRedirects(t *testing.T) {
	router := createRouter()
	server := httptest.NewServer(router)
	defer server.Close()
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	clientID := registerClient(t, server.URL)

	var body map[string]any
	postJSON(t, server.URL+"/api/v1/oauth/authorize/validate", user.Token,
		authorizeParams(clientID, "https://attacker.example/cb", "x"), http.StatusBadRequest, &body)
	assert.Equal(t, true, body["fatal"])
	assert.NotContains(t, body, "redirectTo")
}

func registerClient(t *testing.T, baseURL string) string {
	t.Helper()
	body := `{"client_name":"Test Assistant","redirect_uris":["` + redirectURI + `"],"token_endpoint_auth_method":"none"}`
	resp, err := http.Post(baseURL+"/oauth/register", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var registered map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&registered))
	return registered["client_id"].(string)
}

func authorizeParams(clientID, redirect, verifier string) map[string]any {
	sum := sha256.Sum256([]byte(verifier))
	return map[string]any{
		"response_type": "code", "client_id": clientID, "redirect_uri": redirect, "state": "xyz",
		"code_challenge": base64.RawURLEncoding.EncodeToString(sum[:]), "code_challenge_method": "S256",
		"scope": "mcp:read",
	}
}

// authorize runs the consent page's calls and returns the code and state.
func authorize(
	t *testing.T, _ *gin.Engine, baseURL string, user *users_dto.SignInResponseDTO, clientID, verifier string,
) (string, string) {
	t.Helper()
	params := authorizeParams(clientID, redirectURI, verifier)
	params["resource"] = baseURL + "/mcp"

	var info oauth.ConsentInfo
	postJSON(t, baseURL+"/api/v1/oauth/authorize/validate", user.Token, params, http.StatusOK, &info)
	assert.Equal(t, "Test Assistant", info.ClientName)

	params["approve"] = true
	var decision struct {
		RedirectTo string `json:"redirectTo"`
	}
	postJSON(t, baseURL+"/api/v1/oauth/authorize/decision", user.Token, params, http.StatusOK, &decision)

	redirect, err := url.Parse(decision.RedirectTo)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(decision.RedirectTo, redirectURI))
	assert.Equal(t, baseURL, redirect.Query().Get("iss"))
	return redirect.Query().Get("code"), redirect.Query().Get("state")
}

func postJSON(t *testing.T, target, token string, body any, wantStatus int, out any) {
	t.Helper()
	encoded, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, target, strings.NewReader(string(encoded)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var raw map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&raw))
	require.Equal(t, wantStatus, resp.StatusCode, raw)
	if out != nil {
		again, _ := json.Marshal(raw)
		require.NoError(t, json.Unmarshal(again, out))
	}
}

func exchange(t *testing.T, baseURL string, form url.Values, wantStatus int) map[string]any {
	t.Helper()
	resp, err := http.PostForm(baseURL+"/oauth/token", form)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, wantStatus, resp.StatusCode, body)
	return body
}

func getJSON(t *testing.T, target string) map[string]any {
	t.Helper()
	resp, err := http.Get(target)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	return body
}
