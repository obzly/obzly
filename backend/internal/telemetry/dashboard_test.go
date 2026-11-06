package telemetry_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/model/rulefmt"
	"github.com/prometheus/prometheus/promql/parser"
	"github.com/stretchr/testify/require"
)

const shippedDashboardFile = "../../../deploy/observability/grafana/obzly-overview.json"

type dashboardPanel struct {
	ID      int    `json:"id"`
	Title   string `json:"title"`
	Targets []struct {
		Expr string `json:"expr"`
	} `json:"targets"`
}

// Guards the seed Grafana dashboard like the alert rules: every query must
// parse and reference only metrics Obzly defines or recording rules the
// rules file ships.
func Test_ShippedGrafanaDashboard_QueriesReferenceOnlyDefinedMetrics(t *testing.T) {
	raw, err := os.ReadFile(shippedDashboardFile)
	require.NoError(t, err)
	var dashboard struct {
		UID    string           `json:"uid"`
		Panels []dashboardPanel `json:"panels"`
	}
	require.NoError(t, json.Unmarshal(raw, &dashboard))
	require.NotEmpty(t, dashboard.UID)

	defined := definedMetricNames(t)
	groups, errs := rulefmt.ParseFile(
		shippedRulesFile, false, model.UTF8Validation, parser.NewParser(parser.Options{}),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	require.Empty(t, errs)
	for _, group := range groups.Groups {
		for _, rule := range group.Rules {
			if rule.Record != "" {
				defined[rule.Record] = true
			}
		}
	}

	ids := map[int]bool{}
	queries := 0
	for _, panel := range dashboard.Panels {
		require.Falsef(t, ids[panel.ID], "duplicate panel id %d", panel.ID)
		ids[panel.ID] = true
		for _, target := range panel.Targets {
			requireDefinedMetrics(t, "panel "+panel.Title, target.Expr, defined)
			queries++
		}
	}
	require.Positive(t, queries)
}
