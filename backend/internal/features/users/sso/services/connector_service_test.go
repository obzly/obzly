package sso_services

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	sso_dto "obzly/internal/features/users/sso/dto"
	sso_models "obzly/internal/features/users/sso/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setUpConnectorAtURL is setUpConnector pointed at another issuer URL (here a
// counting proxy in front of the fake OIDC server).
func setUpConnectorAtURL(t *testing.T, issuerURL, clientID string) *sso_models.Connector {
	t.Helper()

	response, err := GetConnectorService().CreateConnector(&sso_dto.CreateConnectorRequestDTO{
		Name:         "Test Connector",
		ProviderType: sso_models.ProviderTypeGenericOIDC,
		IssuerURL:    issuerURL,
		ClientID:     clientID,
		ClientSecret: "fake-client-secret",
		ButtonLabel:  "Continue with Test",
		Enabled:      true,
	}, adminUserForTest())
	require.NoError(t, err)

	connector, err := connectorRepository.GetByID(response.ID)
	require.NoError(t, err)
	return connector
}

// A second call must not hit the IdP's discovery endpoint within discoveryCacheTTL.
func Test_ListPublicConnectors_CachesDiscoveryAcrossCalls(t *testing.T) {
	fake := newFakeOIDCServer(t, "test-client-id")

	var discoveryHits int64
	var proxyURL string
	countingProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		atomic.AddInt64(&discoveryHits, 1)

		resp, err := http.Get(fake.Server.URL + r.URL.Path)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		var doc map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&doc))
		// go-oidc requires the discovery "issuer" to match the URL asked for, so point
		// those fields at the proxy.
		for _, key := range []string{"issuer", "authorization_endpoint"} {
			if s, ok := doc[key].(string); ok {
				doc[key] = strings.Replace(s, fake.Server.URL, proxyURL, 1)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(doc))
	}))
	proxyURL = countingProxy.URL
	t.Cleanup(countingProxy.Close)

	// CreateConnector's own (uncached) discovery accounts for one hit already.
	connector := setUpConnectorAtURL(t, countingProxy.URL, fake.ClientID)
	require.True(t, connector.Enabled)
	baseline := atomic.LoadInt64(&discoveryHits)

	_, err := GetConnectorService().ListPublicConnectors("http://localhost/callback")
	require.NoError(t, err)
	afterFirst := atomic.LoadInt64(&discoveryHits)
	assert.Equal(t, baseline+1, afterFirst, "the first call should hit discovery once")

	_, err = GetConnectorService().ListPublicConnectors("http://localhost/callback")
	require.NoError(t, err)
	afterSecond := atomic.LoadInt64(&discoveryHits)
	assert.Equal(t, afterFirst, afterSecond, "the second call should be served from the cache, no new discovery hit")
}
