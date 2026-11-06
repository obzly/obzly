package control_plane_services

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"obzly/internal/util/egress"
	"time"

	"obzly/internal/config"
)

// Egress policy for every server-side call to a Site-supplied address (token exchange,
// proxy, connectivity check, Site logout). Enforced twice:
//  1. checkEgressAllowed: a pre-flight check so a bad address fails fast.
//  2. pinnedDialContext: the same policy on the IP actually dialed, connecting to that
//     checked IP directly. This is the one that matters: it closes the DNS-rebinding
//     window between pre-flight resolution and the transport's own resolution.
//
// Dialing an IP doesn't affect TLS: SNI and verification use the URL's hostname.

// egressIPPolicy decides whether ip may be dialed; a variable so tests can allow
// loopback (see SetEgressPolicyForTests).
var egressIPPolicy = defaultEgressIPPolicy

// defaultEgressIPPolicy is the SSRF floor (no loopback, link-local, unspecified or cloud
// metadata) plus CONTROL_PLANE_EGRESS_ALLOWLIST when set. Private ranges pass the floor
// on purpose (Sites usually live on internal networks); restrict them with the allowlist.
func defaultEgressIPPolicy(ip net.IP) error {
	networks, err := allowlistNetworks()
	if err != nil {
		return err
	}
	return egressIPPolicyWith(ip, networks)
}

func allowlistNetworks() ([]*net.IPNet, error) {
	allowlist := config.GetEnv().ControlPlaneEgressAllowlist
	if allowlist == "" {
		return nil, nil
	}
	networks, err := config.ParseCIDRList(allowlist)
	if err != nil {
		// Validated at startup, so the env changed under a running process. Fail closed.
		return nil, fmt.Errorf("invalid CONTROL_PLANE_EGRESS_ALLOWLIST: %w", err)
	}
	return networks, nil
}

func egressIPPolicyWith(ip net.IP, allowlist []*net.IPNet) error {
	if isDisallowedSiteIP(ip) {
		return fmt.Errorf("address %s is not allowed", ip)
	}
	if len(allowlist) == 0 {
		return nil
	}
	for _, network := range allowlist {
		if network.Contains(ip) {
			return nil
		}
	}
	return fmt.Errorf("address %s is outside CONTROL_PLANE_EGRESS_ALLOWLIST", ip)
}

// baseDialer has explicit timeouts; a zero-value net.Dialer has none.
var baseDialer = &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}

// pinnedDialContext resolves addr itself and refuses if ANY resolved address fails
// egressIPPolicy (mixed answers are what a rebinding attacker produces), then dials the
// checked IPs directly so the checked address is the connected one.
func pinnedDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return egress.DialContext(egressIPPolicy, baseDialer)(ctx, network, addr)
}

// newHardenedTransport is the transport for every outbound Site call: pinned dialer, no
// environment proxy (requests must go exactly to api_url), TLS >= 1.2 and a handshake
// timeout. Callers add their own per-purpose timeouts.
func newHardenedTransport() *http.Transport {
	return &http.Transport{
		Proxy:               nil,
		DialContext:         pinnedDialContext,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: 10 * time.Second,
		IdleConnTimeout:     90 * time.Second,
		MaxIdleConnsPerHost: 32,
	}
}

// checkEgressAllowed is the pre-flight check: resolve apiURL's host and apply
// egressIPPolicy to every address. Never the only defense; see pinnedDialContext.
func checkEgressAllowed(apiURL string) error {
	ctx, cancel := context.WithTimeout(context.Background(), egressLookupTimeout)
	defer cancel()
	return checkEgressAllowedContext(ctx, apiURL)
}

// egressLookupTimeout bounds the pre-flight DNS lookup.
const egressLookupTimeout = 5 * time.Second

func checkEgressAllowedContext(ctx context.Context, apiURL string) error {
	parsed, err := url.Parse(apiURL)
	if err != nil {
		return fmt.Errorf("invalid site url: %w", err)
	}
	host := parsed.Hostname()
	if host == "" {
		return errors.New("invalid site url: no host")
	}

	var ips []net.IP
	if literal := net.ParseIP(host); literal != nil {
		ips = []net.IP{literal}
	} else {
		resolved, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return fmt.Errorf("failed to resolve site address: %w", err)
		}
		for _, entry := range resolved {
			ips = append(ips, entry.IP)
		}
	}
	for _, ip := range ips {
		if err := egressIPPolicy(ip); err != nil {
			return fmt.Errorf("resolved %w", err)
		}
	}
	return nil
}

// CheckEgressAllowed exports checkEgressAllowed for the admin connectivity check.
func CheckEgressAllowed(apiURL string) error {
	return checkEgressAllowed(apiURL)
}

// SetEgressPolicyForTests swaps the per-IP policy (e.g. to allow a loopback
// httptest.Server) and returns a restore func. Test-only.
func SetEgressPolicyForTests(policy func(net.IP) error) (restore func()) {
	previous := egressIPPolicy
	egressIPPolicy = policy
	return func() { egressIPPolicy = previous }
}
