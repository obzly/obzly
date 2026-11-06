package dashboards_services

import (
	"testing"

	"github.com/prometheus/prometheus/promql/parser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	metrics_core "obzly/internal/features/metrics/core"
)

// Every generated Overview query must parse, dotted OTel metric names
// included.
func Test_metricOverviewQuery_DottedNamesParse(t *testing.T) {
	cases := []struct {
		name  string
		m     metrics_core.MetricNameInfo
		query string
	}{
		{"legacy counter stays bare", metrics_core.MetricNameInfo{Name: "http_requests_total", Type: metrics_core.MetricTypeCounter}, "sum(rate(http_requests_total[5m]))"},
		{"legacy gauge stays bare", metrics_core.MetricNameInfo{Name: "process_cpu_seconds", Type: metrics_core.MetricTypeGauge}, "avg(process_cpu_seconds)"},
		{"dotted counter is quoted", metrics_core.MetricNameInfo{Name: "http.server.request.duration_count", Type: metrics_core.MetricTypeCounter}, `sum(rate({"__name__"="http.server.request.duration_count"}[5m]))`},
		{"dotted gauge is quoted", metrics_core.MetricNameInfo{Name: "system.cpu.time", Type: metrics_core.MetricTypeGauge}, `avg({"__name__"="system.cpu.time"})`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			query, title := metricOverviewQuery(tc.m)
			assert.Equal(t, tc.query, query)
			assert.Contains(t, title, tc.m.Name)
			_, err := parser.NewParser(parser.Options{}).ParseExpr(query)
			require.NoError(t, err, "generated query must be valid PromQL: %s", query)
		})
	}
}
