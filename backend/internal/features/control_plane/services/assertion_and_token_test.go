package control_plane_services

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	control_plane_models "obzly/internal/features/control_plane/models"
	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"
	users_repositories "obzly/internal/features/users/repositories"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTestUserRow(t *testing.T) *users_models.User {
	t.Helper()
	user := &users_models.User{
		ID:                   uuid.New(),
		Email:                "cp-test-" + uuid.NewString() + "@example.com",
		Name:                 "CP Test User",
		PasswordCreationTime: time.Now().UTC(),
		Role:                 users_enums.UserRoleMember,
		Status:               users_enums.UserStatusActive,
		CreatedAt:            time.Now().UTC(),
	}
	require.NoError(t, (&users_repositories.UserRepository{}).CreateUser(user))
	return user
}

func createTestSiteRow(t *testing.T, apiURL string) *control_plane_models.Site {
	t.Helper()
	site := &control_plane_models.Site{
		ID: "site-" + uuid.NewString()[:8], Name: "Test Site", ApiURL: apiURL, Enabled: true,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, siteRepository.Create(site))
	return site
}

func decodeUnverifiedJWTPayload(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	require.Len(t, parts, 3)
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var claims map[string]any
	require.NoError(t, json.Unmarshal(payload, &claims))
	return claims
}

func Test_AssertionService_Mint_CarriesGrantedProjectsAndAdminRole(t *testing.T) {
	require.NoError(t, signingKeyService.EnsureActiveKey())

	adminUser := createTestUserRow(t)
	site := createTestSiteRow(t, "https://site.example.com")
	require.NoError(t, grantRepository.Create(&control_plane_models.SiteGrant{
		UserID: adminUser.ID, SiteID: site.ID, ProjectID: nil, Role: "ADMIN", CreatedBy: adminUser.ID,
	}))

	raw, err := assertionService.Mint(adminUser, site.ID, "https://cp.example.com", []string{"mfa"})
	require.NoError(t, err)

	claims := decodeUnverifiedJWTPayload(t, raw)
	assert.Equal(t, "https://cp.example.com", claims["iss"])
	assert.Equal(t, site.ID, claims["aud"])
	assert.Equal(t, adminUser.ID.String(), claims["sub"])
	assert.Equal(t, "ADMIN", claims["site_role"])
	assert.NotEmpty(t, claims["jti"])

	// A project-scoped user gets site_role=MEMBER plus their project role.
	memberUser := createTestUserRow(t)
	projectID := uuid.New()
	require.NoError(t, grantRepository.Create(&control_plane_models.SiteGrant{
		UserID: memberUser.ID, SiteID: site.ID, ProjectID: &projectID, Role: "PROJECT_VIEWER", CreatedBy: adminUser.ID,
	}))

	raw2, err := assertionService.Mint(memberUser, site.ID, "https://cp.example.com", []string{"pwd"})
	require.NoError(t, err)
	claims2 := decodeUnverifiedJWTPayload(t, raw2)
	assert.Equal(t, "MEMBER", claims2["site_role"])
	projects, ok := claims2["projects"].([]any)
	require.True(t, ok)
	require.Len(t, projects, 1)
	entry, ok := projects[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, projectID.String(), entry["id"])
	assert.Equal(t, "PROJECT_VIEWER", entry["role"])
}

// A user with no grant on a Site must never get an assertion for it.
func Test_AssertionService_Mint_NoGrant_Errors(t *testing.T) {
	require.NoError(t, signingKeyService.EnsureActiveKey())
	user := createTestUserRow(t)
	site := createTestSiteRow(t, "https://site.example.com")

	_, err := assertionService.Mint(user, site.ID, "https://cp.example.com", []string{"pwd"})
	require.Error(t, err)
}

// Checks the token comes back, a repeat TokenFor hits the cache, and concurrent misses
// collapse into one exchange call.
func Test_SiteTokenService_CachesAndSingleflightsExchange(t *testing.T) {
	require.NoError(t, signingKeyService.EnsureActiveKey())

	// httptest.Server is loopback-bound, which the real egress policy refuses.
	t.Cleanup(SetEgressPolicyForTests(func(net.IP) error { return nil }))

	var callCount int64
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&callCount, 1)
		w.Header().Set("Content-Type", "application/json")
		token := fakeSiteToken(t, time.Now().Add(time.Hour))
		_ = json.NewEncoder(w).Encode(siteTokenExchangeResponse{Token: token})
	}))
	defer site.Close()

	user := createTestUserRow(t)
	siteRow := createTestSiteRow(t, site.URL)
	require.NoError(t, grantRepository.Create(&control_plane_models.SiteGrant{
		UserID: user.ID, SiteID: siteRow.ID, ProjectID: nil, Role: "ADMIN", CreatedBy: user.ID,
	}))

	token1, err := siteTokenService.TokenFor(user, siteRow, []string{"pwd"})
	require.NoError(t, err)
	assert.NotEmpty(t, token1)
	assert.EqualValues(t, 1, atomic.LoadInt64(&callCount))

	token2, err := siteTokenService.TokenFor(user, siteRow, []string{"pwd"})
	require.NoError(t, err)
	assert.Equal(t, token1, token2)
	assert.EqualValues(t, 1, atomic.LoadInt64(&callCount), "a cache hit must not call the site again")

	siteTokenService.Invalidate(user.ID, siteRow.ID)
	token3, err := siteTokenService.TokenFor(user, siteRow, []string{"pwd"})
	require.NoError(t, err)
	assert.NotEmpty(t, token3)
	assert.EqualValues(t, 2, atomic.LoadInt64(&callCount), "invalidation must force a fresh exchange")
}

