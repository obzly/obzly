package sharedconfig

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/nacl/box"

	"obzly/internal/config"
	control_plane_models "obzly/internal/features/control_plane/models"
	control_plane_services "obzly/internal/features/control_plane/services"
	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"
	"obzly/internal/storage"
	"obzly/internal/util/rate_limit"
	test_utils "obzly/internal/util/testing"
)

// This process is both the control plane and the Site in these tests: the
// Site's key is this deployment's own.

func Test_GetDocument_WithoutTheSitesSignature_Returns401AndRecordsNothing(t *testing.T) {
	ownKey := ownPublicKey(t)
	site := createTestSite(t, "http://127.0.0.1:1", ownKey)
	router := documentRouter(&Controller{})

	test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method: http.MethodGet, URL: "/api/v1/shared-config/" + site.ID, ExpectedStatus: http.StatusUnauthorized,
	})
	forged := signedHeaders(t, site.ID, "abc", ownKey)
	forged[HeaderAppliedVersion] = "def"
	test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method: http.MethodGet, URL: "/api/v1/shared-config/" + site.ID, Headers: forged,
		ExpectedStatus: http.StatusUnauthorized,
	})

	stored := loadSite(t, site.ID)
	require.Nil(t, stored.SharedConfigFetchedAt)
	require.Empty(t, stored.SharedConfigVersion)
	require.Empty(t, stored.SharedConfigAppliedVersion)
}

func Test_GetDocument_SignedByTheSite_ServesItAndStatusFollowsTheAppliedVersion(t *testing.T) {
	require.NoError(t, control_plane_services.GetSigningKeyService().EnsureActiveKey())
	ownKey := ownPublicKey(t)
	site := createTestSite(t, "http://127.0.0.1:1", ownKey)
	router := documentRouter(&Controller{})

	response := test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method: http.MethodGet, URL: "/api/v1/shared-config/" + site.ID,
		Headers: signedHeaders(t, site.ID, "", ownKey), ExpectedStatus: http.StatusOK,
	})
	require.NotEmpty(t, response.Body)
	served := loadSite(t, site.ID)
	require.NotNil(t, served.SharedConfigFetchedAt)
	require.NotEmpty(t, served.SharedConfigVersion)
	require.False(t, siteStatus(t, site.ID).Current, "served is not applied")

	test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method: http.MethodGet, URL: "/api/v1/shared-config/" + site.ID,
		Headers: signedHeaders(t, site.ID, served.SharedConfigVersion, ownKey), ExpectedStatus: http.StatusOK,
	})
	status := siteStatus(t, site.ID)
	require.Equal(t, served.SharedConfigVersion, status.AppliedVersion)
	require.True(t, status.Current)
	require.Equal(t, Fingerprint(ownKey), status.KeyFingerprint)
}

