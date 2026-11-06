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
	require.NoError(t, control_plane_services.GetSigningKeyService().EnsureActiveKey())
	t.Cleanup(control_plane_services.SetEgressPolicyForTests(func(net.IP) error { return nil }))

	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/site/exchange":
			_ = json.NewEncoder(w).Encode(map[string]string{"token": fakeToken(7)})
		case "/api/v1/projects":
			if r.Header.Get("Authorization") != "Bearer "+fakeToken(7) {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"projects":[{"id":"11111111-1111-1111-1111-111111111111","name":"Payments"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer site.Close()

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
		ApiURL:    site.URL,
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
	defer server.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   server.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearerTransport(token.Token)},
	}, nil)
	require.NoError(t, err)
	defer func() { _ = session.Close() }()

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "list_projects", Arguments: map[string]any{"site": siteID},
	})
	require.NoError(t, err)
	text := result.Content[0].(*mcp.TextContent).Text
	require.False(t, result.IsError, text)
	assert.True(t, strings.Contains(text, "Payments"), text)
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
