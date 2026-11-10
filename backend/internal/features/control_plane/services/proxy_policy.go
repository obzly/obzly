package control_plane_services

import (
	"net/http"
	"regexp"
	"strings"
)

// proxyDenylist is checked before proxyAllowlist, so an over-broad allowlist entry can't
// expose these: a Site's local auth, SSO, raw ingest, admin/docs and /users/ (see
// ARCHITECTURE.md "Multi-site: control plane and Sites").
var proxyDenylist = []*regexp.Regexp{
	regexp.MustCompile(`^/api/v1/auth/`),
	regexp.MustCompile(`^/api/v1/sso/`),
	regexp.MustCompile(`^/api/v1/logs/receiving/`),
	regexp.MustCompile(`^/api/v1/docs/`),
	regexp.MustCompile(`^/api/v1/system/`),
	regexp.MustCompile(`^/api/v1/users/`),
}

// proxyAllowlist is every Site-scoped route the frontend calls: the query/data surface,
// never identity/admin, which stays central.
var proxyAllowlist = []*regexp.Regexp{
	regexp.MustCompile(`^/api/v1/disk(/|$)`),
	regexp.MustCompile(`^/api/v1/audit-logs(/|$)`),
	regexp.MustCompile(`^/api/v1/settings(/|$)`),
	regexp.MustCompile(`^/api/v1/plans(/|$)`),
	regexp.MustCompile(`^/api/v1/projects(/|$)`),
	regexp.MustCompile(`^/api/v1/api-keys(/|$)`),
	regexp.MustCompile(`^/api/v1/logs/query(/|$)`),
	regexp.MustCompile(`^/api/v1/logs/tail(/|$)`),
	regexp.MustCompile(`^/api/v1/traces/query(/|$)`),
	regexp.MustCompile(`^/api/v1/metrics/query(/|$)`),
	regexp.MustCompile(`^/api/v1/service-map(/|$)`),
	regexp.MustCompile(`^/api/v1/correlation(/|$)`),
	regexp.MustCompile(`^/api/v1/dashboards(/|$)`),
	regexp.MustCompile(`^/api/v1/ai-usage(/|$)`),
	regexp.MustCompile(`^/api/v1/inventory(/|$)`),
	regexp.MustCompile(`^/api/v1/cloud-cost(/|$)`),
	regexp.MustCompile(`^/api/v1/posture(/|$)`),
}

// IsProxyPathAllowed checks an already path.Clean-ed sub-path. GET /api/v1/users/me is
// the one exception to the /users/ deny entry; the frontend needs it per Site.
func IsProxyPathAllowed(method, cleanedPath string) bool {
	if cleanedPath != "/api/v1" && !strings.HasPrefix(cleanedPath, "/api/v1/") {
		return false
	}
	if cleanedPath == "/api/v1/users/me" && method == http.MethodGet {
		return true
	}
	// The only /system/ route the UI needs per Site: admin-only and read-only.
	if cleanedPath == "/api/v1/system/upgrade-status" && method == http.MethodGet {
		return true
	}
	// Settings → Overview's per-Site ingest and storage; admin-only, read-only.
	if cleanedPath == "/api/v1/system/usage" && method == http.MethodGet {
		return true
	}
	for _, re := range proxyDenylist {
		if re.MatchString(cleanedPath) {
			return false
		}
	}
	for _, re := range proxyAllowlist {
		if re.MatchString(cleanedPath) {
			return true
		}
	}
	return false
}
