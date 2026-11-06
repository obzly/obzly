package control_plane_services

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	control_plane_models "obzly/internal/features/control_plane/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type echoResponse struct {
	AuthHeader   string `json:"authHeader"`
	CookieHeader string `json:"cookieHeader"`
	XApiKey      string `json:"xApiKey"`
	ForwardedFor string `json:"forwardedFor"`
}

// A caller-supplied Authorization/Cookie/X-Api-Key never reaches the Site; the minted
// Site token is what gets sent.
func Test_BuildReverseProxy_InjectsTokenAndStripsInboundCredentials(t *testing.T) {
	t.Cleanup(SetEgressPolicyForTests(func(net.IP) error { return nil })) // httptest is loopback-bound
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "site-session=leaked; Path=/")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(echoResponse{
			AuthHeader:   r.Header.Get("Authorization"),
			CookieHeader: r.Header.Get("Cookie"),
			XApiKey:      r.Header.Get("X-Api-Key"),
			ForwardedFor: r.Header.Get("X-Forwarded-For"),
		})
	}))
	defer site.Close()

	proxy, err := BuildReverseProxy(&control_plane_models.Site{ID: "site-a", ApiURL: site.URL}, "site-token-xyz", "203.0.113.9")
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "http://control-plane.local/api/v1/echo", nil)
	req.URL.Path = "/api/v1/echo"
	req.Header.Set("Authorization", "Bearer forged-central-token")
	req.Header.Set("Cookie", "central-session=abc")
	req.Header.Set("X-Api-Key", "sneaky-key")
	req.Header.Set("X-Forwarded-For", "1.2.3.4") // a spoof attempt - must be replaced, not appended to

	recorder := httptest.NewRecorder()
	proxy.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Empty(t, recorder.Header().Get("Set-Cookie"), "a Site's own Set-Cookie must never reach the browser")

	var parsed echoResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &parsed))
	assert.Equal(t, "Bearer site-token-xyz", parsed.AuthHeader, "the minted Site token must be what the Site actually sees")
	assert.Empty(t, parsed.CookieHeader, "the caller's own Cookie must never reach the Site")
	assert.Empty(t, parsed.XApiKey, "the caller's own X-Api-Key must never reach the Site")
	assert.Equal(t, "203.0.113.9", parsed.ForwardedFor,
		"the Site must see the gin-resolved client IP, not a caller-supplied X-Forwarded-For")
}

// An unreachable Site must never leak a raw Go dial error.
func Test_BuildReverseProxy_UnreachableSite_ReturnsGenericBadGateway(t *testing.T) {
	t.Cleanup(SetEgressPolicyForTests(func(net.IP) error { return nil }))
	proxy, err := BuildReverseProxy(&control_plane_models.Site{ID: "site-a", ApiURL: "http://127.0.0.1:1"}, "token", "")
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "http://control-plane.local/api/v1/echo", nil)
	req.URL.Path = "/api/v1/echo"

	recorder := httptest.NewRecorder()
	proxy.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusBadGateway, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "site unreachable")
}

// A Site behind an ingress answers 503 with an HTML page while down; the client must get
// JSON instead.
func Test_BuildReverseProxy_SiteReturnsHTML503_ReturnsJSONError(t *testing.T) {
	t.Cleanup(SetEgressPolicyForTests(func(net.IP) error { return nil }))
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("<html><head><title>503 Service Temporarily Unavailable</title></head></html>"))
	}))
	t.Cleanup(site.Close)

	proxy, err := BuildReverseProxy(&control_plane_models.Site{ID: "site-b", ApiURL: site.URL}, "token", "")
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodGet, "http://control-plane.local/api/v1/projects", nil)
	recorder := httptest.NewRecorder()
	proxy.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	assert.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
	var body map[string]string
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	assert.Contains(t, body["error"], "site site-b is unavailable (HTTP 503")
	assert.NotContains(t, recorder.Body.String(), "<html>")
}

// A Site's own JSON errors are passed through untouched.
func Test_BuildReverseProxy_SiteReturnsJSON500_PassesThrough(t *testing.T) {
	t.Cleanup(SetEgressPolicyForTests(func(net.IP) error { return nil }))
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"failed to execute query"}`))
	}))
	t.Cleanup(site.Close)

	proxy, err := BuildReverseProxy(&control_plane_models.Site{ID: "site-c", ApiURL: site.URL}, "token", "")
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodGet, "http://control-plane.local/api/v1/logs", nil)
	recorder := httptest.NewRecorder()
	proxy.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusInternalServerError, recorder.Code)
	assert.JSONEq(t, `{"error":"failed to execute query"}`, recorder.Body.String())
}

// A wide search runs up to a Site's query deadline (170s); the proxy must not give up
// first.
func Test_ProxyTransport_WaitsLongerThanASitesQueryDeadline(t *testing.T) {
	assert.Equal(t, 180*time.Second, proxyTransport.ResponseHeaderTimeout)
	assert.Greater(t, proxyTransport.ResponseHeaderTimeout, 170*time.Second)
}
