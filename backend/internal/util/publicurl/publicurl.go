// Package publicurl resolves this server's browser-facing origin.
package publicurl

import (
	"net"
	"net/http"
	"strings"
	"sync"

	"obzly/internal/config"
)

// BaseURL is PUBLIC_URL when set, else scheme://host from the request.
// X-Forwarded-Proto/Host count only when the direct peer is one of
// TRUSTED_PROXIES: anyone else could set them to steer OAuth metadata,
// token audiences and invite links. No trailing slash.
func BaseURL(r *http.Request) string {
	if public := config.GetEnv().PublicURL; public != "" {
		return public
	}
	return baseURLFromRequest(r, trustedProxies())
}

// MCPResource is the canonical resource URI of the MCP endpoint (RFC 8707).
func MCPResource(r *http.Request) string {
	return BaseURL(r) + "/mcp"
}

// BaseURLIsFromConfig reports whether PUBLIC_URL overrides the request.
func BaseURLIsFromConfig() bool {
	return config.GetEnv().PublicURL != ""
}

// trustedProxies is TRUSTED_PROXIES parsed once; startup already rejects
// an invalid value (main.go), so a parse error here just trusts nobody.
var trustedProxies = sync.OnceValue(func() []*net.IPNet {
	networks, err := config.ParseCIDRList(config.GetEnv().TrustedProxies)
	if err != nil {
		return nil
	}
	return networks
})

func baseURLFromRequest(r *http.Request, trusted []*net.IPNet) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host

	if peerIsTrusted(r.RemoteAddr, trusted) {
		if proto := firstValue(r.Header.Get("X-Forwarded-Proto")); proto == "https" || proto == "http" {
			scheme = proto
		}
		if forwarded := firstValue(r.Header.Get("X-Forwarded-Host")); forwarded != "" {
			host = forwarded
		}
	}
	return scheme + "://" + host
}

func peerIsTrusted(remoteAddr string, trusted []*net.IPNet) bool {
	if len(trusted) == 0 {
		return false
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, network := range trusted {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func firstValue(header string) string {
	value, _, _ := strings.Cut(header, ",")
	return strings.ToLower(strings.TrimSpace(value))
}
