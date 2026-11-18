package mcp_tools_tests

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	correlation_core "obzly/internal/features/correlation/core"
	dashboards_dto "obzly/internal/features/dashboards/dto"
	dashboards_models "obzly/internal/features/dashboards/models"
	logs_core "obzly/internal/features/logs/core"
	spans_core "obzly/internal/features/spans/core"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_MCP_LogInsightTools_SummarizeTheProjectsLogs(t *testing.T) {
	e := newEnv(t, "MCP log insights")
	now := time.Now().UTC().Truncate(time.Minute)
	var items []*logs_core.LogItem
	for i := range 12 {
		items = append(items, logAt(now.Add(-time.Duration(i+1)*time.Minute), logs_core.LogLevelError,
			fmt.Sprintf("payment %d failed for order", i), map[string]any{"service_name": "checkout"}))
	}
	for i := range 4 {
		items = append(items, logAt(now.Add(-time.Duration(i+1)*time.Minute), logs_core.LogLevelInfo,
			fmt.Sprintf("request %d served", i), map[string]any{"service_name": "web"}))
	}
	// A long message: its sample is shortened.
	items = append(items, logAt(now.Add(-2*time.Minute), logs_core.LogLevelWarn,
		"retry scheduled "+strings.Repeat("x", 2000), map[string]any{"service_name": "payments"}))
	storeLogs(t, e.project.ID, items...)
	project := e.project.ID.String()

	trends := call(t, e.session, "log_pattern_trends", map[string]any{"project": project, "since": "1h"})
	patterns := list(t, trends, "patterns")
	require.NotEmpty(t, patterns, trends)
	assert.Equal(t, float64(17), trends["totalLines"])
	for _, p := range patterns {
		pattern := p.(map[string]any)
		assert.LessOrEqual(t, len(pattern["sample"].(string)), 300+len("…"))
		assert.NotContains(t, pattern, "series", "series only on request")
	}
	limited := call(t, e.session, "log_pattern_trends", map[string]any{"project": project, "limit": 1})
	assert.LessOrEqual(t, len(list(t, limited, "patterns")), 3, "the biggest one plus any changed ones")

	explained := call(t, e.session, "explain_logs", map[string]any{
		"project": project, "since": "30m", "query": "level=error", "minCount": 1,
	})
	assert.Equal(t, float64(12), explained["currentTotal"])
	assert.NotNil(t, explained["baseline"])
	list(t, explained, "fields")

	volume := call(t, e.session, "log_volume", map[string]any{"project": project, "days": 3})
	assert.Len(t, list(t, volume, "days"), 3)
	assert.Greater(t, volume["today"], float64(0))

	health := call(t, e.session, "log_health_map", map[string]any{"project": project, "since": "1h"})
	assert.Equal(t, "service", health["groupBy"])
	groups := list(t, health, "groups")
	require.NotEmpty(t, groups, health)

	message := callError(t, e.session, "log_health_map", map[string]any{"project": project, "since": "40d"})
	assert.Contains(t, message, "31 days")
}

func Test_MCP_TraceInsightTools_SummarizeRequestsAndErrors(t *testing.T) {
	e := newEnv(t, "MCP trace insights")
	now := time.Now().UTC()
	var spans []*spans_core.Span
	for i := range 6 {
		status := spans_core.StatusCodeOK
		if i%2 == 0 {
			status = spans_core.StatusCodeError
		}
		spans = append(spans, &spans_core.Span{
			ID: uuid.New(), ProjectID: e.project.ID, TraceID: uuid.NewString(), SpanID: fmt.Sprintf("s%d", i),
			Name: "GET /cart", ServiceName: "checkout", Kind: spans_core.SpanKindServer,
			StartTime: now.Add(-time.Duration(i+1) * time.Minute), StatusCode: status,
			EndTime: now.Add(-time.Duration(i+1)*time.Minute + 20*time.Millisecond),
		})
	}
	require.NoError(t, spans_core.GetSpanCoreRepository().StoreSpansBatch(map[uuid.UUID][]*spans_core.Span{
		e.project.ID: spans,
	}))
	require.NoError(t, spans_core.GetSpanCoreRepository().ForceFlush())
	project := e.project.ID.String()

	summary := call(t, e.session, "trace_summary", map[string]any{"project": project, "since": "1h"})
	current := summary["current"].(map[string]any)
	assert.Equal(t, float64(6), current["count"], summary)
	assert.Equal(t, float64(3), current["errors"], summary)
	assert.Contains(t, callError(t, e.session, "trace_summary", map[string]any{"project": project, "since": "8d"}),
		"7 days")

	errorsByService := call(t, e.session, "service_errors", map[string]any{"project": project, "limit": 5})
	services := list(t, errorsByService, "services")
	require.Len(t, services, 1, errorsByService)
	assert.Equal(t, "checkout", services[0].(map[string]any)["serviceName"])
	assert.Equal(t, float64(3), services[0].(map[string]any)["errors"])
}

