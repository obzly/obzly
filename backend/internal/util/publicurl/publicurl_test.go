package publicurl

import (
	"net"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_BaseURL_FromRequestHost(t *testing.T) {
	if BaseURLIsFromConfig() {
		t.Skip("PUBLIC_URL is set in this environment")
	}

	r := httptest.NewRequest("GET", "http://obzly.local:4005/mcp", nil)
	assert.Equal(t, "http://obzly.local:4005", BaseURL(r))
}

func Test_BaseURLFromRequest_WhenPeerIsTrustedProxy_UsesForwardedHeaders(t *testing.T) {
	_, proxies, err := net.ParseCIDR("192.0.2.0/24")
	require.NoError(t, err)

	r := httptest.NewRequest("GET", "http://obzly.local:4005/mcp", nil)
	r.RemoteAddr = "192.0.2.10:51000"
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "obzly.example.com, proxy.internal")

	assert.Equal(t, "https://obzly.example.com", baseURLFromRequest(r, []*net.IPNet{proxies}))
}

func Test_BaseURLFromRequest_WhenPeerIsNotTrusted_IgnoresForwardedHeaders(t *testing.T) {
	_, proxies, err := net.ParseCIDR("192.0.2.0/24")
	require.NoError(t, err)

	r := httptest.NewRequest("GET", "http://obzly.local:4005/mcp", nil)
	r.RemoteAddr = "198.51.100.7:51000"
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "attacker.example.net")

	assert.Equal(t, "http://obzly.local:4005", baseURLFromRequest(r, []*net.IPNet{proxies}))
	assert.Equal(t, "http://obzly.local:4005", baseURLFromRequest(r, nil), "no TRUSTED_PROXIES trusts nobody")
}
