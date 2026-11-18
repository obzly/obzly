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
	metrics_querying "obzly/internal/features/metrics/querying"
	"obzly/internal/features/synthetics"
	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"
	users_repositories "obzly/internal/features/users/repositories"
	"obzly/internal/storage"
	"obzly/internal/synthetics/spec"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// monitor_status lists monitors centrally, then reads their results with PromQL on the
// destination Site through the control plane's /sites/{id} proxy.
func Test_MCP_MonitorStatus_OnControlPlane_ReadsResultsThroughTheSiteProxy(t *testing.T) {
	require.NoError(t, control_plane_services.GetSigningKeyService().EnsureActiveKey())
	t.Cleanup(control_plane_services.SetEgressPolicyForTests(func(net.IP) error { return nil }))

	projectID := uuid.NewString()
	var checkID string
	var promQueries []metrics_querying.PromQLQueryRequestDTO
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/auth/site/exchange":
			_ = json.NewEncoder(w).Encode(map[string]string{"token": fakeToken(9)})
		case r.URL.Path == "/api/v1/metrics/query/promql/"+projectID:
			var request metrics_querying.PromQLQueryRequestDTO
			_ = json.NewDecoder(r.Body).Decode(&request)
			promQueries = append(promQueries, request)
			_ = json.NewEncoder(w).Encode(fakeSyntheticsResult(request.Query, checkID))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer site.Close()

	user := &users_models.User{
		ID: uuid.New(), Email: "mcp-mon-" + uuid.NewString()[:8] + "@example.com", Name: "MCP",
		PasswordCreationTime: time.Now().UTC(), Role: users_enums.UserRoleAdmin,
		Status: users_enums.UserStatusActive, CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, (&users_repositories.UserRepository{}).CreateUser(user))
	siteID := "mon-" + uuid.NewString()[:8]
	require.NoError(t, (&control_plane_repositories.SiteRepository{}).Create(&control_plane_models.Site{
		ID: siteID, Name: "EU", ApiURL: site.URL, Enabled: true,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}))
	require.NoError(t, (&control_plane_repositories.GrantRepository{}).Create(&control_plane_models.SiteGrant{
		UserID: user.ID, SiteID: siteID, Role: "MEMBER", CreatedBy: user.ID,
	}))
	destination, err := synthetics.GetService().CreateDestination(user, synthetics.DestinationRequest{
		Name: "results " + siteID, SiteID: siteID, ProjectID: projectID, OTLPURL: "https://site.example.com/",
		APIKey: "lb_destination_key_5678", // gitleaks:allow (test fixture)
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		storage.GetDb().Exec("DELETE FROM synthetic_checks WHERE destination_id = ?", destination.ID)
		storage.GetDb().Exec("DELETE FROM synthetic_destinations WHERE id = ?", destination.ID)
	})
	name := "Checkout health " + uuid.NewString()[:8]
	check, err := synthetics.GetService().CreateCheck(user, synthetics.CheckRequest{
		Name: name, Enabled: true, IntervalSeconds: 60, DestinationID: destination.ID,
		Definition: spec.Definition{Type: spec.TypeHTTP, Request: &spec.HTTPRequest{
			URL: "https://shop.example.com/health", Headers: map[string]string{"Authorization": "Bearer hidden-value"},
		}},
	})
	require.NoError(t, err)
	checkID = check.ID.String()
	token, err := access_tokens.GetAccessTokenService().Create(user,
		&access_tokens.CreateAccessTokenRequestDTO{Name: "cp", ExpiresInDays: 30}, true)
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(gin.Recovery())
	fakeAuth := func(c *gin.Context) { c.Set("user", user); c.Next() }
	synthetics.GetController().RegisterRoutes(router.Group("/api/v1", fakeAuth))
	GetProxyController().RegisterRoutes(router, fakeAuth, func(c *gin.Context) { c.Next() })
	router.Any("/mcp", gin.WrapH(mcp_server.NewHandler(router, true, "test")))
	server := httptest.NewServer(router)
	defer server.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   server.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearerTransport(token.Token)},
	}, nil)
	require.NoError(t, err)
	defer func() { _ = session.Close() }()

	monitors := callTool(t, session, "list_monitors", map[string]any{"name": name})
	assert.Contains(t, monitors, "https://shop.example.com/health")
	assert.NotContains(t, monitors, "hidden-value", "request headers are never shown")

	text := callTool(t, session, "monitor_status", map[string]any{"monitor": name, "since": "6h", "site": siteID})
	var result struct {
		Monitors []struct {
			ID            string `json:"id"`
			Status        string `json:"status"`
			UptimePercent any    `json:"uptimePercent"`
			P95Ms         any    `json:"p95Ms"`
			Locations     []struct {
				Location string `json:"location"`
				Up       bool   `json:"up"`
			} `json:"locations"`
		} `json:"monitors"`
		Errors []string `json:"errors"`
	}
	require.NoError(t, json.Unmarshal([]byte(text), &result), text)
	require.Empty(t, result.Errors)
	require.Len(t, result.Monitors, 1, text)
	got := result.Monitors[0]
	assert.Equal(t, checkID, got.ID)
	assert.Equal(t, "down", got.Status, "one of two locations failing is half of them")
	assert.InDelta(t, 75.0, got.UptimePercent, 0.001)
	assert.InDelta(t, 250.0, got.P95Ms, 0.001)
	require.Len(t, got.Locations, 2)
	assert.Equal(t, "eu", got.Locations[0].Location)
	assert.False(t, got.Locations[0].Up)

	require.Len(t, promQueries, 4, "latest up and warn, uptime and p95")
	for _, query := range promQueries {
		assert.True(t, query.Instant)
		assert.Contains(t, query.Query, `check_id=~"`+checkID+`"`)
		assert.LessOrEqual(t, query.MaxSeries, 2000)
	}

	tooLong, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "monitor_status", Arguments: map[string]any{"since": "8d"},
	})
	require.NoError(t, err)
	assert.True(t, tooLong.IsError)
}

func fakeSyntheticsResult(query, checkID string) metrics_querying.PromQLQueryResponseDTO {
	point := func(value float64) []metrics_querying.PromQLPointDTO {
		return []metrics_querying.PromQLPointDTO{{Timestamp: time.Now().UTC(), Value: value}}
	}
	series := func(location string, value float64) metrics_querying.PromQLSeriesDTO {
		labels := map[string]string{"check_id": checkID}
		if location != "" {
			labels["location"] = location
		}
		return metrics_querying.PromQLSeriesDTO{Labels: labels, Points: point(value)}
	}
	response := metrics_querying.PromQLQueryResponseDTO{ResultType: "vector"}
	switch {
	case strings.Contains(query, "last_over_time(synthetics_up"):
		response.Series = []metrics_querying.PromQLSeriesDTO{series("eu", 0), series("us", 1)}
	case strings.Contains(query, "quantile_over_time"):
		response.Series = []metrics_querying.PromQLSeriesDTO{series("", 0.25)}
	case strings.Contains(query, "sum_over_time(synthetics_up"):
		response.Series = []metrics_querying.PromQLSeriesDTO{series("", 0.75)}
	default:
		response.Series = []metrics_querying.PromQLSeriesDTO{}
	}
	return response
}

func callTool(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	text := result.Content[0].(*mcp.TextContent).Text
	require.False(t, result.IsError, text)
	return text
}