func Test_MCP_IncidentImpact_RanksTheSessionsServices(t *testing.T) {
	e := newEnv(t, "MCP impact")
	start := time.Now().UTC().Add(-20 * time.Minute).Truncate(time.Minute)
	trigger := logAt(start.Add(10*time.Second), logs_core.LogLevelError, "checkout failed",
		map[string]any{"service.name": "checkout"})
	storeLogs(t, e.project.ID, trigger,
		logAt(start.Add(20*time.Second), logs_core.LogLevelError, "checkout failed again",
			map[string]any{"service.name": "checkout"}),
	)
	session := &correlation_core.IncidentSession{
		ProjectID: e.project.ID, ServiceName: "checkout", WindowStart: start, WindowEnd: start.Add(2 * time.Minute),
		TriggerType: correlation_core.TriggerTypeTraceIDLink, TriggerSummary: "test session",
	}
	require.NoError(t, correlation_core.GetIncidentSessionRepository().CreateSession(session,
		[]*correlation_core.IncidentSessionSignal{{
			SignalType: correlation_core.SignalTypeLog, SignalRef: correlation_core.SignalRefLog(trigger.ID.String()),
			OccurredAt: trigger.Timestamp,
		}}))

	impact := call(t, e.session, "incident_impact", map[string]any{
		"project": e.project.ID.String(), "sessionId": session.ID.String(),
	})
	services := list(t, impact, "services")
	require.NotEmpty(t, services, impact)
	assert.Equal(t, "checkout", services[0].(map[string]any)["serviceName"])
	assert.Equal(t, float64(2), services[0].(map[string]any)["errorLogs"])

	withImpact := call(t, e.session, "get_correlation_session", map[string]any{
		"project": e.project.ID.String(), "sessionId": session.ID.String(), "includeImpact": true,
	})
	assert.NotNil(t, withImpact["session"])
	assert.Equal(t, "checkout",
		list(t, withImpact["impact"].(map[string]any), "services")[0].(map[string]any)["serviceName"])
	assert.NotEmpty(t, withImpact["untrustedData"])

	message := callError(t, e.session, "incident_impact", map[string]any{
		"project": e.project.ID.String(), "sessionId": "not-a-session",
	})
	assert.Contains(t, message, "sessionId")
}

func Test_MCP_QueryDashboardPanel_ListsPanelsThenRunsOneWithVariables(t *testing.T) {
	e := newEnv(t, "MCP dashboard")
	now := time.Now().UTC()
	storeLogs(t, e.project.ID,
		logAt(now.Add(-3*time.Minute), logs_core.LogLevelError, "boom", nil),
		logAt(now.Add(-2*time.Minute), logs_core.LogLevelError, "boom again", nil),
		logAt(now.Add(-time.Minute), logs_core.LogLevelInfo, "fine", nil),
	)
	byLevel := &logs_core.QueryNode{Type: logs_core.QueryNodeTypeCondition, Condition: &logs_core.ConditionNode{
		Field: "level", Operator: logs_core.ConditionOperatorEquals, Value: "${level}",
	}}
	var dashboard dashboards_models.Dashboard
	test_utils.MakePostRequestAndUnmarshal(t, e.router, "/api/v1/projects/dashboards/"+e.project.ID.String(),
		"Bearer "+e.owner.Token, dashboards_dto.CreateDashboardRequestDTO{
			Title: "Errors",
			Tabs: []dashboards_models.Tab{{ID: "main", Title: "Main", Panels: dashboards_models.Panels{
				{ID: "count", Title: "Lines at level", Type: dashboards_models.PanelTypeStat, Query: byLevel,
					GridPos: dashboards_models.GridPos{W: 6, H: 4}},
			}}},
			Variables: []dashboards_models.Variable{{Key: "level", Label: "Level",
				Source: dashboards_models.VariableSourceStatic, StaticValues: []string{"error", "info"},
				DefaultValue: "error"}},
		}, http.StatusOK, &dashboard)
	args := map[string]any{"project": e.project.ID.String(), "dashboardId": dashboard.ID.String()}

	panels := call(t, e.session, "query_dashboard_panel", args)
	require.Len(t, list(t, panels, "panels"), 1)
	assert.Equal(t, "count", list(t, panels, "panels")[0].(map[string]any)["id"])

	args["panelId"] = "count"
	byDefault := call(t, e.session, "query_dashboard_panel", args)
	buckets := list(t, byDefault, "buckets")
	require.Len(t, buckets, 1, byDefault)
	assert.Equal(t, float64(2), buckets[0].(map[string]any)["value"], "the variable's default, error")

	args["variables"] = map[string]any{"level": "info"}
	picked := call(t, e.session, "query_dashboard_panel", args)
	assert.Equal(t, float64(1), list(t, picked, "buckets")[0].(map[string]any)["value"])
}
