package mcp_server_tests

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	access_tokens "obzly/internal/features/access_tokens"
	audit_logs "obzly/internal/features/audit_logs"
	users_enums "obzly/internal/features/users/enums"
	users_repositories "obzly/internal/features/users/repositories"
	users_services "obzly/internal/features/users/services"
	users_testing "obzly/internal/features/users/testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_AccessAndOAuthTokens_AfterPasswordChange_AreRejected(t *testing.T) {
	router := createRouter()
	server := httptest.NewServer(router)
	defer server.Close()

	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	pat := createToken(t, router, user)
	require.Equal(t, http.StatusOK, postMCP(t, server.URL, pat))

	clientID := registerClient(t, server.URL)
	verifier := strings.Repeat("p", 60)
	code, _ := authorize(t, router, server.URL, user, clientID, verifier)
	oauthTokens := exchange(t, server.URL, url.Values{
		"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {code},
		"redirect_uri": {redirectURI}, "code_verifier": {verifier},
	}, http.StatusOK)
	require.Equal(t, http.StatusOK, postMCP(t, server.URL, oauthTokens["access_token"].(string)))

	users_services.GetUserService().SetAuditLogWriter(audit_logs.GetAuditLogService())
	require.NoError(t, users_services.GetUserService().ChangeUserPasswordByEmail(user.Email, "a-new-password-123"))

	assert.Equal(t, http.StatusUnauthorized, postMCP(t, server.URL, pat))
	assert.Equal(t, http.StatusUnauthorized, postMCP(t, server.URL, oauthTokens["access_token"].(string)))
	exchange(t, server.URL, url.Values{
		"grant_type": {"refresh_token"}, "client_id": {clientID},
		"refresh_token": {oauthTokens["refresh_token"].(string)},
	}, http.StatusBadRequest)
}

func Test_AccessToken_AfterMFAReset_IsRejected(t *testing.T) {
	router := createRouter()
	server := httptest.NewServer(router)
	defer server.Close()

	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	code := users_testing.EnableRealMFAForTestUser(user.UserID, user.Email)
	member, err := (&users_repositories.UserRepository{}).GetUserByID(user.UserID)
	require.NoError(t, err)
	created, err := access_tokens.GetAccessTokenService().Create(member,
		&access_tokens.CreateAccessTokenRequestDTO{Name: "mfa", ExpiresInDays: 30, MFACode: &code}, false)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, postMCP(t, server.URL, created.Token))

	_, err = users_services.GetUserService().ResetMFAByEmail(user.Email)
	require.NoError(t, err)

	assert.Equal(t, http.StatusUnauthorized, postMCP(t, server.URL, created.Token))
}

// A Member's token was created without MFA; after promotion to Admin it
// must not let MCP calls skip the Admin's MFA requirement.
func Test_MCP_TokenCreatedAsMember_AfterPromotionToAdmin_RequiresMFA(t *testing.T) {
	router := createRouter()
	server := httptest.NewServer(router)
	defer server.Close()

	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	session := connect(t, server.URL, createToken(t, router, user))
	callText(t, session, "list_projects", map[string]any{})

	require.NoError(t, (&users_repositories.UserRepository{}).UpdateUserRole(user.UserID, users_enums.UserRoleAdmin))

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "list_projects", Arguments: map[string]any{},
	})
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, text(result), "MFA")
}
