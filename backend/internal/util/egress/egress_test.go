package egress

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func Test_PublicOnly_RefusesNonPublicAddresses_UnlessAllowed(t *testing.T) {
	policy := PublicOnly(nil, nil)
	for _, address := range []string{
		"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1",
		"0.0.0.0", "224.0.0.1", "::1", "fe80::1", "fc00::1", "::ffff:10.0.0.1",
		// Reserved IPv4 and site-local IPv6.
		"0.1.2.3", "192.0.0.8", "198.18.0.1", "240.0.0.1", "255.255.255.255", "fec0::1",
		// IPv6 carrying a private IPv4 address: NAT64, 6to4.
		"64:ff9b::a00:5", "64:ff9b::169.254.169.254", "2002:a00:5::1", "2002:7f00:1::",
		// Teredo, local-use NAT64, IPv4-compatible.
		"2001:0:4136:e378:8000:63bf:3fff:fdd2", "64:ff9b:1::a00:5", "::a00:5",
	} {
		require.Error(t, policy(net.ParseIP(address)), address)
	}
	for _, address := range []string{"1.1.1.1", "2606:4700::1111", "64:ff9b::101:101", "2002:101:101::1"} {
		require.NoError(t, policy(net.ParseIP(address)), address)
	}

	allow, err := ParseCIDRs("10.0.0.0/8, 192.168.1.5")
	require.NoError(t, err)
	deny, err := ParseCIDRs("10.9.0.0/16,1.1.1.1")
	require.NoError(t, err)
	opted := PublicOnly(allow, deny)
	require.NoError(t, opted(net.ParseIP("10.1.2.3")))
	require.NoError(t, opted(net.ParseIP("192.168.1.5")))
	require.Error(t, opted(net.ParseIP("192.168.1.6")))
	require.Error(t, opted(net.ParseIP("10.9.0.1")), "deny wins over allow")
	require.Error(t, opted(net.ParseIP("1.1.1.1")), "deny covers public addresses too")
	require.Error(t, opted(net.ParseIP("64:ff9b::101:101")), "and their NAT64 form")
	_, err = ParseCIDRs("nope")
	require.Error(t, err)
}

// The client refuses a private target, a redirect, and a hostname whose
// answers include a forbidden address (DNS rebinding) - nothing reaches the
// receiver.
func Test_NewClient_RefusesPrivateTargets_Redirects_AndRebinding(t *testing.T) {
	hits := 0
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()

	_, err := NewClient(PublicOnly(nil, nil), 5*time.Second).Get(receiver.URL)
	require.ErrorContains(t, err, "not public", "loopback receiver refused")
	require.Zero(t, hits)

	// "localhost" resolves to loopback - a name never helps.
	_, err = NewClient(PublicOnly(nil, nil), 5*time.Second).Get("http://localhost:1/")
	require.ErrorContains(t, err, "refusing to dial localhost")

	allowLoopback, err := ParseCIDRs("127.0.0.0/8")
	require.NoError(t, err)
	client := NewClient(PublicOnly(allowLoopback, nil), 5*time.Second)
	response, err := client.Get(receiver.URL)
	require.NoError(t, err)
	_ = response.Body.Close()
	require.Equal(t, http.StatusNoContent, response.StatusCode)

	_, err = client.Get(receiver.URL + "/redirect")
	require.True(t, errors.Is(err, ErrRedirect), "redirects are refused: %v", err)
	require.Equal(t, 2, hits, "the redirect target was never fetched")

	// Rebinding: one forbidden answer among several refuses the host.
	mixed := func(net.IP) error { return nil }
	calls := 0
	rebinding := Policy(func(ip net.IP) error {
		calls++
		if ip.IsLoopback() {
			return errors.New("forbidden")
		}
		return mixed(ip)
	})
	dial := DialContext(rebinding, &net.Dialer{Timeout: time.Second})
	_, err = dial(context.Background(), "tcp", "localhost:80")
	require.ErrorContains(t, err, "refusing to dial localhost")
	require.Positive(t, calls)
}
