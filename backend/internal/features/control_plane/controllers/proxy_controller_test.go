package control_plane_controllers

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
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

// fakeSite is an httptest Site: an exchange endpoint issuing tok-1, tok-2, ... and a
// proxied route that 401s the first token with poisonous headers and echoes the body
// otherwise. rejectAll 401s everything.
type fakeSite struct {
	server    *httptest.Server
	exchanges int64
	rejectAll bool
	// refuseExchange makes the exchange answer 403 with a Site-style error body.
	refuseExchange bool
	// failExchange, when set, answers the exchange instead: an outage or
	// rate limit rather than a refusal.
	failExchange http.HandlerFunc
}

func newFakeSite(t *testing.T, rejectAll bool) *fakeSite {
	t.Helper()
	f := &fakeSite{rejectAll: rejectAll}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/site/exchange", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&f.exchanges, 1)
		if f.failExchange != nil {
			f.failExchange(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if f.refuseExchange {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write(
				[]byte(
					`{"error":"this site has a local account with the same email that can't be used through a control plane"}`,
				),
			)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"token": fakeToken(n)})
	})
	mux.HandleFunc("/api/v1/logs/query", func(w http.ResponseWriter, r *http.Request) {
		if f.rejectAll || r.Header.Get("Authorization") == "Bearer "+fakeToken(1) {
			w.Header().Set("X-Site-Auth", "failed")
			w.Header().Set("WWW-Authenticate", "Bearer")
			w.Header().Set("Content-Type", "application/json")
			http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Site-Ok", "1")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

// fakeToken is a JWT-shaped string with a 1h exp; the control plane only reads exp.
func fakeToken(n int64) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, _ := json.Marshal(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "n": n})
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

// closeNotifyingRecorder: ReverseProxy calls CloseNotify via gin's writer, which panics
// on a bare httptest.Recorder.
type closeNotifyingRecorder struct{ *httptest.ResponseRecorder }

func (r *closeNotifyingRecorder) CloseNotify() <-chan bool { return make(chan bool) }

func newRecorder() *closeNotifyingRecorder {
	return &closeNotifyingRecorder{httptest.NewRecorder()}
}

func setupProxyFixture(t *testing.T, site *fakeSite, grantAdmin bool) (*gin.Engine, string) {
	t.Helper()
	require.NoError(t, control_plane_services.GetSigningKeyService().EnsureActiveKey())
	t.Cleanup(control_plane_services.SetEgressPolicyForTests(func(net.IP) error { return nil }))

	user := &users_models.User{
		ID: uuid.New(), Email: "proxy-" + uuid.NewString() + "@example.com", Name: "Proxy Test",
		PasswordCreationTime: time.Now().UTC(), Role: users_enums.UserRoleMember,
		Status: users_enums.UserStatusActive, CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, (&users_repositories.UserRepository{}).CreateUser(user))

	siteRow := &control_plane_models.Site{
		ID: "site-" + uuid.NewString()[:8], Name: "Fake", ApiURL: site.server.URL, Enabled: true,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, (&control_plane_repositories.SiteRepository{}).Create(siteRow))
	if grantAdmin {
		require.NoError(t, (&control_plane_repositories.GrantRepository{}).Create(&control_plane_models.SiteGrant{
			UserID: user.ID, SiteID: siteRow.ID, Role: "ADMIN", CreatedBy: user.ID,
		}))
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	// Stands in for the control plane's own security and CORS headers.
	fakeAuth := func(c *gin.Context) {
		c.Header("X-Frame-Options", "DENY")
		c.Header("Access-Control-Allow-Origin", "https://cp.example.com")
		c.Set("user", user)
		c.Next()
	}
	passthrough := func(c *gin.Context) { c.Next() }
	GetProxyController().RegisterRoutes(r, fakeAuth, passthrough)
	return r, siteRow.ID
}

// Regression for three retry bugs: gin's Flush committing a 200 during buffering, the
// body being consumed by the first attempt, and the 401's headers leaking into the retry.
func Test_Proxy_RetriesOnceOn401_WithReplayedBodyAndCleanHeaders(t *testing.T) {
	site := newFakeSite(t, false)
	r, siteID := setupProxyFixture(t, site, true)

	body := `{"query":{"op":"and"},"limit":50}`
	req := httptest.NewRequest(http.MethodPost, "/sites/"+siteID+"/api/v1/logs/query", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := newRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, body, rec.Body.String(), "the retry must replay the exact original body")
	assert.Equal(t, "1", rec.Header().Get("X-Site-Ok"))
	assert.Empty(t, rec.Header().Get("X-Site-Auth"), "nothing from the swallowed 401 may leak")
	assert.Empty(t, rec.Header().Get("WWW-Authenticate"))
	if cl := rec.Header().Get("Content-Length"); cl != "" {
		assert.Equal(t, len(body), rec.Body.Len())
		assert.Equal(
			t,
			strings.TrimSpace(cl),
			strconv.Itoa(len(body)),
			"a stale Content-Length from the 401 would truncate the real body",
		)
	}
	assert.EqualValues(t, 2, atomic.LoadInt64(&site.exchanges), "exactly one re-exchange")
	assert.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"), "the control plane's own headers survive the retry")
	assert.Equal(t, "https://cp.example.com", rec.Header().Get("Access-Control-Allow-Origin"))
}

// A repeat 401 must never reach the browser (it would log out the central session) or
// leak the Site's error headers/body.
func Test_Proxy_SecondUnauthorized_BecomesGeneric502(t *testing.T) {
	site := newFakeSite(t, true)
	r, siteID := setupProxyFixture(t, site, true)

	req := httptest.NewRequest(http.MethodPost, "/sites/"+siteID+"/api/v1/logs/query", strings.NewReader(`{}`))
	rec := newRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.JSONEq(t, `{"error":"site unreachable"}`, rec.Body.String())
	assert.Empty(t, rec.Header().Get("X-Site-Auth"))
	assert.Empty(t, rec.Header().Get("WWW-Authenticate"))
	assert.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
	assert.Equal(t, "https://cp.example.com", rec.Header().Get("Access-Control-Allow-Origin"))
}

// The per-request grant check is cached for proxyAccessTTL, no longer.
func Test_Proxy_RevokedGrant_StopsWorkingOnceTheAccessCacheExpires(t *testing.T) {
	site := newFakeSite(t, false)
	r, siteID := setupProxyFixture(t, site, true)
	request := func() int {
		rec := newRecorder()
		r.ServeHTTP(
			rec,
			httptest.NewRequest(http.MethodPost, "/sites/"+siteID+"/api/v1/logs/query", strings.NewReader(`{}`)),
		)
		return rec.Code
	}
	require.Equal(t, http.StatusOK, request())

	grants, err := (&control_plane_repositories.GrantRepository{}).ListForSite(siteID)
	require.NoError(t, err)
	for _, grant := range grants {
		require.NoError(t, (&control_plane_repositories.GrantRepository{}).Delete(grant.ID))
	}
	require.Equal(t, http.StatusOK, request(), "served from the access cache")
	expireProxyAccessCache()

	assert.Equal(t, http.StatusForbidden, request())
}

func expireProxyAccessCache() {
	proxyAccessCache.Range(func(key, value any) bool {
		if entry, ok := value.(proxyAccess); ok {
			entry.expires = time.Now().Add(-time.Second)
			proxyAccessCache.Store(key, entry)
		}
		return true
	})
}

func Test_Proxy_NoGrant_Forbidden_And_DeniedPath_NotFound(t *testing.T) {
	site := newFakeSite(t, false)
	r, siteID := setupProxyFixture(t, site, false)

	rec := newRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sites/"+siteID+"/api/v1/projects", nil))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(
		t,
		rec.Body.String(),
		"don't have access to this site",
		"the refusal carries a message the SPA can show",
	)

	rec = newRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/sites/"+siteID+"/api/v1/users/signin", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code, "denylisted paths are refused before any grant lookup")
	assert.JSONEq(t, `{"error":"not found"}`, rec.Body.String(), "a refusal carries a JSON body the SPA can show")

	rec = newRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sites/"+siteID+"/api/v1/projects/../users/signin", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code, "traversal is cleaned before the policy check")
	assert.Zero(t, atomic.LoadInt64(&site.exchanges), "a refused request must never mint anything")
}

// A Site's refusal at the exchange must reach the browser as a 403 with its message,
// not a generic 502.
func Test_Proxy_SiteRefusesAccount_Returns403WithReason(t *testing.T) {
	site := newFakeSite(t, false)
	site.refuseExchange = true
	r, siteID := setupProxyFixture(t, site, true)

	rec := newRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sites/"+siteID+"/api/v1/projects", nil))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "can't be used through a control plane")
}

func Test_Proxy_WhenTheSiteRateLimitsTheExchange_Returns502WithReasonAndRetryAfter(t *testing.T) {
	site := newFakeSite(t, false)
	site.failExchange = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "30")
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}
	r, siteID := setupProxyFixture(t, site, true)

	rec := newRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sites/"+siteID+"/api/v1/projects", nil))

	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Contains(t, rec.Body.String(), "rate-limiting sign-ins")
	assert.Equal(t, "30", rec.Header().Get("Retry-After"))
}

func Test_Proxy_WhenTheSiteFailsTheExchange_Returns502WithItsError(t *testing.T) {
	site := newFakeSite(t, false)
	site.failExchange = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"failed to fetch the control plane's signing keys"}`))
	}
	r, siteID := setupProxyFixture(t, site, true)

	rec := newRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sites/"+siteID+"/api/v1/projects", nil))

	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Contains(t, rec.Body.String(), "status 500")
	assert.Contains(t, rec.Body.String(), "signing keys")
}

func Test_Proxy_WhenTheSiteIsDown_Returns502SayingTheConnectionWasRefused(t *testing.T) {
	site := newFakeSite(t, false)
	r, siteID := setupProxyFixture(t, site, true)
	site.server.Close()

	rec := newRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sites/"+siteID+"/api/v1/projects", nil))

	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Contains(t, rec.Body.String(), "connection was refused")
}
