package control_plane_controllers

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	access_tokens "obzly/internal/features/access_tokens"
	control_plane_models "obzly/internal/features/control_plane/models"
	control_plane_repositories "obzly/internal/features/control_plane/repositories"
	control_plane_services "obzly/internal/features/control_plane/services"
	mcp_server "obzly/internal/features/mcp"
	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"
	users_repositories "obzly/internal/features/users/repositories"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression: MCP GET calls through the in-process Site proxy had a nil body and a
// writer without CloseNotify, and panicked.
func Test_MCP_OnControlPlane_ListsProjectsThroughTheSiteProxy(t *testing.T) {
	session, siteID := connectThroughControlPlane(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/api/v1/projects" {
			return false
		}
		_, _ = w.Write([]byte(`{"projects":[{"id":"11111111-1111-1111-1111-111111111111","name":"Payments"}]}`))
		return true
	})

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "list_projects", Arguments: map[string]any{"site": siteID},
	})
	require.NoError(t, err)
	text := result.Content[0].(*mcp.TextContent).Text
	require.False(t, result.IsError, text)
	assert.True(t, strings.Contains(text, "Payments"), text)
}

func Test_MCP_OnControlPlane_ListsEveryToolAndQueriesThroughTheSiteProxy(t *testing.T) {
	projectID := "11111111-1111-1111-1111-111111111111"
	searchedThrough := time.Now().UTC().Add(-30 * time.Minute).Format(time.RFC3339Nano)
	session, siteID := connectThroughControlPlane(t, func(w http.ResponseWriter, r *http.Request) bool {
		switch r.URL.Path {
		case "/api/v1/logs/query/execute/" + projectID:
			_, _ = w.Write([]byte(`{"logs":[{"id":"l1","timestamp":"2026-09-24T06:00:00Z","level":"error",` +
				`"message":"boom"}],"total":-1,"countSkipped":true,"partial":true,"searchedThrough":"` +
				searchedThrough + `","nextCursor":"c2"}`))
		case "/api/v1/metrics/query/promql/" + projectID:
			_, _ = w.Write([]byte(`{"resultType":"matrix","stepSeconds":60,"series":[{"labels":{"pod":"a"},` +
				`"points":[{"timestamp":"2026-09-24T06:00:00Z","value":2}]}]}`))
		case "/api/v1/traces/query/get/" + projectID + "/abc123":
			_, _ = w.Write([]byte(`{"traceId":"abc123","spans":[{"spanId":"s1","name":"GET /"}]}`))
		default:
			return false
		}
		return true
	})

	tools, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(tools.Tools), 27)
	for _, tool := range tools.Tools {
		schema, err := json.Marshal(tool.InputSchema)
		require.NoError(t, err)
		assert.Contains(t, string(schema), `"site"`, tool.Name)
		assert.True(t, tool.Annotations.ReadOnlyHint, tool.Name)
	}

	logs := callThroughControlPlane(t, session, "search_logs", map[string]any{"site": siteID, "project": projectID})
	assert.Contains(t, logs, `"partial":true`)
	assert.Contains(t, logs, `"searchedThrough":"`+searchedThrough+`"`)
	assert.Contains(t, logs, `"nextCursor":"c2"`)
	assert.Contains(t, logs, `"totalMatches":null`)
	metrics := callThroughControlPlane(t, session, "query_metrics",
		map[string]any{"site": siteID, "project": projectID, "promql": "up"})
	assert.Contains(t, metrics, `"points":[[1790229600,2]]`)
	trace := callThroughControlPlane(t, session, "get_trace",
		map[string]any{"site": siteID, "project": projectID, "traceId": "abc123"})
	assert.Contains(t, trace, `"spanId":"s1"`)
	assert.Contains(t, trace, `"untrustedData"`)
}

// connectThroughControlPlane serves MCP on a control plane whose one Site is
// a fake: site answers a request (returning true) or it is a 404.
func connectThroughControlPlane(
	t *testing.T, site func(http.ResponseWriter, *http.Request) bool,
) (*mcp.ClientSession, string) {
	t.Helper()
	require.NoError(t, control_plane_services.GetSigningKeyService().EnsureActiveKey())
	t.Cleanup(control_plane_services.SetEgressPolicyForTests(func(net.IP) error { return nil }))

	siteServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/auth/site/exchange" {
			_ = json.NewEncoder(w).Encode(map[string]string{"token": fakeToken(7)})
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+fakeToken(7) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !site(w, r) {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(siteServer.Close)

	user := &users_models.User{
		ID: uuid.New(), Email: "mcp-cp-" + uuid.NewString()[:8] + "@example.com", Name: "MCP",
		PasswordCreationTime: time.Now().UTC(), Role: users_enums.UserRoleMember,
		Status: users_enums.UserStatusActive, CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, (&users_repositories.UserRepository{}).CreateUser(user))
	siteID := "mcp-" + uuid.NewString()[:8]
	require.NoError(t, (&control_plane_repositories.SiteRepository{}).Create(&control_plane_models.Site{
		ID:        siteID,
		Name:      "EU",
		ApiURL:    siteServer.URL,
		Enabled:   true,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}))
	require.NoError(t, (&control_plane_repositories.GrantRepository{}).Create(&control_plane_models.SiteGrant{
		UserID: user.ID, SiteID: siteID, Role: "ADMIN", CreatedBy: user.ID,
	}))
	token, err := access_tokens.GetAccessTokenService().Create(user,
		&access_tokens.CreateAccessTokenRequestDTO{Name: "cp", ExpiresInDays: 30}, true)
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(gin.Recovery())
	fakeAuth := func(c *gin.Context) { c.Set("user", user); c.Next() }
	GetProxyController().RegisterRoutes(router, fakeAuth, func(c *gin.Context) { c.Next() })
	router.Any("/mcp", gin.WrapH(mcp_server.NewHandler(router, true, "test")))
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   server.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearerTransport(token.Token)},
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	return session, siteID
}

func callThroughControlPlane(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	text := result.Content[0].(*mcp.TextContent).Text
	require.False(t, result.IsError, text)
	return text
}

type bearerRoundTripper string

func (b bearerRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+string(b))
	return http.DefaultTransport.RoundTrip(r)
}

func bearerTransport(token string) http.RoundTripper {
	return bearerRoundTripper(token)
}
