package mcp_tools_tests

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"obzly/internal/features/aiusage"
	"obzly/internal/features/ingestion"
	"obzly/internal/features/inventory"
	logs_core "obzly/internal/features/logs/core"
	"obzly/internal/storage"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_MCP_IngestionStatus_ListsTheProjectsSources(t *testing.T) {
	e := newEnv(t, "MCP ingestion")
	tracker := ingestion.GetTracker()
	tracker.ObserveLogs(e.project.ID, []*logs_core.LogItem{
		{ID: uuid.New(), Timestamp: time.Now().UTC(), Message: "hello",
			Fields: map[string]any{"obzly_source": "edge-proxy", "service_name": "nginx"}},
		{ID: uuid.New(), Timestamp: time.Now().UTC(), Message: "hello",
			Fields: map[string]any{"service_name": "checkout", "host_name": "vm-1"}},
	})
	require.NoError(t, tracker.Flush(context.Background()))

	status := call(t, e.session, "ingestion_status", map[string]any{"project": e.project.Name})
	assert.Len(t, list(t, status, "sources"), 2, status)
	limited := call(t, e.session, "ingestion_status", map[string]any{"project": e.project.Name, "limit": 1})
	assert.Len(t, list(t, limited, "sources"), 1)
	require.NotEmpty(t, limited["nextCursor"])
	rest := call(t, e.session, "ingestion_status", map[string]any{"project": e.project.Name, "limit": 1,
		"cursor": limited["nextCursor"]})
	assert.Len(t, list(t, rest, "sources"), 1)
	assert.NotContains(t, rest, "nextCursor")
	none := call(t, e.session, "ingestion_status", map[string]any{"project": e.project.Name, "signal": "traces"})
	assert.Empty(t, list(t, none, "sources"))
}

func Test_MCP_InventoryTools_ReadAssetsAndChangesOfTheCallersProjects(t *testing.T) {
	e := newEnv(t, "MCP inventory")
	now := time.Now().UTC()
	cluster := &inventory.Asset{ID: uuid.New(), ProjectID: e.project.ID, Kind: inventory.KindCluster,
		Key: "cluster:prd-" + uuid.NewString()[:6], Name: "prd", FirstSeen: now, LastSeen: now,
		State: inventory.StateActive, Health: inventory.HealthHealthy, UpdatedAt: now}
	var pods []*inventory.Asset
	for i := range 3 {
		pods = append(pods, &inventory.Asset{ID: uuid.New(), ProjectID: e.project.ID, Kind: inventory.KindPod,
			Key: fmt.Sprintf("pod:prd/shop/%s-%d", uuid.NewString()[:6], i), Name: fmt.Sprintf("checkout-%d", i),
			ParentKey: cluster.Key, Cluster: "prd", Namespace: "shop", FirstSeen: now, LastSeen: now,
			State: inventory.StateActive, Health: inventory.HealthCritical,
			Attributes: map[string]string{"k8s_pod_uid": "uid"},
			Reasons:    []inventory.Reason{{Health: inventory.HealthCritical, Text: "CrashLoopBackOff"}},
			UpdatedAt:  now})
	}
	require.NoError(t, storage.GetDb().Create(cluster).Error)
	for _, pod := range pods {
		require.NoError(t, storage.GetDb().Create(pod).Error)
	}
	require.NoError(t, storage.GetDb().Create(&inventory.Change{ID: uuid.New(), ProjectID: e.project.ID,
		AssetKey: pods[0].Key, AssetName: pods[0].Name, Kind: inventory.KindPod, Type: inventory.ChangeHealth,
		From: inventory.HealthHealthy, To: inventory.HealthCritical, At: now.Add(-time.Minute)}).Error)
	project := map[string]any{"project": e.project.ID.String()}

	summary := call(t, e.session, "inventory_summary", project)
	assert.Equal(t, float64(4), summary["total"], summary)

	assets := call(t, e.session, "list_assets", map[string]any{
		"project": e.project.ID.String(), "kind": "pod", "limit": 2,
	})
	assert.Len(t, list(t, assets, "assets"), 2)
	assert.Equal(t, float64(3), assets["total"])
	assert.Equal(t, true, assets["truncated"])
	first := list(t, assets, "assets")[0].(map[string]any)
	assert.Equal(t, "critical", first["health"])
	assert.NotContains(t, first, "attributes", "attributes only in get_asset")

	detail := call(t, e.session, "get_asset", map[string]any{"id": cluster.ID.String()})
	assert.Equal(t, "prd", detail["asset"].(map[string]any)["name"])
	assert.Len(t, list(t, detail, "children"), 3)
	podDetail := call(t, e.session, "get_asset", map[string]any{"id": pods[0].ID.String()})
	assert.Equal(t, "uid", podDetail["asset"].(map[string]any)["attributes"].(map[string]any)["k8s_pod_uid"])

	changes := call(t, e.session, "asset_changes", project)
	require.Len(t, list(t, changes, "changes"), 1)
	assert.Equal(t, "critical", list(t, changes, "changes")[0].(map[string]any)["to"])

	// Another user's asset is not found, not leaked.
	assert.Contains(t, callError(t, e.outsider, "get_asset", map[string]any{"id": cluster.ID.String()}),
		"not found")
}

