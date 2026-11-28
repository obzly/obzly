package mcp_server_tests

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	access_tokens "obzly/internal/features/access_tokens"
	"obzly/internal/features/alerting"
	audit_logs "obzly/internal/features/audit_logs"
	dashboards_controllers "obzly/internal/features/dashboards/controllers"
	dashboards_models "obzly/internal/features/dashboards/models"
	mcp_server "obzly/internal/features/mcp"
	"obzly/internal/features/oauth"
	projects_controllers "obzly/internal/features/projects/controllers"
	projects_models "obzly/internal/features/projects/models"
	projects_testing "obzly/internal/features/projects/testing"
	users_controllers "obzly/internal/features/users/controllers"
	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	users_middleware "obzly/internal/features/users/middleware"
	users_repositories "obzly/internal/features/users/repositories"
	users_services "obzly/internal/features/users/services"
	users_testing "obzly/internal/features/users/testing"
	"obzly/internal/storage"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeEnv is a project whose owner has a write token, served over HTTP.
type writeEnv struct {
	router  *gin.Engine
	baseURL string
	owner   *users_dto.SignInResponseDTO
	project *projects_models.Project
	token   string
}

// elicitor answers elicitation requests and remembers their messages.
type elicitor struct {
	mu       sync.Mutex
	action   string
	messages []string
}

var writeTools = []string{"create_silence", "expire_silence", "create_dashboard", "add_dashboard_panel"}

func Test_MCP_ReadToken_NeitherListsNorCallsWriteTools(t *testing.T) {
	env := newWriteEnv(t)
	session := connect(t, env.baseURL, createToken(t, env.router, env.owner))

	tools, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	for _, tool := range tools.Tools {
		assert.NotContains(t, writeTools, tool.Name)
		assert.True(t, tool.Annotations.ReadOnlyHint, tool.Name)
	}

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "create_silence", Arguments: silenceArgs(env, true),
	})
	if err == nil {
		assert.True(t, result.IsError)
	}
	assert.Empty(t, listSilences(t, env))
}

// MFA is required for every account: a session without it can't reach the
// route, and the service still refuses write to a session that didn't pass MFA.
func Test_CreateAccessToken_WithWrite_NeedsASessionThatPassedMFA(t *testing.T) {
	env := newWriteEnv(t)

	withoutMFA := users_testing.CreateTestUserWithoutMFA(users_enums.UserRoleMember)
	resp := test_utils.MakePostRequest(t, env.router, "/api/v1/access-tokens", "Bearer "+withoutMFA.Token,
		access_tokens.CreateAccessTokenRequestDTO{Name: "writer", ExpiresInDays: 30, Write: true},
		http.StatusForbidden)
	assert.Contains(t, string(resp.Body), "mfaSetupRequired")
	user, err := (&users_repositories.UserRepository{}).GetUserByID(withoutMFA.UserID)
	require.NoError(t, err)
	_, err = access_tokens.GetAccessTokenService().Create(user,
		&access_tokens.CreateAccessTokenRequestDTO{Name: "writer", ExpiresInDays: 30, Write: true}, false)
	assert.ErrorIs(t, err, access_tokens.ErrWriteRequiresMFA)

	var list access_tokens.ListAccessTokensResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(t, env.router, "/api/v1/access-tokens", "Bearer "+env.owner.Token,
		http.StatusOK, &list)
	scopes := map[string][]string{}
	for _, token := range list.AccessTokens {
		scopes[token.Name] = token.Scopes
	}
	assert.Equal(t, []string{access_tokens.ScopeMCPRead, access_tokens.ScopeMCPWrite}, scopes["writer (mfa)"])
	assert.NotContains(t, scopes, "writer")
}

func Test_MCP_WriteToken_WithoutMFAMark_IsRefused(t *testing.T) {
	env := newWriteEnv(t)
	require.NoError(t, storage.GetDb().Model(&access_tokens.AccessToken{}).
		Where("token_hash = ?", access_tokens.HashToken(env.token)).Update("mfa_satisfied", false).Error)

	result, err := connect(t, env.baseURL, env.token).CallTool(context.Background(), &mcp.CallToolParams{
		Name: "create_silence", Arguments: silenceArgs(env, true),
	})
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, text(result), "two-factor")
	assert.Empty(t, listSilences(t, env))
}

