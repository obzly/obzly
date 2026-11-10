package mcp_server_tests

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	access_tokens "obzly/internal/features/access_tokens"
	"obzly/internal/features/cloudcost"
	logs_core "obzly/internal/features/logs/core"
	logs_querying "obzly/internal/features/logs/querying"
	mcp_server "obzly/internal/features/mcp"
	"obzly/internal/features/oauth"
	"obzly/internal/features/posture"
	projects_controllers "obzly/internal/features/projects/controllers"
	projects_testing "obzly/internal/features/projects/testing"
	users_controllers "obzly/internal/features/users/controllers"
	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	users_middleware "obzly/internal/features/users/middleware"
	users_models "obzly/internal/features/users/models"
	users_repositories "obzly/internal/features/users/repositories"
	users_services "obzly/internal/features/users/services"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_MCP_WithAccessToken_ListsToolsAndSearchesLogs(t *testing.T) {
	router := createRouter()
	server := httptest.NewServer(router)
	defer server.Close()

	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project := projects_testing.CreateTestProject("MCP "+uuid.NewString()[:8], owner, router)
	marker := "mcp-marker-" + uuid.NewString()[:8]
	storeLogs(t, project.ID, marker)

	session := connect(t, server.URL, createToken(t, router, owner))

	tools, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
		assert.True(t, tool.Annotations.ReadOnlyHint, tool.Name)
	}
	for _, name := range []string{
		"list_projects", "search_logs", "log_context", "query_logs_sql", "query_metrics", "get_trace",
	} {
		assert.True(t, names[name], name)
	}

	projects := callText(t, session, "list_projects", map[string]any{})
	assert.Contains(t, projects, project.ID.String())

	byName := callText(t, session, "search_logs", map[string]any{
		"project": project.Name, "query": marker + " level:ERROR", "since": "2h",
	})
	var result struct {
		TotalMatches int              `json:"totalMatches"`
		Logs         []map[string]any `json:"logs"`
	}
	require.NoError(t, json.Unmarshal([]byte(byName), &result), byName)
	assert.Equal(t, 1, result.TotalMatches, byName)
	require.Len(t, result.Logs, 1)
	assert.Equal(t, "error", result.Logs[0]["level"])

	// From that hit, log_context finds what came after it - outside the search.
	around := callText(t, session, "log_context", map[string]any{
		"project": project.ID.String(), "id": result.Logs[0]["id"], "timestamp": result.Logs[0]["timestamp"],
		"same": []string{"*"},
	})
	var context struct {
		Log    map[string]any   `json:"log"`
		Before []map[string]any `json:"before"`
		After  []map[string]any `json:"after"`
	}
	require.NoError(t, json.Unmarshal([]byte(around), &context), around)
	assert.Equal(t, result.Logs[0]["id"], context.Log["id"])
	assert.Empty(t, context.Before)
	require.Len(t, context.After, 1, around)
	assert.Contains(t, context.After[0]["message"], "payment retried")

	counted := callText(t, session, "query_logs_sql", map[string]any{
		"project": project.ID.String(),
		"since":   "2h",
		"sql":     "SELECT level, count(*) AS n FROM logs WHERE message LIKE '%" + marker + "%' GROUP BY level ORDER BY level",
	})
	assert.Contains(t, counted, `"columns":["level","n"]`)
	assert.Contains(t, counted, `"rowCount":2`)
}

func Test_MCP_ProjectOfAnotherUser_ReturnsToolError(t *testing.T) {
	router := createRouter()
	server := httptest.NewServer(router)
	defer server.Close()

	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project := projects_testing.CreateTestProject("Private "+uuid.NewString()[:8], owner, router)
	outsider := users_testing.CreateTestUser(users_enums.UserRoleMember)
	session := connect(t, server.URL, createToken(t, router, outsider))

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "search_logs", Arguments: map[string]any{"project": project.ID.String()},
	})
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, text(result), "insufficient permissions")
}