// A Site token minted for a password-only session (amr=pwd) must not be reused once the
// session has MFA, nor the reverse: the Site decides MFA from the token's amr.
func Test_SiteTokenService_DifferentAMR_ExchangesSeparately(t *testing.T) {
	require.NoError(t, signingKeyService.EnsureActiveKey())
	t.Cleanup(SetEgressPolicyForTests(func(net.IP) error { return nil }))

	var callCount int64
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&callCount, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(siteTokenExchangeResponse{Token: fakeSiteToken(t, time.Now().Add(time.Hour))})
	}))
	defer site.Close()

	user := createTestUserRow(t)
	siteRow := createTestSiteRow(t, site.URL)
	require.NoError(t, grantRepository.Create(&control_plane_models.SiteGrant{
		UserID: user.ID, SiteID: siteRow.ID, ProjectID: nil, Role: "ADMIN", CreatedBy: user.ID,
	}))

	pwdToken, err := siteTokenService.TokenFor(user, siteRow, []string{"pwd"})
	require.NoError(t, err)
	mfaToken, err := siteTokenService.TokenFor(user, siteRow, []string{"mfa"})
	require.NoError(t, err)
	assert.NotEqual(t, pwdToken, mfaToken)
	assert.EqualValues(t, 2, atomic.LoadInt64(&callCount))

	siteTokenService.Invalidate(user.ID, siteRow.ID)
	_, _ = siteTokenService.TokenFor(user, siteRow, []string{"pwd"})
	_, _ = siteTokenService.TokenFor(user, siteRow, []string{"mfa"})
	assert.EqualValues(t, 4, atomic.LoadInt64(&callCount), "Invalidate clears both variants")
}

// fakeSiteToken builds an unsigned JWT with just an exp claim; SiteTokenService only
// decodes exp, it never verifies a Site's token.
func fakeSiteToken(t *testing.T, exp time.Time) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, err := json.Marshal(map[string]any{"exp": exp.Unix(), "sub": uuid.NewString()})
	require.NoError(t, err)
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".fakesignature"
}

// A Site's 4xx on the exchange is a *SiteRejectedError with the Site's message, and is
// negative-cached briefly; Invalidate clears it.
func Test_SiteTokenService_SiteRefusal_IsTypedAndNegativeCached(t *testing.T) {
	require.NoError(t, signingKeyService.EnsureActiveKey())
	t.Cleanup(SetEgressPolicyForTests(func(net.IP) error { return nil }))

	var callCount int64
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&callCount, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"this account has no access to this site"}`))
	}))
	defer site.Close()

	user := createTestUserRow(t)
	siteRow := createTestSiteRow(t, site.URL)
	require.NoError(t, grantRepository.Create(&control_plane_models.SiteGrant{
		UserID: user.ID, SiteID: siteRow.ID, ProjectID: nil, Role: "ADMIN", CreatedBy: user.ID,
	}))

	_, err := siteTokenService.TokenFor(user, siteRow, []string{"pwd"})
	var rejected *SiteRejectedError
	require.ErrorAs(t, err, &rejected)
	assert.Equal(t, http.StatusForbidden, rejected.Status)
	assert.Equal(t, "this account has no access to this site", rejected.Message)
	assert.EqualValues(t, 1, atomic.LoadInt64(&callCount))

	_, err = siteTokenService.TokenFor(user, siteRow, []string{"pwd"})
	require.ErrorAs(t, err, &rejected)
	assert.Equal(
		t,
		"this account has no access to this site",
		rejected.Message,
		"the remembered refusal keeps its message",
	)
	assert.EqualValues(t, 1, atomic.LoadInt64(&callCount), "a remembered refusal must not call the site again")

	siteTokenService.Invalidate(user.ID, siteRow.ID)
	_, _ = siteTokenService.TokenFor(user, siteRow, []string{"pwd"})
	assert.EqualValues(t, 2, atomic.LoadInt64(&callCount), "Invalidate clears the remembered refusal")
}