func Test_MCP_CreateSilence_PreviewThenConfirm_LabelsAndAuditsIt(t *testing.T) {
	env := newWriteEnv(t)
	session := connect(t, env.baseURL, env.token)

	tools, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	listed := map[string]bool{}
	for _, tool := range tools.Tools {
		listed[tool.Name] = true
	}
	for _, name := range writeTools {
		assert.True(t, listed[name], name)
	}

	preview := callText(t, session, "create_silence", silenceArgs(env, false))
	assert.Contains(t, preview, `"preview":true`)
	assert.Empty(t, listSilences(t, env), "a preview changes nothing")

	done := callText(t, session, "create_silence", silenceArgs(env, true))
	assert.Contains(t, done, `"changed":true`)
	silences := listSilences(t, env)
	require.Len(t, silences, 1)
	assert.Equal(t, "writer (mfa)", silences[0].MCPClient)
	assert.Equal(t, "node-3 maintenance", silences[0].Comment)
	assert.WithinDuration(t, time.Now().Add(90*time.Minute), silences[0].EndsAt, time.Minute)

	entry := latestAudit(t, env, "MCP write create_silence")
	assert.Contains(t, entry.Message, `access token "writer (mfa)"`)
	assert.Contains(t, entry.Message, `"comment":"node-3 maintenance"`)
	assert.Contains(t, entry.Message, ", done:")
	assert.NotContains(t, entry.Message, env.token)
	assert.NotContains(t, entry.Message, "confirm")
}

func Test_MCP_CreateSilence_TooLongOrMatchingEverything_IsRefused(t *testing.T) {
	env := newWriteEnv(t)
	session := connect(t, env.baseURL, env.token)

	for name, change := range map[string]func(map[string]any){
		"five hours": func(a map[string]any) { a["duration"] = "5h" },
		"no comment": func(a map[string]any) { delete(a, "comment") },
		"regex only": func(a map[string]any) {
			a["matchers"] = []map[string]any{{"name": "alertname", "value": ".*", "regex": true}}
		},
	} {
		args := silenceArgs(env, true)
		change(args)
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "create_silence", Arguments: args})
		require.NoError(t, err)
		assert.True(t, result.IsError, name)
	}
	assert.Empty(t, listSilences(t, env))
}

func Test_MCP_ClientWithElicitation_IsAskedForEachWrite(t *testing.T) {
	env := newWriteEnv(t)
	asker := &elicitor{action: "accept"}
	session := connectWithElicitation(t, env.baseURL, env.token, asker, "")

	// confirm: true from the model doesn't skip asking the user.
	done := callText(t, session, "create_silence", silenceArgs(env, true))
	assert.Contains(t, done, `"changed":true`)
	require.Len(t, asker.messages, 1)
	assert.Contains(t, asker.messages[0], `alertname="HighCPU"`)
	assert.Contains(t, asker.messages[0], "writer (mfa)")
	require.Len(t, listSilences(t, env), 1)

	asker.action = "decline"
	declined := callText(t, session, "create_silence", silenceArgs(env, true))
	assert.Contains(t, declined, `"changed":false`)
	assert.Len(t, asker.messages, 2)
	assert.Len(t, listSilences(t, env), 1, "a declined write changes nothing")
}

func Test_MCP_OlderProtocolClient_FallsBackToThePreview(t *testing.T) {
	env := newWriteEnv(t)
	asker := &elicitor{action: "accept"}
	// A stateless server can't send elicitation/create to a pre-2026-07-28 client.
	session := connectWithElicitation(t, env.baseURL, env.token, asker, "2025-11-25")

	preview := callText(t, session, "create_silence", silenceArgs(env, false))
	assert.Contains(t, preview, `"preview":true`)
	assert.Empty(t, asker.messages)
	assert.Empty(t, listSilences(t, env))

	callText(t, session, "create_silence", silenceArgs(env, true))
	assert.Len(t, listSilences(t, env), 1)
}