func Test_GetDocument_WhenTheSitesKeyChanges_SealsToNothingNewUntilAnAdminAccepts(t *testing.T) {
	require.NoError(t, control_plane_services.GetSigningKeyService().EnsureActiveKey())
	t.Cleanup(control_plane_services.SetEgressPolicyForTests(func(net.IP) error { return nil }))
	ownKey := ownPublicKey(t)
	siteID := "key-" + uuid.NewString()[:8]
	info := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"siteId": siteID, "sharedConfigKey": ownKey})
	}))
	t.Cleanup(info.Close)
	previous, _, err := box.GenerateKey(rand.Reader)
	require.NoError(t, err)
	previousKey := base64.StdEncoding.EncodeToString(previous[:])
	site := createTestSiteWithID(t, siteID, info.URL, previousKey)
	router := documentRouter(&Controller{})
	admin := &users_models.User{ID: uuid.New(), Role: users_enums.UserRoleAdmin}
	router.POST("/api/v1/settings/sync/:siteId/accept-key", func(c *gin.Context) {
		c.Set("user", admin)
		c.Next()
	}, (&Controller{}).AcceptKey)

	test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method: http.MethodGet, URL: "/api/v1/shared-config/" + site.ID,
		Headers: signedHeaders(t, site.ID, "", ownKey), ExpectedStatus: http.StatusUnauthorized,
	})
	status := siteStatus(t, site.ID)
	require.Equal(t, Fingerprint(previousKey), status.KeyFingerprint, "the pinned key stays")
	require.Equal(t, Fingerprint(ownKey), status.PendingKeyFingerprint)
	require.NotNil(t, status.KeyCheckedAt)

	test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method: http.MethodPost, URL: "/api/v1/settings/sync/" + site.ID + "/accept-key",
		Body: AcceptKeyRequest{Fingerprint: Fingerprint(previousKey)}, ExpectedStatus: http.StatusConflict,
	})
	test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method: http.MethodPost, URL: "/api/v1/settings/sync/" + site.ID + "/accept-key",
		Body: AcceptKeyRequest{Fingerprint: Fingerprint(ownKey)}, ExpectedStatus: http.StatusOK,
	})
	test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method: http.MethodGet, URL: "/api/v1/shared-config/" + site.ID,
		Headers: signedHeaders(t, site.ID, "", ownKey), ExpectedStatus: http.StatusOK,
	})
	stored := loadSite(t, site.ID)
	require.Equal(t, ownKey, stored.SharedConfigKey)
	require.Empty(t, stored.SharedConfigPendingKey)
}

func Test_Refresh_SignsWithTheSitesKey_AndReportsWhatItApplied(t *testing.T) {
	require.NoError(t, control_plane_services.GetSigningKeyService().EnsureActiveKey())
	require.NoError(t, storage.GetDb().Exec("DELETE FROM shared_channels").Error)
	cleanChannels(t)
	site := createTestSite(t, "http://127.0.0.1:1", ownPublicKey(t))
	controlPlane := documentRouter(&Controller{})
	controlPlane.GET("/api/v1/shared-config-key", (&Controller{}).GetKey)
	controlPlane.GET(jwksSuffix, func(c *gin.Context) {
		jwks, err := control_plane_services.GetSigningKeyService().JWKS()
		require.NoError(t, err)
		c.JSON(http.StatusOK, jwks)
	})
	server := httptest.NewServer(controlPlane)
	t.Cleanup(server.Close)
	env := config.EnvVariables{
		SiteID: site.ID, ControlPlaneIssuer: config.GetEnv().PublicURL, ControlPlaneJWKSURL: server.URL + jwksSuffix,
	}

	require.NoError(t, Refresh(t.Context(), env))

	state, err := AppliedState()
	require.NoError(t, err)
	stored := loadSite(t, site.ID)
	require.Equal(t, state.Version, stored.SharedConfigVersion)
	require.Equal(t, state.Version, stored.SharedConfigAppliedVersion, "the Site reports at once what it applied")
	require.True(t, siteStatus(t, site.ID).Current)
}

func Test_Apply_WhenMonitorAlertsFail_LeavesTheVersionUnappliedForTheNextRefresh(t *testing.T) {
	cleanChannels(t)
	SetMonitorAlerts(nil, func([]MonitorAlert) error { return errors.New("alerting unavailable") })
	t.Cleanup(func() { SetMonitorAlerts(nil, nil) })

	_, err := apply(testDocument(t, "v-failed", time.Now()), time.Now().UTC())

	require.Error(t, err)
	state, err := AppliedState()
	require.NoError(t, err)
	require.Nil(t, state, "a document only partly applied must not count as applied")
}

func Test_GetDocument_WhenOneAddressAsksTooOften_Returns429(t *testing.T) {
	site := createTestSite(t, "http://127.0.0.1:1", ownPublicKey(t))
	router := documentRouter(&Controller{rate_limit.NewRateLimiter()})
	address := test_utils.RandomTestRemoteAddr()

	statuses := map[int]int{}
	for range documentRequestsPerMinute + 5 {
		response := test_utils.MakeRequest(t, router, test_utils.RequestOptions{
			Method: http.MethodGet, URL: "/api/v1/shared-config/" + site.ID, RemoteAddr: address,
		})
		statuses[response.StatusCode]++
	}

	require.Positive(t, statuses[http.StatusTooManyRequests])
	require.LessOrEqual(t, statuses[http.StatusUnauthorized], documentRequestsPerMinute+1)
}

