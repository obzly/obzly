// Package egress makes outbound connections to user-configured addresses (alert
// webhooks, OAuth client metadata, Sites) safely: the dialer resolves the host itself,
// checks every address against a policy (one forbidden answer refuses the host, since
// mixed answers are what DNS rebinding produces) and connects to the address it checked.
package egress

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Policy refuses an address by returning an error.
type Policy func(net.IP) error

var (
	// Not public though Go's net.IP methods don't say so: carrier-grade NAT,
	// "this network", IETF protocol assignments, benchmarking, reserved
	// (with broadcast), and deprecated site-local IPv6.
	nonPublicNetworks = []*net.IPNet{
		mustCIDR("100.64.0.0/10"), mustCIDR("0.0.0.0/8"), mustCIDR("192.0.0.0/24"),
		mustCIDR("198.18.0.0/15"), mustCIDR("240.0.0.0/4"), mustCIDR("fec0::/10"),
	}
	// IPv6 forms that carry an IPv4 address with no single safe reading:
	// Teredo, local-use NAT64 and IPv4-compatible.
	opaqueIPv4Networks = []*net.IPNet{mustCIDR("2001::/32"), mustCIDR("64:ff9b:1::/48"), mustCIDR("::/96")}
	nat64Network       = mustCIDR("64:ff9b::/96")
	sixToFourNetwork   = mustCIDR("2002::/16")
)

// PublicOnly refuses non-public addresses (IsPublic) unless allow covers
// them, and anything deny covers - checked against an IPv4 address that
// NAT64 or 6to4 embeds too.
func PublicOnly(allow, deny []*net.IPNet) Policy {
	return func(ip net.IP) error {
		if v4 := ip.To4(); v4 != nil {
			ip = v4
		}
		for _, network := range deny {
			if network.Contains(ip) {
				return fmt.Errorf("address %s is denied", ip)
			}
			if embedded := embeddedIPv4(ip); embedded != nil && network.Contains(embedded) {
				return fmt.Errorf("address %s (%s) is denied", ip, embedded)
			}
		}
		if IsPublic(ip) {
			return nil
		}
		for _, network := range allow {
			if network.Contains(ip) {
				return nil
			}
		}
		return fmt.Errorf("address %s is not public", ip)
	}
}

// IsPublic reports whether ip is a public unicast address. An IPv6 address
// embedding an IPv4 one (NAT64, 6to4) is public only if that one is: on an
// IPv6 cluster, NAT64 to 10.0.0.5 reaches 10.0.0.5.
func IsPublic(ip net.IP) bool {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	for _, network := range nonPublicNetworks {
		if network.Contains(ip) {
			return false
		}
	}
	if len(ip) == net.IPv6len {
		for _, network := range opaqueIPv4Networks {
			if network.Contains(ip) {
				return false
			}
		}
	}
	if embedded := embeddedIPv4(ip); embedded != nil {
		return IsPublic(embedded)
	}
	return true
}

// ParseCIDRs parses a comma-separated CIDR list ("" for none); a bare IP
// is a single-address network.
func ParseCIDRs(list string) ([]*net.IPNet, error) {
	var networks []*net.IPNet
	for _, entry := range strings.Split(list, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if !strings.Contains(entry, "/") {
			ip := net.ParseIP(entry)
			if ip == nil {
				return nil, fmt.Errorf("invalid address %q", entry)
			}
			bits := 128
			if ip.To4() != nil {
				ip, bits = ip.To4(), 32
			}
			networks = append(networks, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		_, network, err := net.ParseCIDR(entry)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q: %w", entry, err)
		}
		networks = append(networks, network)
	}
	return networks, nil
}

// DialContext dials only addresses policy allows, connecting to the very
// addresses it checked.
func DialContext(policy Policy, dialer *net.Dialer) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		var ips []net.IP
		if literal := net.ParseIP(host); literal != nil {
			ips = []net.IP{literal}
		} else {
			resolved, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve %s: %w", host, err)
			}
			for _, entry := range resolved {
				ips = append(ips, entry.IP)
			}
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("%s resolved to no addresses", host)
		}
		for _, ip := range ips {
			if err := policy(ip); err != nil {
				return nil, fmt.Errorf("refusing to dial %s: %w", host, err)
			}
		}
		var lastErr error
		for _, ip := range ips {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
}

// ErrRedirect: the client never follows redirects (one to an internal
// address would bypass the check).
var ErrRedirect = errors.New("redirects are not followed")

// NewClient is an HTTP client through DialContext(policy): no proxy from
// the environment, TLS 1.2 or later, no redirects, timeout overall.
func NewClient(policy Policy, timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:               nil,
			DialContext:         DialContext(policy, dialer),
			TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout: 10 * time.Second,
			MaxIdleConns:        16,
			IdleConnTimeout:     90 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return ErrRedirect },
	}
}

// ReadLimited reads at most limit bytes of body and discards the rest.
func ReadLimited(body io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, limit))
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 1<<20))
	return data, err
}

// embeddedIPv4 is the IPv4 address a NAT64 (64:ff9b::/96) or 6to4
// (2002::/16) address carries, else nil.
func embeddedIPv4(ip net.IP) net.IP {
	if len(ip) != net.IPv6len {
		return nil
	}
	switch {
	case nat64Network.Contains(ip):
		return net.IPv4(ip[12], ip[13], ip[14], ip[15]).To4()
	case sixToFourNetwork.Contains(ip):
		return net.IPv4(ip[2], ip[3], ip[4], ip[5]).To4()
	}
	return nil
}

func mustCIDR(cidr string) *net.IPNet {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		panic(err)
	}
	return network
}
