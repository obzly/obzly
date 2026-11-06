package control_plane_services

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_checkEgressAllowed_RejectsLoopbackLiteral(t *testing.T) {
	assert.Error(t, checkEgressAllowed("http://127.0.0.1:9999"))
}

func Test_checkEgressAllowed_AllowsPrivateAddressWithNoAllowlist(t *testing.T) {
	// Private addresses are allowed by the SSRF floor; only the allowlist restricts them.
	assert.NoError(t, checkEgressAllowed("http://10.1.2.3:4005"))
}

func Test_egressIPPolicyWith_Allowlist(t *testing.T) {
	_, allowed, err := net.ParseCIDR("10.0.0.0/8")
	require.NoError(t, err)
	networks := []*net.IPNet{allowed}

	assert.NoError(t, egressIPPolicyWith(net.ParseIP("10.20.30.40"), networks))
	assert.Error(t, egressIPPolicyWith(net.ParseIP("192.168.1.1"), networks), "outside the allowlist")
	assert.Error(t, egressIPPolicyWith(net.ParseIP("127.0.0.1"), nil), "the floor applies with no allowlist")
	assert.Error(t, egressIPPolicyWith(net.ParseIP("169.254.169.254"), networks), "metadata address is refused even if allowlisted range would match")
	assert.Error(t, egressIPPolicyWith(net.ParseIP("::1"), nil))
	assert.Error(t, egressIPPolicyWith(net.ParseIP("0.0.0.0"), nil))
}

// DNS-rebinding property: the policy runs on what the hostname resolves to at dial time.
// "localhost" passes a string check but resolves to loopback, so the dial is refused.
func Test_pinnedDialContext_ChecksTheResolvedAddress(t *testing.T) {
	_, err := pinnedDialContext(context.Background(), "tcp", "localhost:1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to dial localhost")

	_, err = pinnedDialContext(context.Background(), "tcp", "127.0.0.1:1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to dial")
}

// With a permissive policy the dialer connects through a real http.Transport, and the
// pre-flight check agrees.
func Test_pinnedDialContext_DialsTheCheckedIP(t *testing.T) {
	t.Cleanup(SetEgressPolicyForTests(func(net.IP) error { return nil }))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	client := &http.Client{Transport: newHardenedTransport()}
	resp, err := client.Get(server.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	assert.NoError(t, checkEgressAllowed(server.URL))
}

// The dial-time check is independent: the real policy refuses loopback even with no
// pre-flight check.
func Test_pinnedDialContext_PolicyIsEnforcedAtDialNotJustPreflight(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("should never be reached"))
	}))
	defer server.Close()

	client := &http.Client{Transport: newHardenedTransport()}
	_, err := client.Get(server.URL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to dial")
}
