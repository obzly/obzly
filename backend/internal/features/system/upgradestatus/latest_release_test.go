package system_upgradestatus

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	users_enums "obzly/internal/features/users/enums"
	users_middleware "obzly/internal/features/users/middleware"
	users_services "obzly/internal/features/users/services"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const releaseBody = `{"tag_name":"v1.2.0","html_url":"https://github.com/obzly/obzly/releases/tag/v1.2.0",
	"published_at":"2026-11-17T09:30:00Z","draft":false,"prerelease":false,"body":"notes"}`

type fakeGitHub struct {
	server   *httptest.Server
	hits     atomic.Int32
	status   int
	body     string
	delay    time.Duration
	requests chan *http.Request
}

func Test_GetLatestRelease_AsksGitHubAnonymously_AndCachesForSixHours(t *testing.T) {
	github := newFakeGitHub(t, http.StatusOK, releaseBody)
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	router, checker := releaseRouter(github.server.URL, true, &now)
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	release := getLatestRelease(t, router, admin.Token, "")
	assert.Equal(t, ReleaseKnown, release.Status)
	assert.Equal(t, "1.2.0", release.Version)
	assert.Equal(t, "https://github.com/obzly/obzly/releases/tag/v1.2.0", release.URL)
	require.NotNil(t, release.PublishedAt)
	assert.True(t, release.PublishedAt.Equal(time.Date(2026, 11, 17, 9, 30, 0, 0, time.UTC)))
	require.NotNil(t, release.CheckedAt)

	request := <-github.requests
	assert.Equal(t, releaseUserAgent, request.Header.Get("User-Agent"), "a generic client name")
	assert.Empty(t, request.Header.Get("Authorization"))
	assert.Empty(t, request.Header.Get("Cookie"))
	assert.Empty(t, request.URL.RawQuery, "nothing about this install in the URL")

	now = now.Add(5 * time.Hour)
	getLatestRelease(t, router, admin.Token, "")
	assert.Equal(t, int32(1), github.hits.Load(), "cached")
	getLatestRelease(t, router, admin.Token, "?refresh=true")
	assert.Equal(t, int32(2), github.hits.Load(), "Check again asks again")
	now = now.Add(30 * time.Second)
	getLatestRelease(t, router, admin.Token, "?refresh=true")
	assert.Equal(t, int32(2), github.hits.Load(), "but not more than once a minute")
	now = now.Add(6*time.Hour + time.Minute)
	getLatestRelease(t, router, admin.Token, "")
	assert.Equal(t, int32(3), github.hits.Load(), "asked again after six hours")
	assert.NotNil(t, checker.cached)
}

func Test_GetLatestRelease_WhenGitHubFailsOrHangs_IsUnknown(t *testing.T) {
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

	failing := newFakeGitHub(t, http.StatusForbidden, `{"message":"API rate limit exceeded"}`)
	router, _ := releaseRouter(failing.server.URL, true, &now)
	release := getLatestRelease(t, router, admin.Token, "")
	assert.Equal(t, ReleaseUnknown, release.Status)
	assert.Empty(t, release.Version, "never a version, so never \"up to date\"")
	assert.Equal(t, "GitHub answered 403", release.Error)
	now = now.Add(10 * time.Minute)
	getLatestRelease(t, router, admin.Token, "")
	assert.Equal(t, int32(1), failing.hits.Load(), "a failure isn't retried on every page load")
	now = now.Add(6 * time.Minute)
	getLatestRelease(t, router, admin.Token, "")
	assert.Equal(t, int32(2), failing.hits.Load(), "but after 15 minutes")

	garbled := newFakeGitHub(t, http.StatusOK, `{"tag_name":"nightly","html_url":"https://example.com/x"}`)
	router, _ = releaseRouter(garbled.server.URL, true, &now)
	assert.Equal(t, ReleaseUnknown, getLatestRelease(t, router, admin.Token, "").Status)

	hanging := newFakeGitHub(t, http.StatusOK, releaseBody)
	hanging.delay = 2 * time.Second
	router, checker := releaseRouter(hanging.server.URL, true, &now)
	checker.client.Timeout = 100 * time.Millisecond
	started := time.Now()
	release = getLatestRelease(t, router, admin.Token, "")
	assert.Equal(t, ReleaseUnknown, release.Status)
	assert.Equal(t, "GitHub couldn't be reached", release.Error)
	assert.Less(t, time.Since(started), time.Second, "bounded by the timeout")
}

func Test_GetLatestRelease_WhenTurnedOff_IsDisabledAndNeverAsks(t *testing.T) {
	github := newFakeGitHub(t, http.StatusOK, releaseBody)
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	router, _ := releaseRouter(github.server.URL, false, &now)
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	release := getLatestRelease(t, router, admin.Token, "?refresh=true")

	assert.Equal(t, ReleaseDisabled, release.Status)
	assert.Empty(t, release.Version)
	assert.Zero(t, github.hits.Load())
}

func Test_GetLatestRelease_WhenNotAdmin_ReturnsForbidden(t *testing.T) {
	github := newFakeGitHub(t, http.StatusOK, releaseBody)
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	router, _ := releaseRouter(github.server.URL, true, &now)
	member := users_testing.CreateTestUser(users_enums.UserRoleMember)

	test_utils.MakeGetRequest(t, router, "/api/v1/system/latest-release", "Bearer "+member.Token,
		http.StatusForbidden)
	assert.Zero(t, github.hits.Load())
}

func newFakeGitHub(t *testing.T, status int, body string) *fakeGitHub {
	t.Helper()
	fake := &fakeGitHub{status: status, body: body, requests: make(chan *http.Request, 10)}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.hits.Add(1)
		select {
		case fake.requests <- r.Clone(r.Context()):
		default:
		}
		if fake.delay > 0 {
			select {
			case <-time.After(fake.delay):
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(fake.status)
		_, _ = w.Write([]byte(fake.body))
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func releaseRouter(url string, enabled bool, now *time.Time) (*gin.Engine, *ReleaseChecker) {
	checker := NewReleaseChecker(url, func() bool { return enabled }, func() time.Time { return *now })
	gin.SetMode(gin.TestMode)
	router := gin.New()
	protected := router.Group("/api/v1")
	protected.Use(users_middleware.AuthMiddleware(users_services.GetUserService()))
	(&Controller{service, checker}).RegisterRoutes(protected)
	return router, checker
}

func getLatestRelease(t *testing.T, router *gin.Engine, token, query string) LatestReleaseDTO {
	t.Helper()
	var release LatestReleaseDTO
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/system/latest-release"+query, "Bearer "+token,
		http.StatusOK, &release)
	return release
}