func Test_MCP_AIUsageTools_ReadUsageAndBudgets(t *testing.T) {
	e := newEnv(t, "MCP AI usage")
	test_utils.MakePostRequest(t, e.router, "/api/v1/projects/"+e.project.ID.String()+"/ai-budgets",
		"Bearer "+e.owner.Token, aiusage.BudgetRequest{Name: "Monthly team budget", AmountUSD: 100,
			Period: aiusage.PeriodMonth, Scope: aiusage.ScopeProject}, http.StatusOK)
	project := e.project.ID.String()

	overview := call(t, e.session, "ai_usage_overview", map[string]any{"project": project})
	assert.Equal(t, "claude_code", overview["kind"])
	assert.NotNil(t, overview["summary"], overview)
	assert.NotContains(t, overview, "filterOptions")
	llm := call(t, e.session, "ai_usage_overview", map[string]any{"project": project, "kind": "llm"})
	assert.Equal(t, "service", llm["groupBy"])
	assert.Contains(t, callError(t, e.session, "ai_usage_overview", map[string]any{"project": project, "since": "40d"}),
		"31 days")

	budgets := call(t, e.session, "ai_budget_status", map[string]any{"project": project})
	require.Len(t, list(t, budgets, "budgets"), 1)
	budget := list(t, budgets, "budgets")[0].(map[string]any)
	assert.Equal(t, "Monthly team budget", budget["name"])
	assert.NotContains(t, budget, "channelIds")
	status, ok := budget["status"].(map[string]any)
	require.True(t, ok, budget)
	assert.Equal(t, float64(0), status["spentUsd"])
}

func Test_MCP_WebAnalyticsReport_SummarizesAccessLogs(t *testing.T) {
	e := newEnv(t, "MCP web analytics")
	now := time.Now().UTC()
	request := func(client, path, status string) map[string]any {
		return map[string]any{
			"service_name":    "nginx",
			"host_name":       "web-1",
			"remote_addr":     client,
			"method":          "GET",
			"path":            path,
			"status":          status,
			"body_bytes_sent": "100",
			"referer":         "-",
			"user_agent":      "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36",
		}
	}
	storeLogs(t, e.project.ID,
		logAt(now.Add(-3*time.Minute), logs_core.LogLevelInfo, "request", request("198.51.100.1", "/", "200")),
		logAt(now.Add(-2*time.Minute), logs_core.LogLevelInfo, "request", request("198.51.100.2", "/cart", "500")),
		logAt(now.Add(-time.Minute), logs_core.LogLevelInfo, "request", request("198.51.100.1", "/", "200")),
	)

	report := call(t, e.session, "web_analytics_report", map[string]any{
		"project": e.project.ID.String(), "since": "1h", "top": 1,
	})
	totals := report["totals"].(map[string]any)
	assert.Equal(t, float64(3), totals["requests"], report)
	assert.Equal(t, float64(1), totals["failed"])
	pages := report["top"].(map[string]any)["pages"].([]any)
	require.Len(t, pages, 1, "top is respected")
	assert.Equal(t, "/", pages[0].(map[string]any)["key"])
	assert.NotContains(t, report, "series")
}

func Test_MCP_MonitorTools_OnASingleInstall_SayThereAreNone(t *testing.T) {
	e := newEnv(t, "MCP monitors")

	for _, name := range []string{"list_monitors", "monitor_status"} {
		result := call(t, e.session, name, map[string]any{})
		assert.Empty(t, list(t, result, "monitors"))
		assert.Contains(t, result["note"], "control plane", name)
	}
}