func Test_Adopt_WhenAChannelIDBelongsToAnotherSitesChannel_RefusesToOverwriteIt(t *testing.T) {
	require.NoError(t, storage.GetDb().Exec("DELETE FROM shared_channels").Error)
	t.Cleanup(func() { storage.GetDb().Exec("DELETE FROM shared_channels") })
	site := createTestSite(t, "http://127.0.0.1:1", "")
	victim := SharedChannel{
		ID: uuid.New(), Name: "everyone", Kind: "slack", ConfigEncrypted: "kept", Scope: Scope{AllSites: true},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, storage.GetDb().Create(&victim).Error)
	sealed := sealedExport(t, SyncedChannel{
		ID: victim.ID, Name: "planted", Kind: "slack", Config: json.RawMessage(`{"url":"https://hooks.example.com/x"}`),
		AllProjects: true,
	})

	_, err := Adopt(site.ID, sealed, uuid.New())
	_, unknownErr := Adopt("no-such-site", sealed, uuid.New())

	require.ErrorIs(t, err, ErrChannelIDTaken)
	require.ErrorIs(t, unknownErr, ErrUnknownSite)
	var stored SharedChannel
	require.NoError(t, storage.GetDb().Where("id = ?", victim.ID).First(&stored).Error)
	require.Equal(t, "kept", stored.ConfigEncrypted)
	require.Equal(t, Scope{AllSites: true}, stored.Scope)
}

func documentRouter(controller *Controller) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v1/shared-config/:siteId", controller.GetDocument)
	return router
}

func ownPublicKey(t *testing.T) string {
	t.Helper()
	key, err := PublicKey()
	require.NoError(t, err)
	return key
}

// signedHeaders signs as the Site whose key pair is this deployment's, for
// a control plane whose public key is controlPlaneKey.
func signedHeaders(t *testing.T, siteID, applied, controlPlaneKey string) map[string]string {
	t.Helper()
	key, err := requestKey(controlPlaneKey)
	require.NoError(t, err)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	return map[string]string{
		HeaderSiteTime: timestamp, HeaderAppliedVersion: applied,
		HeaderSiteAuth: requestMAC(key, siteID, timestamp, applied),
	}
}

func createTestSite(t *testing.T, apiURL, key string) *control_plane_models.Site {
	t.Helper()
	return createTestSiteWithID(t, "sync-"+uuid.NewString()[:8], apiURL, key)
}

func createTestSiteWithID(t *testing.T, id, apiURL, key string) *control_plane_models.Site {
	t.Helper()
	site := &control_plane_models.Site{
		ID: id, Name: id, ApiURL: apiURL, Enabled: true, SharedConfigKey: key,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, storage.GetDb().Create(site).Error)
	t.Cleanup(func() { storage.GetDb().Where("id = ?", id).Delete(&control_plane_models.Site{}) })
	return site
}

func loadSite(t *testing.T, id string) control_plane_models.Site {
	t.Helper()
	var site control_plane_models.Site
	require.NoError(t, storage.GetDb().Where("id = ?", id).First(&site).Error)
	return site
}

func siteStatus(t *testing.T, id string) SiteSync {
	t.Helper()
	statuses, err := SyncStatus()
	require.NoError(t, err)
	for _, status := range statuses {
		if status.SiteID == id {
			return status
		}
	}
	t.Fatalf("no sync status for %s", id)
	return SiteSync{}
}

func sealedExport(t *testing.T, channels ...SyncedChannel) string {
	t.Helper()
	plain, err := json.Marshal(payload{Channels: channels})
	require.NoError(t, err)
	sealed, err := Seal(ownPublicKey(t), plain)
	require.NoError(t, err)
	return sealed
}