func Test_MCP_ExpireSilence_SaysWhatItEnded(t *testing.T) {
	env := newWriteEnv(t)
	var created alerting.Silence
	test_utils.MakePostRequestAndUnmarshal(t, env.router, "/api/v1/projects/"+env.project.ID.String()+"/silences",
		"Bearer "+env.owner.Token, alerting.SilenceRequest{
			Matchers: alerting.Matchers{{Name: "alertname", Value: "DiskFull"}}, EndsAt: time.Now().Add(time.Hour),
			Comment: "disk swap",
		}, http.StatusOK, &created)
	session := connect(t, env.baseURL, env.token)
	args := map[string]any{"project": env.project.Name, "silenceId": created.ID.String()}

	preview := callText(t, session, "expire_silence", args)
	assert.Contains(t, preview, created.ID.String())
	assert.Contains(t, preview, "disk swap")
	assert.Len(t, listSilences(t, env), 1)

	args["confirm"] = true
	done := callText(t, session, "expire_silence", args)
	assert.Contains(t, done, created.ID.String())
	assert.Contains(t, done, `"expired"`)
	assert.Contains(t, done, "disk swap")
	assert.Empty(t, listSilences(t, env))
	latestAudit(t, env, "MCP write expire_silence")

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "expire_silence", Arguments: args})
	require.NoError(t, err)
	assert.True(t, result.IsError, "an ended silence isn't listed any more")
}

func Test_MCP_ExpireSilence_ByProjectViewer_IsRefused(t *testing.T) {
	env := newWriteEnv(t)
	var created alerting.Silence
	test_utils.MakePostRequestAndUnmarshal(t, env.router, "/api/v1/projects/"+env.project.ID.String()+"/silences",
		"Bearer "+env.owner.Token, alerting.SilenceRequest{
			Matchers: alerting.Matchers{{Name: "alertname", Value: "DiskFull"}}, EndsAt: time.Now().Add(time.Hour),
		}, http.StatusOK, &created)
	viewer := users_testing.CreateTestUser(users_enums.UserRoleMember)
	projects_testing.AddMemberToProject(env.project, viewer, users_enums.ProjectRoleViewer, env.owner.Token, env.router)

	result, err := connect(t, env.baseURL, writeToken(t, viewer)).CallTool(context.Background(),
		&mcp.CallToolParams{Name: "expire_silence", Arguments: map[string]any{
			"project": env.project.ID.String(), "silenceId": created.ID.String(), "confirm": true,
		}})
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, text(result), "insufficient permissions")
	assert.Len(t, listSilences(t, env), 1)
}

func Test_MCP_CreateDashboardThenAddPanel_KeepsExistingPanels(t *testing.T) {
	env := newWriteEnv(t)
	session := connect(t, env.baseURL, env.token)

	created := callText(t, session, "create_dashboard", map[string]any{
		"project": env.project.Name, "title": "Checkout", "confirm": true,
		"panels": []map[string]any{
			{"title": "Errors", "query": "level:error", "groupField": "service_name"},
			{"title": "CPU", "type": "gauge", "promql": "avg(system_cpu_utilization)", "max": 1},
		},
	})
	var result struct {
		Result struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal([]byte(created), &result), created)
	require.NotEmpty(t, result.Result.ID, created)

	bad, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "add_dashboard_panel",
		Arguments: map[string]any{"project": env.project.Name, "dashboardId": result.Result.ID, "confirm": true,
			"panel": map[string]any{"title": "x", "type": "line", "warn": 3}}})
	require.NoError(t, err)
	assert.True(t, bad.IsError, "thresholds aren't a line panel setting")

	added := callText(t, session, "add_dashboard_panel", map[string]any{
		"project": env.project.Name, "dashboardId": result.Result.ID, "confirm": true,
		"panel": map[string]any{"title": "Latency", "type": "stat", "service": "api", "metric": "p95_duration_ms"},
	})
	assert.Contains(t, added, `"changed":true`)

	var dashboard dashboards_models.Dashboard
	test_utils.MakeGetRequestAndUnmarshal(t, env.router,
		"/api/v1/projects/dashboards/"+env.project.ID.String()+"/"+result.Result.ID, "Bearer "+env.owner.Token,
		http.StatusOK, &dashboard)
	require.Len(t, dashboard.Tabs, 1)
	require.Len(t, dashboard.Tabs[0].Panels, 3)
	assert.Equal(t, "Errors", dashboard.Tabs[0].Panels[0].Title)
	assert.Equal(t, "Latency", dashboard.Tabs[0].Panels[2].Title)
	assert.Equal(t, 8, dashboard.Tabs[0].Panels[2].GridPos.Y)
	latestAudit(t, env, "MCP write add_dashboard_panel")
}