func Test_MCP_InvalidOrRevokedToken_IsRejected(t *testing.T) {
	router := createRouter()
	server := httptest.NewServer(router)
	defer server.Close()

	status := postMCP(t, server.URL, "obz_pat_not-a-real-token")
	assert.Equal(t, http.StatusUnauthorized, status)

	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	token := createToken(t, router, user)
	assert.Equal(t, http.StatusOK, postMCP(t, server.URL, token))

	var list access_tokens.ListAccessTokensResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t,
		router,
		"/api/v1/access-tokens",
		"Bearer "+user.Token,
		http.StatusOK,
		&list,
	)
	require.Len(t, list.AccessTokens, 1)
	test_utils.MakeDeleteRequest(t, router, "/api/v1/access-tokens/"+list.AccessTokens[0].ID.String(),
		"Bearer "+user.Token, http.StatusOK)

	assert.Equal(t, http.StatusUnauthorized, postMCP(t, server.URL, token))
}

func Test_AccessToken_IsNotAcceptedByTheRESTAPI(t *testing.T) {
	router := createRouter()
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	token := createToken(t, router, user)

	test_utils.MakeGetRequest(t, router, "/api/v1/projects", "Bearer "+token, http.StatusUnauthorized)
}

func Test_CreateAccessToken_WhenMFAEnabled_RequiresCode(t *testing.T) {
	router := createRouter()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	resp := test_utils.MakePostRequest(t, router, "/api/v1/access-tokens", "Bearer "+admin.Token,
		access_tokens.CreateAccessTokenRequestDTO{Name: "agent", ExpiresInDays: 30}, http.StatusBadRequest)
	assert.Contains(t, string(resp.Body), "mfaRequired")
}

func Test_MCP_CloudTools_ReadCostAndPostureOfTheCallersProjects(t *testing.T) {
	router := createRouter()
	server := httptest.NewServer(router)
	defer server.Close()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project := projects_testing.CreateTestProject("MCP cloud "+uuid.NewString()[:8], owner, router)
	session := connect(t, server.URL, createToken(t, router, owner))

	recommendations := callText(t, session, "cost_recommendations", map[string]any{"project": project.Name})
	assert.Contains(t, recommendations, `"recommendations":[]`, recommendations)
	scores := callText(t, session, "posture_scores", map[string]any{"project": project.ID.String()})
	assert.Contains(t, scores, `"standards":[]`, scores)
	summary := callText(t, session, "cost_summary", map[string]any{})
	assert.Contains(t, summary, `"currencies"`, summary)
}

func createRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	v1 := router.Group("/api/v1")
	protected := v1.Group("")
	protected.Use(users_middleware.AuthMiddleware(users_services.GetUserService()))
	protected.Use(users_middleware.RequireMFAIfEnforced())

	access_tokens.GetAccessTokenController().RegisterRoutes(protected)
	projects_controllers.GetProjectController().RegisterRoutes(protected)
	projects_controllers.GetMembershipController().RegisterRoutes(protected)
	logs_querying.GetLogQueryController().RegisterRoutes(protected)
	cloudcost.GetController().RegisterRoutes(protected)
	posture.GetController().RegisterRoutes(protected)
	users_controllers.GetUserController().RegisterProtectedRoutes(protected)

	oauth.GetOAuthController().RegisterRoutes(protected)
	oauth.GetOAuthController().RegisterPublicRoutes(router)
	router.Any("/mcp", gin.WrapH(mcp_server.NewHandler(router, false, "test")))
	return router
}

func createToken(t *testing.T, router *gin.Engine, user *users_dto.SignInResponseDTO) string {
	t.Helper()
	var created access_tokens.CreateAccessTokenResponseDTO
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/access-tokens", "Bearer "+user.Token,
		access_tokens.CreateAccessTokenRequestDTO{Name: "test agent", ExpiresInDays: 30}, http.StatusOK, &created)
	require.True(t, strings.HasPrefix(created.Token, access_tokens.TokenPrefix))
	return created.Token
}

func storeLogs(t *testing.T, projectID uuid.UUID, marker string) {
	t.Helper()
	now := time.Now().UTC()
	items := []*logs_core.LogItem{
		{ID: uuid.New(), ProjectID: projectID, Timestamp: now.Add(-2 * time.Minute), Level: logs_core.LogLevelError,
			Message: marker + " payment failed", ClientIP: "203.0.113.5"},
		{ID: uuid.New(), ProjectID: projectID, Timestamp: now.Add(-time.Minute), Level: logs_core.LogLevelInfo,
			Message: marker + " payment retried"},
	}
	repository := logs_core.GetLogCoreRepository()
	require.NoError(t, repository.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{projectID: items}))
	require.NoError(t, repository.ForceFlush())
}

