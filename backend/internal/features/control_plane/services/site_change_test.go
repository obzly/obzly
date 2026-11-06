package control_plane_services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"obzly/internal/cache"
	control_plane_dto "obzly/internal/features/control_plane/dto"
	control_plane_models "obzly/internal/features/control_plane/models"
	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// exchangeSite is a Site whose exchange endpoint issues tokens naming it.
type exchangeSite struct {
	server    *httptest.Server
	name      string
	exchanges int64
}

func Test_TokenFor_AfterTheSitesURLChanges_NeverSendsTheOldAddressesToken(t *testing.T) {
	require.NoError(t, signingKeyService.EnsureActiveKey())
	t.Cleanup(SetEgressPolicyForTests(func(net.IP) error { return nil }))
	oldSite, newSite := newExchangeSite(t, "old"), newExchangeSite(t, "new")
	user := createTestUserRow(t)
	site := createTestSiteRow(t, oldSite.server.URL)
	require.NoError(t, grantRepository.Create(&control_plane_models.SiteGrant{
		UserID: user.ID, SiteID: site.ID, Role: GrantRoleSiteViewer, CreatedBy: user.ID,
	}))
	first, err := siteTokenService.TokenFor(user, site, []string{"pwd"})
	require.NoError(t, err)
	require.Equal(t, "old", tokenIssuer(t, first))

	// Repointed without UpdateSite's purge: the cached token alone must not follow the
	// Site to its new address.
	site.ApiURL = newSite.server.URL
	require.NoError(t, siteRepository.Update(site))
	second, err := siteTokenService.TokenFor(user, site, []string{"pwd"})

	require.NoError(t, err)
	assert.Equal(t, "new", tokenIssuer(t, second))
	assert.EqualValues(t, 1, atomic.LoadInt64(&newSite.exchanges))
}

func Test_UpdateSite_WhenTheURLChanges_PurgesCachedTokensAndRechecksTheKey(t *testing.T) {
	ctx := context.Background()
	client := cache.GetCache()
	site := createTestSiteRow(t, "https://site-a.example.com")
	checked := time.Now().UTC()
	site.SharedConfigKey, site.SharedConfigKeyCheckedAt = "pinned", &checked
	require.NoError(t, siteRepository.Update(site))
	cached := siteTokenKeyPrefix + uuid.NewString() + ":" + site.ID
	require.NoError(t, client.Do(ctx, client.B().Set().Key(cached).Value("x").Ex(time.Minute).Build()).Error())
	admin := &users_models.User{ID: uuid.New(), Role: users_enums.UserRoleAdmin}

	_, err := siteService.UpdateSite(admin, site.ID, control_plane_dto.UpdateSiteRequestDTO{
		Name: site.Name, ApiURL: "https://site-b.example.com", Enabled: true,
	})

	require.NoError(t, err)
	exists, err := client.Do(ctx, client.B().Exists().Key(cached).Build()).AsInt64()
	require.NoError(t, err)
	assert.Zero(t, exists)
	stored, err := siteRepository.GetByID(site.ID)
	require.NoError(t, err)
	assert.Equal(t, "pinned", stored.SharedConfigKey, "the new address must prove it holds the pinned key")
	assert.Nil(t, stored.SharedConfigKeyCheckedAt)
}

func Test_PurgeSite_DropsEveryUsersTokenAndRefusalForThatSiteOnly(t *testing.T) {
	ctx := context.Background()
	client := cache.GetCache()
	siteID, otherSiteID := "purge-"+uuid.NewString()[:8], "purge-"+uuid.NewString()[:8]
	keys := []string{
		siteTokenKeyPrefix + uuid.NewString() + ":" + siteID,
		siteTokenKeyPrefix + uuid.NewString() + ":" + siteID,
		siteTokenDeniedKeyPrefix + uuid.NewString() + ":" + siteID,
	}
	kept := siteTokenKeyPrefix + uuid.NewString() + ":" + otherSiteID
	for _, key := range append(keys, kept) {
		require.NoError(t, client.Do(ctx, client.B().Set().Key(key).Value("x").Ex(time.Minute).Build()).Error())
	}
	t.Cleanup(func() { _ = client.Do(ctx, client.B().Del().Key(kept).Build()) })

	siteTokenService.PurgeSite(siteID)

	for _, key := range keys {
		exists, err := client.Do(ctx, client.B().Exists().Key(key).Build()).AsInt64()
		require.NoError(t, err)
		assert.Zero(t, exists, key)
	}
	exists, err := client.Do(ctx, client.B().Exists().Key(kept).Build()).AsInt64()
	require.NoError(t, err)
	assert.EqualValues(t, 1, exists, "another Site's token stays")
}

func Test_ResolveEffectiveGrants_MatchesResolvingEachSite(t *testing.T) {
	user := createTestUserRow(t)
	first, second := createTestSiteRow(t, "https://a.example.com"), createTestSiteRow(t, "https://b.example.com")
	projectID := uuid.New()
	for _, grant := range []*control_plane_models.SiteGrant{
		{UserID: user.ID, SiteID: first.ID, Role: GrantRoleSiteMember, CreatedBy: user.ID},
		{UserID: user.ID, SiteID: first.ID, ProjectID: &projectID, Role: "PROJECT_VIEWER", CreatedBy: user.ID},
		{UserID: user.ID, SiteID: second.ID, ProjectID: &projectID, Role: "PROJECT_ADMIN", CreatedBy: user.ID},
	} {
		require.NoError(t, grantRepository.Create(grant))
	}

	all, err := grantService.ResolveEffectiveGrants(user.ID)

	require.NoError(t, err)
	for _, siteID := range []string{first.ID, second.ID} {
		one, err := grantService.ResolveEffectiveGrant(user.ID, siteID)
		require.NoError(t, err)
		assert.Equal(t, one, all[siteID])
	}
	assert.Equal(t, GrantRoleSiteMember, all[first.ID].SiteRole)
	assert.Equal(t, "PROJECT_ADMIN", all[second.ID].Projects[projectID])
	assert.Len(t, all, 2)
}

func Test_CheckSiteStatuses_WhenTheCallerHasGone_StillChecksAndCachesTheRealStatus(t *testing.T) {
	t.Cleanup(SetEgressPolicyForTests(func(net.IP) error { return nil }))
	siteID := "status-" + uuid.NewString()[:8]
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/site/info", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"siteId":%q,"version":"v1"}`, siteID)
	})
	mux.HandleFunc("/api/v1/system/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	site := &control_plane_models.Site{ID: siteID, ApiURL: server.URL, Enabled: true}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	statuses := CheckSiteStatuses(cancelled, []*control_plane_models.Site{site})

	require.Len(t, statuses, 1)
	assert.Equal(t, SiteStatusUp, statuses[0].Status, "a closed tab must not cache the Site as down")
}

func newExchangeSite(t *testing.T, name string) *exchangeSite {
	t.Helper()
	site := &exchangeSite{name: name}
	site.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&site.exchanges, 1)
		claims, _ := json.Marshal(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "iss": name})
		token := "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"token": token})
	}))
	t.Cleanup(site.server.Close)
	return site
}

func tokenIssuer(t *testing.T, token string) string {
	t.Helper()
	issuer, _ := decodeUnverifiedJWTPayload(t, token)["iss"].(string)
	return issuer
}
