package control_plane_services

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_IsProxyPathAllowed(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		want   bool
	}{
		{"logs query allowed", http.MethodPost, "/api/v1/logs/query", true},
		{"streamed logs search allowed", http.MethodPost, "/api/v1/logs/query/stream/abc", true},
		{"logs field values allowed", http.MethodPost, "/api/v1/logs/query/field-values/abc", true},
		{"logs context allowed", http.MethodPost, "/api/v1/logs/query/context/abc", true},
		{"streamed panel query allowed", http.MethodPost, "/api/v1/projects/dashboards/abc/query/stream", true},
		{"logs tail allowed", http.MethodGet, "/api/v1/logs/tail/abc", true},
		{"ai usage allowed", http.MethodGet, "/api/v1/ai-usage/overview/abc", true},
		{"llm apps overview allowed", http.MethodGet, "/api/v1/ai-usage/llm/overview/abc", true},
		{"llm apps calls allowed", http.MethodGet, "/api/v1/ai-usage/llm/calls/abc", true},
		{"ai budgets allowed", http.MethodPut, "/api/v1/projects/abc/ai-budgets/def", true},
		{"ai budget status allowed", http.MethodGet, "/api/v1/projects/abc/ai-budgets/status", true},
		{"ai model prices allowed", http.MethodPost, "/api/v1/projects/abc/ai-model-prices/fill-common", true},
		{"alert rule activity allowed", http.MethodGet, "/api/v1/projects/abc/alert-rules/activity", true},
		{"alerts summary allowed", http.MethodGet, "/api/v1/projects/abc/alerts/summary", true},
		{"incident notifications allowed", http.MethodPut, "/api/v1/projects/abc/incident-notifications", true},
		{"message templates allowed", http.MethodPost, "/api/v1/settings/message-templates/preview", true},
		{"ingestion status allowed", http.MethodGet, "/api/v1/projects/abc/ingestion-status", true},
		{"web analytics report allowed", http.MethodGet, "/api/v1/projects/abc/web-analytics/report", true},
		{"web analytics settings allowed", http.MethodPut, "/api/v1/projects/abc/web-analytics/settings", true},
		{"inventory allowed", http.MethodGet, "/api/v1/inventory/assets/abc", true},
		{"cloud cost allowed", http.MethodPost, "/api/v1/cloud-cost/explore", true},
		{"posture allowed", http.MethodPatch, "/api/v1/posture/findings/0b9f1c2d-0000-0000-0000-000000000000", true},
		{"posture prefix only", http.MethodGet, "/api/v1/posturex", false},
		{"cost receiver never proxied", http.MethodPost, "/v1/cloud/cost", false},
		{"projects allowed", http.MethodGet, "/api/v1/projects", true},
		{"dashboards allowed", http.MethodGet, "/api/v1/dashboards/xyz", true},
		// A typo in these prefixes silently 404s a whole page through the control plane.
		{"service map allowed (real route is /service-map)", http.MethodGet, "/api/v1/service-map/abc", true},
		{"correlation sessions allowed", http.MethodGet, "/api/v1/correlation/sessions/abc", true},
		{"traces query allowed", http.MethodPost, "/api/v1/traces/query/abc", true},
		{"traces summary allowed", http.MethodPost, "/api/v1/traces/query/summary/abc", true},
		{"traces service errors allowed", http.MethodPost, "/api/v1/traces/query/service-errors/abc", true},
		{"incident impact allowed", http.MethodGet, "/api/v1/correlation/sessions/abc/def/impact", true},
		{"metrics query allowed", http.MethodPost, "/api/v1/metrics/query/abc", true},
		{"disk usage allowed", http.MethodGet, "/api/v1/disk/usage", true},
		{"audit logs allowed", http.MethodGet, "/api/v1/audit-logs/global", true},
		{"project api keys allowed", http.MethodGet, "/api/v1/projects/api-keys/abc", true},
		{"users me GET allowed", http.MethodGet, "/api/v1/users/me", true},
		{"users me PUT denied", http.MethodPut, "/api/v1/users/me", false},
		{"users list denied", http.MethodGet, "/api/v1/users", false},
		{"users signin denied", http.MethodPost, "/api/v1/users/signin", false},
		{"auth denied", http.MethodPost, "/api/v1/auth/site/exchange", false},
		{"sso denied", http.MethodGet, "/api/v1/sso/connectors", false},
		{"logs receiving denied", http.MethodPost, "/api/v1/logs/receiving/abc", false},
		{"docs denied", http.MethodGet, "/api/v1/docs/swagger/index.html", false},
		{"system denied", http.MethodGet, "/api/v1/system/health", false},
		{
			"system upgrade-status allowed (admin-only Site readiness)",
			http.MethodGet,
			"/api/v1/system/upgrade-status",
			true,
		},
		{"system upgrade-status denied for non-GET", http.MethodPost, "/api/v1/system/upgrade-status", false},
		{"system usage allowed (admin-only Site ingest and storage)", http.MethodGet, "/api/v1/system/usage", true},
		{"system usage denied for non-GET", http.MethodPost, "/api/v1/system/usage", false},
		{"root otlp denied (outside /api/v1)", http.MethodPost, "/v1/logs", false},
		{"jaeger root denied (outside /api/v1)", http.MethodGet, "/jaeger/api/traces", false},
		{"unknown api path denied (not allowlisted)", http.MethodGet, "/api/v1/something-new", false},
		{"bare /api/v1 denied", http.MethodGet, "/api/v1", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsProxyPathAllowed(tc.method, tc.path))
		})
	}
}
