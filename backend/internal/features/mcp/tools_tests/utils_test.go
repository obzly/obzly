package mcp_tools_tests

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	access_tokens "obzly/internal/features/access_tokens"
	"obzly/internal/features/aiusage"
	"obzly/internal/features/alerting"
	correlation_api "obzly/internal/features/correlation/api"
	dashboards_controllers "obzly/internal/features/dashboards/controllers"
	"obzly/internal/features/ingestion"
	"obzly/internal/features/inventory"
	logs_core "obzly/internal/features/logs/core"
	logs_querying "obzly/internal/features/logs/querying"
	mcp_server "obzly/internal/features/mcp"
	projects_controllers "obzly/internal/features/projects/controllers"
	projects_models "obzly/internal/features/projects/models"
	projects_testing "obzly/internal/features/projects/testing"
	"obzly/internal/features/savedviews"
	system_siteinfo "obzly/internal/features/system/siteinfo"
	traces_querying "obzly/internal/features/traces/querying"
	users_controllers "obzly/internal/features/users/controllers"
	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	users_middleware "obzly/internal/features/users/middleware"
	users_services "obzly/internal/features/users/services"
	users_testing "obzly/internal/features/users/testing"
	"obzly/internal/features/webanalytics"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// env is one project with its owner connected over MCP, and an outsider session.
type env struct {
	router   *gin.Engine
	owner    *users_dto.SignInResponseDTO
	project  *projects_models.Project
	session  *mcp.ClientSession
	outsider *mcp.ClientSession
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

func newEnv(t *testing.T, name string) *env {
	t.Helper()
	router := createRouter()
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project := projects_testing.CreateTestProject(name+" "+uuid.NewString()[:8], owner, router)
	outsider := users_testing.CreateTestUser(users_enums.UserRoleMember)
	return &env{
		router: router, owner: owner, project: project,
		session:  connect(t, server.URL, createToken(t, router, owner)),
		outsider: connect(t, server.URL, createToken(t, router, outsider)),
	}
}

func createRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	system_siteinfo.GetController().RegisterRoutes(router.Group("/api/v1"))
	protected := router.Group("/api/v1")
	protected.Use(users_middleware.AuthMiddleware(users_services.GetUserService()))
	protected.Use(users_middleware.RequireMFAIfEnforced())

	access_tokens.GetAccessTokenController().RegisterRoutes(protected)
	projects_controllers.GetProjectController().RegisterRoutes(protected)
	projects_controllers.GetMembershipController().RegisterRoutes(protected)
	users_controllers.GetUserController().RegisterProtectedRoutes(protected)
	logs_querying.GetLogQueryController().RegisterRoutes(protected)
	traces_querying.GetTraceQueryController().RegisterRoutes(protected)
	correlation_api.GetCorrelationController().RegisterRoutes(protected)
	dashboards_controllers.GetDashboardController().RegisterRoutes(protected)
	alerting.GetController().RegisterRoutes(protected)
	ingestion.GetController().RegisterRoutes(protected)
	inventory.GetController().RegisterRoutes(protected)
	aiusage.GetAIUsageController().RegisterRoutes(protected)
	aiusage.GetBudgetController().RegisterRoutes(protected)
	webanalytics.GetController().RegisterRoutes(protected)
	savedviews.GetController().RegisterRoutes(protected)

	router.Any("/mcp", gin.WrapH(mcp_server.NewHandler(router, false, "test")))
	return router
}

func createToken(t *testing.T, router *gin.Engine, user *users_dto.SignInResponseDTO) string {
	t.Helper()
	// MFA is required for every account, so creating a token asks for a code.
	code := users_testing.EnableRealMFAForTestUser(user.UserID, user.Email)
	var created access_tokens.CreateAccessTokenResponseDTO
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/access-tokens", "Bearer "+user.Token,
		access_tokens.CreateAccessTokenRequestDTO{Name: "test agent", ExpiresInDays: 30, MFACode: &code},
		http.StatusOK, &created)
	return created.Token
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

// call runs a tool that must succeed and decodes its JSON result.
func call(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	body := text(result)
	require.False(t, result.IsError, "%s: %s", name, body)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &decoded), "%s: %s", name, body)
	return decoded
}

// callError runs a tool that must fail and returns its error text.
func callError(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	require.True(t, result.IsError, "%s should fail: %s", name, text(result))
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

func list(t *testing.T, result map[string]any, key string) []any {
	t.Helper()
	items, ok := result[key].([]any)
	require.True(t, ok, "%s is not a list in %v", key, result)
	return items
}

func storeLogs(t *testing.T, projectID uuid.UUID, items ...*logs_core.LogItem) {
	t.Helper()
	for _, item := range items {
		item.ProjectID = projectID
		if item.ID == uuid.Nil {
			item.ID = uuid.New()
		}
	}
	repository := logs_core.GetLogCoreRepository()
	require.NoError(t, repository.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{projectID: items}))
	require.NoError(t, repository.ForceFlush())
}

func logAt(at time.Time, level logs_core.LogLevel, message string, fields map[string]any) *logs_core.LogItem {
	return &logs_core.LogItem{Timestamp: at, Level: level, Message: message, Fields: fields}
}
