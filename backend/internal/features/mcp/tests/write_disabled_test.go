package mcp_server_tests

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	access_tokens "obzly/internal/features/access_tokens"
	"obzly/internal/features/oauth"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_MCP_WriteTurnedOff_TokenWithWriteScope_IsServedReadOnly(t *testing.T) {
	env := newWriteEnv(t)
	turnWriteOff(t)
	session := connect(t, env.baseURL, env.token)

	tools, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	require.NotEmpty(t, tools.Tools)
	for _, tool := range tools.Tools {
		assert.NotContains(t, writeTools, tool.Name)
	}

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "create_silence", Arguments: silenceArgs(env, true),
	})
	if err == nil {
		assert.True(t, result.IsError)
	}
	assert.Empty(t, listSilences(t, env))
}

func Test_CreateAccessToken_WithWrite_WhenWriteTurnedOff_IsRefused(t *testing.T) {
	env := newWriteEnv(t)
	turnWriteOff(t)

	resp := test_utils.MakePostRequest(t, env.router, "/api/v1/access-tokens", "Bearer "+env.owner.Token,
		access_tokens.CreateAccessTokenRequestDTO{Name: "writer while off", ExpiresInDays: 30, Write: true},
		http.StatusBadRequest)
	assert.Contains(t, string(resp.Body), access_tokens.ErrWriteDisabled.Error())

	var list access_tokens.ListAccessTokensResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(t, env.router, "/api/v1/access-tokens", "Bearer "+env.owner.Token,
		http.StatusOK, &list)
	assert.False(t, list.WriteAvailable)
	for _, token := range list.AccessTokens {
		assert.NotEqual(t, "writer while off", token.Name)
	}
}

func Test_OAuth_WriteTurnedOff_GrantsReadOnlyAndDoesNotAdvertiseWrite(t *testing.T) {
	router := createWriteRouter()
	server := httptest.NewServer(router)
	defer server.Close()
	clientID := registerClient(t, server.URL)
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	turnWriteOff(t)

	for _, path := range []string{"/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-authorization-server"} {
		assert.Equal(t, []any{"mcp:read"}, getJSON(t, server.URL+path)["scopes_supported"], path)
	}

	verifier := strings.Repeat("o", 50)
	params := scopedParams(server.URL, clientID, verifier, "mcp:read mcp:write")
	var info oauth.ConsentInfo
	postJSON(t, server.URL+"/api/v1/oauth/authorize/validate", admin.Token, params, http.StatusOK, &info)
	assert.False(t, info.WriteRequested)

	tokens := decideAndExchange(t, server.URL, admin, clientID, verifier, params, true)
	assert.Equal(t, "mcp:read", tokens["scope"])
}

func turnWriteOff(t *testing.T) {
	t.Helper()
	access_tokens.SetWriteEnabled(false)
	t.Cleanup(func() { access_tokens.SetWriteEnabled(true) })
}
