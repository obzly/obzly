package mcp_tools_tests

import (
	"context"
	"net/http"
	"testing"
	"time"

	"obzly/internal/features/alerting"
	"obzly/internal/storage"
	test_utils "obzly/internal/util/testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_MCP_NewTools_AreListedAsReadOnly(t *testing.T) {
	e := newEnv(t, "MCP tool list")

	tools, err := e.session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	byName := map[string]*mcp.Tool{}
	for _, tool := range tools.Tools {
		byName[tool.Name] = tool
	}
	for _, name := range []string{
		"list_alerts", "alert_summary", "list_alert_rules", "alert_rule_activity", "list_silences",
		"trace_summary", "service_errors", "incident_impact", "log_pattern_trends", "explain_logs", "log_volume",
		"log_health_map", "query_dashboard_panel", "ingestion_status", "inventory_summary", "list_assets",
		"get_asset", "asset_changes", "ai_usage_overview", "ai_budget_status", "list_monitors", "monitor_status",
		"web_analytics_report",
	} {
		tool, ok := byName[name]
		require.True(t, ok, name)
		assert.True(t, tool.Annotations.ReadOnlyHint, name)
		assert.NotEmpty(t, tool.Description, name)
	}
	for _, name := range []string{"log_pattern_trends", "list_alerts", "list_assets", "web_analytics_report"} {
		assert.Contains(t, byName[name].Description, "untrusted", name)
	}
}

func Test_MCP_AlertTools_ReadAlertsRulesActivityAndSilences(t *testing.T) {
	e := newEnv(t, "MCP alerts")
	base := "/api/v1/projects/" + e.project.ID.String()
	auth := "Bearer " + e.owner.Token

	var rule alerting.AlertRule
	test_utils.MakePostRequestAndUnmarshal(t, e.router, base+"/alert-rules", auth, alerting.RuleRequest{
		Name: "High error rate", Source: "metrics", Query: alerting.RuleQuery{PromQL: "vector(1)"},
		Comparison: ">", Threshold: 0.5, Labels: alerting.Labels{"team": "payments"},
	}, http.StatusOK, &rule)
	now := time.Now().UTC()
	for i, state := range []string{alerting.StateFiring, alerting.StateFiring, alerting.StateResolved} {
		require.NoError(t, storage.GetDb().Create(&alerting.AlertState{
			RuleID:      rule.ID,
			Fingerprint: string(rune('a' + i)),
			State:       state,
			Value:       float64(i),
			Labels: alerting.Labels{
				"pod": "checkout-" + string(rune('a'+i)),
			},
			ActiveAt:  now.Add(-time.Duration(i) * time.Minute),
			FiredAt:   &now,
			UpdatedAt: now,
		}).Error)
	}
	test_utils.MakePostRequest(t, e.router, base+"/silences", auth, alerting.SilenceRequest{
		Matchers: alerting.Matchers{{Name: "team", Value: "payments"}}, EndsAt: now.Add(time.Hour),
		Comment: "deploy",
	}, http.StatusOK)
	project := map[string]any{"project": e.project.Name}

	alerts := call(t, e.session, "list_alerts", project)
	assert.Len(t, list(t, alerts, "alerts"), 2, "resolved alerts are left out by default")
	assert.Equal(t, "High error rate", list(t, alerts, "alerts")[0].(map[string]any)["ruleName"])
	limited := call(t, e.session, "list_alerts", map[string]any{"project": e.project.Name, "limit": 1})
	assert.Len(t, list(t, limited, "alerts"), 1)
	assert.Equal(t, float64(2), limited["total"])
	require.NotEmpty(t, limited["nextCursor"])
	second := call(t, e.session, "list_alerts", map[string]any{"project": e.project.Name, "limit": 1,
		"cursor": limited["nextCursor"]})
	require.Len(t, list(t, second, "alerts"), 1)
	assert.Equal(t, float64(1), second["offset"])
	assert.NotContains(t, second, "nextCursor", "the last page has no cursor")
	assert.NotEqual(t, list(t, limited, "alerts")[0].(map[string]any)["labels"],
		list(t, second, "alerts")[0].(map[string]any)["labels"], "the second page holds the other alert")
	assert.Contains(t, callError(t, e.session, "list_alerts", map[string]any{"project": e.project.Name,
		"state": "resolved", "cursor": limited["nextCursor"]}), "another call",
		"a cursor only continues the call it came from")
	resolved := call(t, e.session, "list_alerts", map[string]any{"project": e.project.Name, "state": "resolved"})
	assert.Len(t, list(t, resolved, "alerts"), 1)

	summary := call(t, e.session, "alert_summary", project)
	assert.Equal(t, float64(2), summary["firing"])
	assert.Equal(t, float64(1), summary["rules"])
	assert.Equal(t, float64(1), summary["silences"])

	rules := call(t, e.session, "list_alert_rules", project)
	require.Len(t, list(t, rules, "rules"), 1)
	listed := list(t, rules, "rules")[0].(map[string]any)
	assert.Equal(t, "High error rate", listed["name"])
	assert.Equal(t, float64(0), listed["notificationChannels"])
	assert.NotContains(t, listed, "channelIds", "channels are never exposed")

	activity := call(t, e.session, "alert_rule_activity", project)
	assert.Len(t, list(t, activity, "days"), 7)
	for _, r := range list(t, activity, "rules") {
		assert.Equal(t, "High error rate", r.(map[string]any)["ruleName"])
	}

	silences := call(t, e.session, "list_silences", project)
	require.Len(t, list(t, silences, "silences"), 1)
	assert.Equal(t, true, list(t, silences, "silences")[0].(map[string]any)["active"])
}

func Test_MCP_ProjectTools_ForANonMember_ReturnAToolError(t *testing.T) {
	e := newEnv(t, "MCP private")
	project := e.project.ID.String()

	for name, args := range map[string]map[string]any{
		"list_alerts":           {"project": project},
		"alert_summary":         {"project": project},
		"list_alert_rules":      {"project": project},
		"alert_rule_activity":   {"project": project},
		"list_silences":         {"project": project},
		"trace_summary":         {"project": project},
		"service_errors":        {"project": project},
		"incident_impact":       {"project": project, "sessionId": "00000000-0000-0000-0000-000000000001"},
		"log_pattern_trends":    {"project": project},
		"explain_logs":          {"project": project},
		"log_volume":            {"project": project},
		"log_health_map":        {"project": project},
		"query_dashboard_panel": {"project": project, "dashboardId": "00000000-0000-0000-0000-000000000001"},
		"ingestion_status":      {"project": project},
		"inventory_summary":     {"project": project},
		"list_assets":           {"project": project},
		"asset_changes":         {"project": project},
		"ai_usage_overview":     {"project": project},
		"ai_budget_status":      {"project": project},
		"web_analytics_report":  {"project": project},
	} {
		t.Run(name, func(t *testing.T) {
			message := callError(t, e.outsider, name, args)
			assert.Regexp(t, `(?i)permission|access|forbidden|denied|can't read`, message)
		})
	}
}