type bearer struct {
	token string
	base  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r)
}

func connect(t *testing.T, baseURL, token string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   baseURL + "/mcp",
		HTTPClient: &http.Client{Transport: bearer{token, http.DefaultTransport}},
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func callText(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	require.False(t, result.IsError, text(result))
	return text(result)
}

func text(result *mcp.CallToolResult) string {
	var parts []string
	for _, content := range result.Content {
		if tc, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// postMCP sends a raw initialize request and returns the HTTP status.
func postMCP(t *testing.T, baseURL, token string) int {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25",` +
		`"capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
	req, err := http.NewRequest(http.MethodPost, baseURL+"/mcp", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

func Test_MCP_ListProjects_ForAdmin_ReportsSiteWideRole(t *testing.T) {
	router := createRouter()
	server := httptest.NewServer(router)
	defer server.Close()

	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project := projects_testing.CreateTestProject("Admin view "+uuid.NewString()[:8], owner, router)

	admin := &users_models.User{
		ID: uuid.New(), Email: "mcp-admin-" + uuid.NewString()[:8] + "@example.com", Name: "Admin",
		PasswordCreationTime: time.Now().UTC(), Role: users_enums.UserRoleAdmin,
		Status: users_enums.UserStatusActive, CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, (&users_repositories.UserRepository{}).CreateUser(admin))
	created, err := access_tokens.GetAccessTokenService().Create(admin,
		&access_tokens.CreateAccessTokenRequestDTO{Name: "admin", ExpiresInDays: 30}, true)
	require.NoError(t, err)

	text := callText(t, connect(t, server.URL, created.Token), "list_projects", map[string]any{"name": project.Name})
	var result struct {
		SiteRole string           `json:"siteRole"`
		Projects []map[string]any `json:"projects"`
	}
	require.NoError(t, json.Unmarshal([]byte(text), &result), text)
	assert.Equal(t, "ADMIN", result.SiteRole)
	for _, p := range result.Projects {
		if p["id"] == project.ID.String() {
			assert.Equal(t, "ADMIN", p["role"])
			assert.Equal(t, "site-wide admin", p["access"])
			return
		}
	}
	t.Fatalf("project %s not listed: %s", project.ID, text)
}

func Test_MCP_SearchLogs_LargeResult_IsTrimmedToValidJSON(t *testing.T) {
	router := createRouter()
	server := httptest.NewServer(router)
	defer server.Close()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project := projects_testing.CreateTestProject("Big "+uuid.NewString()[:8], owner, router)

	now := time.Now().UTC()
	items := make([]*logs_core.LogItem, 120)
	for i := range items {
		items[i] = &logs_core.LogItem{
			ID: uuid.New(), ProjectID: project.ID, Timestamp: now.Add(-time.Duration(i) * time.Second),
			Level: logs_core.LogLevelInfo, Message: strings.Repeat("GET /api/v1/orders ", 90),
			Fields: map[string]any{"host": "dev.florista.ph", "ua": strings.Repeat("Mozilla ", 80)},
		}
	}
	repository := logs_core.GetLogCoreRepository()
	require.NoError(t, repository.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{project.ID: items}))
	require.NoError(t, repository.ForceFlush())

	text := callText(t, connect(t, server.URL, createToken(t, router, owner)), "search_logs", map[string]any{
		"project": project.ID.String(), "limit": 100, "since": "1h",
	})
	var result struct {
		TotalMatches int              `json:"totalMatches"`
		Returned     int              `json:"returned"`
		Logs         []map[string]any `json:"logs"`
		Note         string           `json:"note"`
	}
	require.NoError(t, json.Unmarshal([]byte(text), &result), "result must be valid JSON")
	assert.Equal(t, 120, result.TotalMatches)
	assert.Len(t, result.Logs, result.Returned)
	assert.Less(t, result.Returned, 100)
	assert.Contains(t, result.Note, "of 100 logs")
}
