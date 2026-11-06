package control_plane_controllers

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	control_plane_models "obzly/internal/features/control_plane/models"
	control_plane_repositories "obzly/internal/features/control_plane/repositories"
	control_plane_services "obzly/internal/features/control_plane/services"
	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"
	users_repositories "obzly/internal/features/users/repositories"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// statusSite is a fake Site serving /api/v1/site/info and /api/v1/system/health.
func statusSite(t *testing.T, siteInfo, health http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/site/info", siteInfo)
	mux.HandleFunc("/api/v1/system/health", health)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func infoAs(siteID string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"siteId":"` + siteID + `","version":"v9.9.9"}`))
	}
}

func healthy(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"Application is healthy"}`))
}

func newStatusSiteID() string { return "status-" + uuid.NewString()[:8] }

func registerStatusSite(t *testing.T, id, apiURL string, enabled bool) string {
	t.Helper()
	site := &control_plane_models.Site{
		ID: id, Name: "Status", ApiURL: apiURL, Enabled: enabled,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, (&control_plane_repositories.SiteRepository{}).Create(site))
	t.Cleanup(func() { _ = (&control_plane_repositories.SiteRepository{}).Delete(site.ID) })
	return site.ID
}

func getSiteStatuses(
	t *testing.T,
	role users_enums.UserRole,
) (int, map[string]control_plane_services.SiteStatusResult) {
	t.Helper()
	t.Cleanup(control_plane_services.SetEgressPolicyForTests(func(net.IP) error { return nil }))
	user := &users_models.User{
		ID: uuid.New(), Email: "status-" + uuid.NewString() + "@example.com", Name: "Status Test",
		PasswordCreationTime: time.Now().UTC(), Role: role,
		Status: users_enums.UserStatusActive, CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, (&users_repositories.UserRepository{}).CreateUser(user))

	gin.SetMode(gin.TestMode)
	router := gin.New()
	protected := router.Group("/api/v1", func(c *gin.Context) { c.Set("user", user); c.Next() })
	GetSiteController().RegisterRoutes(protected)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/control-plane/admin/sites/status", nil))

	var body struct {
		Statuses []control_plane_services.SiteStatusResult `json:"statuses"`
	}
	byID := map[string]control_plane_services.SiteStatusResult{}
	if recorder.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
		for _, s := range body.Statuses {
			byID[s.SiteID] = s
		}
	}
	return recorder.Code, byID
}

func Test_GetSiteStatuses_ReportsEachSitesTrafficLight(t *testing.T) {
	upID, unhealthyID := newStatusSiteID(), newStatusSiteID()
	up := registerStatusSite(t, upID, statusSite(t, infoAs(upID), healthy).URL, true)

	ingress503 := statusSite(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("<html><title>503 Service Temporarily Unavailable</title></html>"))
	}, healthy)
	deploying := registerStatusSite(t, newStatusSiteID(), ingress503.URL, true)

	unhealthy := registerStatusSite(
		t,
		unhealthyID,
		statusSite(t, infoAs(unhealthyID), func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"disk usage is above 95%"}`))
		}).URL,
		true,
	)

	wrongSite := registerStatusSite(t, newStatusSiteID(), statusSite(t, infoAs("someone-else"), healthy).URL, true)
	unreachable := registerStatusSite(t, newStatusSiteID(), "http://127.0.0.1:1", true)
	disabled := registerStatusSite(t, newStatusSiteID(), "http://127.0.0.1:1", false)

	code, statuses := getSiteStatuses(t, users_enums.UserRoleAdmin)
	require.Equal(t, http.StatusOK, code)

	assert.Equal(t, control_plane_services.SiteStatusUp, statuses[up].Status)
	assert.Equal(t, "v9.9.9", statuses[up].Version)

	assert.Equal(t, control_plane_services.SiteStatusDown, statuses[deploying].Status)
	assert.Contains(t, statuses[deploying].Message, "HTTP 503")
	assert.NotContains(t, statuses[deploying].Message, "<html>")

	assert.Equal(t, control_plane_services.SiteStatusDegraded, statuses[unhealthy].Status)
	assert.Contains(t, statuses[unhealthy].Message, "disk usage is above 95%")

	assert.Equal(t, control_plane_services.SiteStatusDegraded, statuses[wrongSite].Status)
	assert.Contains(t, statuses[wrongSite].Message, "someone-else")

	assert.Equal(t, control_plane_services.SiteStatusDown, statuses[unreachable].Status)
	assert.Equal(t, control_plane_services.SiteStatusDisabled, statuses[disabled].Status)
}

func Test_GetSiteStatuses_WhenUserIsNotAdmin_ReturnsForbidden(t *testing.T) {
	code, _ := getSiteStatuses(t, users_enums.UserRoleMember)
	assert.Equal(t, http.StatusForbidden, code)
}

// The picker's status route is for every user: only Sites they can reach,
// and only the light - no reason, version or latency.
func Test_GetReachableSiteStatuses_OnlyGrantedSitesAndOnlyTheLight(t *testing.T) {
	t.Cleanup(control_plane_services.SetEgressPolicyForTests(func(net.IP) error { return nil }))
	grantedID := newStatusSiteID()
	granted := registerStatusSite(t, grantedID, statusSite(t, infoAs(grantedID), healthy).URL, true)
	notGranted := registerStatusSite(t, newStatusSiteID(), "http://127.0.0.1:1", true)

	user := &users_models.User{
		ID: uuid.New(), Email: "picker-" + uuid.NewString() + "@example.com", Name: "Picker Test",
		PasswordCreationTime: time.Now().UTC(), Role: users_enums.UserRoleMember,
		Status: users_enums.UserStatusActive, CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, (&users_repositories.UserRepository{}).CreateUser(user))
	require.NoError(t, (&control_plane_repositories.GrantRepository{}).Create(&control_plane_models.SiteGrant{
		UserID: user.ID, SiteID: granted, Role: "ADMIN", CreatedBy: user.ID,
	}))

	gin.SetMode(gin.TestMode)
	router := gin.New()
	protected := router.Group("/api/v1", func(c *gin.Context) { c.Set("user", user); c.Next() })
	GetSiteController().RegisterRoutes(protected)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/control-plane/sites/status", nil))
	require.Equal(t, http.StatusOK, recorder.Code)

	var body struct {
		Statuses []map[string]any `json:"statuses"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.Len(t, body.Statuses, 1, "only the Site the user has a grant on")
	assert.Equal(t, granted, body.Statuses[0]["siteId"])
	assert.Equal(t, "up", body.Statuses[0]["status"])
	assert.Len(t, body.Statuses[0], 2, "no message, version or latency for non-admin callers")
	assert.NotEqual(t, notGranted, body.Statuses[0]["siteId"])
}