func newWriteEnv(t *testing.T) *writeEnv {
	t.Helper()
	router := createWriteRouter()
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project := projects_testing.CreateTestProject("MCP write "+uuid.NewString()[:8], owner, router)
	return &writeEnv{router: router, baseURL: server.URL, owner: owner, project: project, token: writeToken(t, owner)}
}

func createWriteRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	protected := router.Group("/api/v1")
	protected.Use(users_middleware.AuthMiddleware(users_services.GetUserService()))
	protected.Use(users_middleware.RequireMFAIfEnforced())

	access_tokens.GetAccessTokenController().RegisterRoutes(protected)
	projects_controllers.GetProjectController().RegisterRoutes(protected)
	projects_controllers.GetMembershipController().RegisterRoutes(protected)
	users_controllers.GetUserController().RegisterProtectedRoutes(protected)
	alerting.GetController().RegisterRoutes(protected)
	dashboards_controllers.GetDashboardController().RegisterRoutes(protected)
	oauth.GetOAuthController().RegisterRoutes(protected)
	oauth.GetOAuthController().RegisterPublicRoutes(router)
	router.Any("/mcp", gin.WrapH(mcp_server.NewHandler(router, false, "test")))
	return router
}

// writeToken is a write token created with an authenticator code, as every
// account with local MFA creates one.
func writeToken(t *testing.T, signIn *users_dto.SignInResponseDTO) string {
	t.Helper()
	code := users_testing.EnableRealMFAForTestUser(signIn.UserID, signIn.Email)
	user, err := (&users_repositories.UserRepository{}).GetUserByID(signIn.UserID)
	require.NoError(t, err)
	created, err := access_tokens.GetAccessTokenService().Create(user,
		&access_tokens.CreateAccessTokenRequestDTO{
			Name: "writer (mfa)", ExpiresInDays: 30, Write: true, MFACode: &code,
		}, false)
	require.NoError(t, err)
	return created.Token
}

func silenceArgs(env *writeEnv, confirm bool) map[string]any {
	return map[string]any{
		"project": env.project.Name, "duration": "90m", "comment": "node-3 maintenance", "confirm": confirm,
		"matchers": []map[string]any{{"name": "alertname", "value": "HighCPU"}},
	}
}

func listSilences(t *testing.T, env *writeEnv) []alerting.Silence {
	t.Helper()
	var silences []alerting.Silence
	test_utils.MakeGetRequestAndUnmarshal(t, env.router, "/api/v1/projects/"+env.project.ID.String()+"/silences",
		"Bearer "+env.owner.Token, http.StatusOK, &silences)
	return silences
}

func latestAudit(t *testing.T, env *writeEnv, prefix string) audit_logs.AuditLog {
	t.Helper()
	var entry audit_logs.AuditLog
	require.NoError(t, storage.GetDb().Where("user_id = ? AND message LIKE ?", env.owner.UserID, prefix+"%").
		Order("created_at DESC").First(&entry).Error)
	return entry
}

func connectWithElicitation(t *testing.T, baseURL, token string, asker *elicitor, version string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, &mcp.ClientOptions{
		ElicitationHandler: asker.handle,
	})
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   baseURL + "/mcp",
		HTTPClient: &http.Client{Transport: bearer{token, http.DefaultTransport}},
	}, &mcp.ClientSessionOptions{ProtocolVersion: version})
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func (e *elicitor) handle(_ context.Context, request *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.messages = append(e.messages, request.Params.Message)
	if e.action != "accept" {
		return &mcp.ElicitResult{Action: e.action}, nil
	}
	return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}, nil
}
