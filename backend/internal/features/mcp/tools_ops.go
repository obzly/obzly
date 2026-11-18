package mcp_server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	logs_core "obzly/internal/features/logs/core"
	metrics_querying "obzly/internal/features/metrics/querying"
	users_models "obzly/internal/features/users/models"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Operations tools: ingestion health, infrastructure inventory, AI usage and budgets,
// synthetic monitors and web analytics - each the data behind its page.

const (
	maxAIWindow           = 31 * 24 * time.Hour
	maxMonitorWindow      = 7 * 24 * time.Hour
	maxWebAnalyticsWindow = 31 * 24 * time.Hour
	defaultMonitorWindow  = 24 * time.Hour
	maxMonitors           = 100
	maxMonitorSites       = 10
	monitorMaxSeries      = 2000
	defaultTopRows        = 10
	maxTopRows            = 50
	maxAssetChildren      = 50
)

type ingestionStatusArgs struct {
	projectArgs
	Health string `json:"health,omitempty" jsonschema:"Only sources in this state: sending, quiet or stopped"`
	Signal string `json:"signal,omitempty" jsonschema:"Only sources sending this signal: logs, traces or metrics"`
	Limit  int    `json:"limit,omitempty"  jsonschema:"Max sources (default 50, max 200)"`
	Cursor string `json:"cursor,omitempty" jsonschema:"nextCursor from the previous page (same other arguments)"`
}

type inventorySummaryArgs struct {
	cloudProjectArgs
}

type listAssetsArgs struct {
	cloudProjectArgs
	Kind      string `json:"kind,omitempty"      jsonschema:"Comma-separated: cluster, node, host, namespace, workload, pod, container, docker_container, service"`
	Health    string `json:"health,omitempty"    jsonschema:"Comma-separated: healthy, warning, critical, inactive, unknown"`
	State     string `json:"state,omitempty"     jsonschema:"Comma-separated: new, active, not_reporting, terminated"`
	Cluster   string `json:"cluster,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Search    string `json:"search,omitempty"    jsonschema:"Matches name, node and attributes"`
	Limit     int    `json:"limit,omitempty"     jsonschema:"Max assets, worst health first (default 50, max 200)"`
}

type getAssetArgs struct {
	siteArg
	ID string `json:"id" jsonschema:"Asset id from list_assets"`
}

type assetChangesArgs struct {
	cloudProjectArgs
	Hours int `json:"hours,omitempty" jsonschema:"How far back (default 24, max 168)"`
	Limit int `json:"limit,omitempty" jsonschema:"Max changes, newest first (default 50, max 200)"`
}

type aiUsageArgs struct {
	projectArgs
	windowArgs
	Kind    string `json:"kind,omitempty"    jsonschema:"claude_code (Claude Code telemetry, default) or llm (OpenTelemetry gen_ai spans of your apps)"`
	GroupBy string `json:"groupBy,omitempty" jsonschema:"llm only: service (default), model, provider or operation"`
	Limit   int    `json:"limit,omitempty"   jsonschema:"Max rows per breakdown list (default 10, max 50)"`
}

type listMonitorsArgs struct {
	Site   string `json:"site,omitempty"   jsonschema:"Only monitors whose results are stored on this site (list_sites); omit for all"`
	Name   string `json:"name,omitempty"   jsonschema:"Only monitors whose name contains this (case-insensitive)"`
	Folder string `json:"folder,omitempty" jsonschema:"Only monitors in this folder"`
	Type   string `json:"type,omitempty"   jsonschema:"Only this type: http, tls, tcp, dns, icmp, multistep"`
	Limit  int    `json:"limit,omitempty"  jsonschema:"Max monitors, by name (default 100, max 200)"`
	Cursor string `json:"cursor,omitempty" jsonschema:"nextCursor from the previous page (same other arguments)"`
}

type monitorStatusArgs struct {
	windowArgs
	Site    string `json:"site,omitempty"    jsonschema:"Only monitors whose results are stored on this site (list_sites); omit for all"`
	Monitor string `json:"monitor,omitempty" jsonschema:"Monitor id, or part of its name; omit for every monitor"`
	Folder  string `json:"folder,omitempty"  jsonschema:"Only monitors in this folder"`
	Limit   int    `json:"limit,omitempty"   jsonschema:"Max monitors, by name (default 25, max 100)"`
	Cursor  string `json:"cursor,omitempty"  jsonschema:"nextCursor from the previous page (same other arguments)"`
}

type webAnalyticsArgs struct {
	projectArgs
	windowArgs
	Host    string   `json:"host,omitempty"    jsonschema:"Only this virtual host"`
	Sources []string `json:"sources,omitempty" jsonschema:"Only these access-log source keys (default all)"`
	Top     int      `json:"top,omitempty"     jsonschema:"Rows per top list (default 10, max 50)"`
}

// monitor is the summary of a synthetic monitor the tools read: never its request headers,
// body, assertions, secrets or alert channels.
type monitor struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Type            string   `json:"type"`
	Enabled         bool     `json:"enabled"`
	IntervalSeconds int      `json:"intervalSeconds"`
	DownLocations   int      `json:"downLocations"`
	Locations       []string `json:"locations"`
	Folder          string   `json:"folder"`
	ParentIDs       []string `json:"parentIds"`
	Destination     struct {
		Name      string `json:"name"`
		SiteID    string `json:"siteId"`
		ProjectID string `json:"projectId"`
	} `json:"destination"`
	Definition struct {
		Target  string `json:"target"`
		Request *struct {
			Method string `json:"method"`
			URL    string `json:"url"`
		} `json:"request"`
		Steps []struct {
			Request struct {
				URL string `json:"url"`
			} `json:"request"`
		} `json:"steps"`
	} `json:"definition"`
}

type locationState struct {
	Location string `json:"location"`
	Up       bool   `json:"up"`
	Warn     bool   `json:"warn"`
}

func init() {
	allowSiteCalls(http.MethodGet,
		"/projects/{project}/ingestion-status",
		"/inventory/summary", "/inventory/assets", "/inventory/assets/{asset}", "/inventory/changes",
		"/ai-usage/overview/{project}", "/ai-usage/llm/overview/{project}",
		"/projects/{project}/ai-budgets", "/projects/{project}/ai-budgets/status",
		"/projects/{project}/web-analytics/report",
	)
	// monitor_status reads results with /metrics/query/promql/{project}, already allowed.
	allowCentralCalls(http.MethodGet, "/synthetics/checks")
}

func (t *toolSet) registerOps(server *mcp.Server, add func(name, description string) *mcp.Tool) {
	mcp.AddTool(server, add("ingestion_status",
		"Sources sending to a project (by obzly_source, Kubernetes workload, or service and host) with each "+
			"signal's health (sending, quiet, stopped), last seen and recent rate. Sources seen in the last 7 "+
			"days. Use it when data seems missing."+paged+untrustedNote),
		wrap("ingestion_status", untrusted(t.ingestionStatus)))
	mcp.AddTool(server, add("inventory_summary",
		"Infrastructure inventory counts (clusters, nodes, hosts, workloads, pods, containers, services) by "+
			"kind, health and state, per project. Omit project for every project you can read."),
		wrap("inventory_summary", t.inventorySummary))
	mcp.AddTool(server, add("list_assets",
		"Infrastructure assets, worst health first, with health reasons and latest CPU/memory/disk. Filter by "+
			"kind, health, state, cluster, namespace or text."+notPaged+untrustedNote),
		wrap("list_assets", untrusted(t.listAssets)))
	mcp.AddTool(server, add("get_asset",
		"One infrastructure asset with its attributes, parents, node, children (at most 50) and recent "+
			"changes."+untrustedNote),
		wrap("get_asset", untrusted(t.getAsset)))
	mcp.AddTool(server, add("asset_changes",
		"Infrastructure change feed: assets that appeared or changed state or health, newest first (default "+
			"last 24 h, max 7 days)."+notPaged+untrustedNote),
		wrap("asset_changes", untrusted(t.assetChanges)))
	mcp.AddTool(server, add("ai_usage_overview",
		"AI usage for a window against the window before it: Claude Code cost, tokens, sessions, users, models, "+
			"tools and API errors, or (kind llm) your apps' gen_ai calls, tokens, errors, latency and cost by "+
			"service/model/provider/operation. Never message content. Window default 24h, at most 31 days. "+
			"Error text and tool names come from the monitored systems."+untrustedNote),
		wrap("ai_usage_overview", untrusted(t.aiUsageOverview)))
	mcp.AddTool(server, add("ai_budget_status",
		"A project's AI spending budgets with this period's spend, percent used, forecast and top users or "+
			"models."+untrustedNote),
		wrap("ai_budget_status", untrusted(t.aiBudgetStatus)))
	mcp.AddTool(server, add("list_monitors",
		"Synthetic monitors (HTTP, TLS, TCP, DNS, ICMP, multi-step): type, target, interval, locations, "+
			"folder and where results are stored (site and project). Definitions' headers, bodies and secrets are "+
			"never shown. Use monitor_status for whether they pass."+paged+untrustedNote),
		wrap("list_monitors", untrusted(t.listMonitors)))
	mcp.AddTool(server, add("monitor_status",
		"Synthetic monitors' latest status per location (up, warn, down; no data when nothing ran recently), "+
			"uptime percent and p95 duration over a window, read from their results like the Synthetics page. "+
			"Window default 24h, at most 7 days; at most 100 monitors. Maintenance windows are not left out of "+
			"uptime here."+paged+untrustedNote),
		wrap("monitor_status", untrusted(t.monitorStatus)))
	mcp.AddTool(server, add("web_analytics_report",
		"Web analytics from a project's access logs: requests, visitors, failures, bytes, p95 and top pages, "+
			"404s, referrers, IPs, browsers, systems, countries, hosts and status codes, against the window "+
			"before. Window default 24h, at most 31 days. The first call for a project starts its hourly rollups "+
			"(7 days backfilled), so early reports can miss hours."+untrustedNote),
		wrap("web_analytics_report", untrusted(t.webAnalyticsReport)))
}

func (t *toolSet) ingestionStatus(ctx context.Context, user *users_models.User, args ingestionStatusArgs) (any, error) {
	projectID, err := t.resolveProject(ctx, user, args.projectArgs)
	if err != nil {
		return nil, err
	}
	var response struct {
		Sources           []map[string]any `json:"sources"`
		Truncated         bool             `json:"truncated"`
		GeneratedAt       time.Time        `json:"generatedAt"`
		QuietAfterMinutes int              `json:"quietAfterMinutes"`
		StoppedAfterHours int              `json:"stoppedAfterHours"`
		RateWindowMinutes int              `json:"rateWindowMinutes"`
	}
	if err := t.exec.siteCall(ctx, user, args.Site, http.MethodGet,
		"/projects/"+projectID+"/ingestion-status", nil, nil, &response); err != nil {
		return nil, err
	}

	health, signal := strings.ToLower(args.Health), strings.ToLower(args.Signal)
	sources := make([]map[string]any, 0, len(response.Sources))
	for _, source := range response.Sources {
		if health != "" && source["health"] != health {
			continue
		}
		if signal != "" && !sendsSignal(source, signal) {
			continue
		}
		sources = append(sources, source)
	}
	filter := ingestionStatusArgs{projectArgs: args.projectArgs, Health: args.Health, Signal: args.Signal}
	page, offset, err := pageOf("ingestion_status", filter, args.Cursor, sources, clampRows(args.Limit, defaultRows))
	if err != nil {
		return nil, err
	}
	return pageResult("ingestion_status", filter, "sources", page, offset, len(sources), map[string]any{
		"generatedAt": response.GeneratedAt, "quietAfterMinutes": response.QuietAfterMinutes,
		"stoppedAfterHours": response.StoppedAfterHours, "rateWindowMinutes": response.RateWindowMinutes,
		"moreSourcesNotReported": response.Truncated,
	}), nil
}

func (t *toolSet) inventorySummary(
	ctx context.Context,
	user *users_models.User,
	args inventorySummaryArgs,
) (any, error) {
	query, err := t.cloudQuery(ctx, user, args.cloudProjectArgs)
	if err != nil {
		return nil, err
	}
	var response map[string]any
	return response, t.exec.siteCall(ctx, user, args.Site, http.MethodGet, "/inventory/summary", query, nil, &response)
}

func (t *toolSet) listAssets(ctx context.Context, user *users_models.User, args listAssetsArgs) (any, error) {
	query, err := t.cloudQuery(ctx, user, args.cloudProjectArgs)
	if err != nil {
		return nil, err
	}
	limit := clampRows(args.Limit, defaultRows)
	for name, value := range map[string]string{"kind": args.Kind, "health": args.Health, "state": args.State,
		"cluster": args.Cluster, "namespace": args.Namespace, "q": args.Search} {
		if strings.TrimSpace(value) != "" {
			query.Set(name, strings.TrimSpace(value))
		}
	}
	query.Set("limit", strconv.Itoa(limit))
	var response struct {
		Assets []map[string]any `json:"assets"`
		Total  int              `json:"total"`
	}
	if err := t.exec.siteCall(ctx, user, args.Site, http.MethodGet, "/inventory/assets", query, nil,
		&response); err != nil {
		return nil, err
	}
	assets := make([]map[string]any, 0, len(response.Assets))
	for _, asset := range response.Assets {
		assets = append(assets, compactAsset(asset))
	}
	return listResult("assets", assets, response.Total, limit, nil), nil
}

func (t *toolSet) getAsset(ctx context.Context, user *users_models.User, args getAssetArgs) (any, error) {
	if _, err := uuid.Parse(strings.TrimSpace(args.ID)); err != nil {
		return nil, errors.New("id must be an asset id from list_assets")
	}
	var response struct {
		Asset    map[string]any   `json:"asset"`
		Parents  []map[string]any `json:"parents"`
		Node     map[string]any   `json:"node"`
		Children []map[string]any `json:"children"`
		Changes  []map[string]any `json:"changes"`
	}
	if err := t.exec.siteCall(ctx, user, args.Site, http.MethodGet,
		"/inventory/assets/"+strings.TrimSpace(args.ID), nil, nil, &response); err != nil {
		return nil, err
	}
	asset := compactAsset(response.Asset)
	if attributes, ok := response.Asset["attributes"].(map[string]any); ok {
		asset["attributes"] = selectFields(attributes, nil)
	}
	children := make([]map[string]any, 0, min(len(response.Children), maxAssetChildren))
	for _, child := range response.Children[:min(len(response.Children), maxAssetChildren)] {
		children = append(children, compactAsset(child))
	}
	result := listResult("children", children, len(response.Children), maxAssetChildren, map[string]any{
		"asset": asset, "parents": response.Parents,
		"changes": response.Changes[:min(len(response.Changes), maxAssetChildren)],
	})
	if response.Node != nil {
		result["node"] = response.Node
	}
	return result, nil
}

func (t *toolSet) assetChanges(ctx context.Context, user *users_models.User, args assetChangesArgs) (any, error) {
	query, err := t.cloudQuery(ctx, user, args.cloudProjectArgs)
	if err != nil {
		return nil, err
	}
	limit := clampRows(args.Limit, defaultRows)
	query.Set("hours", strconv.Itoa(clampLimit(args.Hours, 24, 168)))
	// One more than asked tells whether there were more.
	query.Set("limit", strconv.Itoa(limit+1))
	var changes []map[string]any
	if err := t.exec.siteCall(ctx, user, args.Site, http.MethodGet, "/inventory/changes", query, nil,
		&changes); err != nil {
		return nil, err
	}
	result := listResult("changes", changes[:min(len(changes), limit)], min(len(changes), limit), limit,
		map[string]any{"hours": clampLimit(args.Hours, 24, 168)})
	if len(changes) > limit {
		result["truncated"] = true
		result["note"] = fmt.Sprintf("more than %d changes - raise limit or shorten hours", limit)
	}
	return result, nil
}

func (t *toolSet) aiUsageOverview(ctx context.Context, user *users_models.User, args aiUsageArgs) (any, error) {
	projectID, err := t.resolveProject(ctx, user, args.projectArgs)
	if err != nil {
		return nil, err
	}
	window, err := withDefault(args.windowArgs, 24*time.Hour).windowWithin(maxAIWindow)
	if err != nil {
		return nil, err
	}
	query := queryValues(map[string]string{
		"from": window.From.Format(time.RFC3339), "to": window.To.Format(time.RFC3339), "compare": "previous",
	})
	limit := clampLimit(args.Limit, defaultTopRows, maxTopRows)

	switch strings.ToLower(orDefault(args.Kind, "claude_code")) {
	case "claude_code":
		var response map[string]any
		if err := t.exec.siteCall(ctx, user, args.Site, http.MethodGet, "/ai-usage/overview/"+projectID, query, nil,
			&response); err != nil {
			return nil, err
		}
		result := map[string]any{"window": windowSummary(window), "kind": "claude_code",
			"summary": response["summary"], "previous": response["previous"],
			"latestEventAt": response["latestEventAt"],
			"note":          "costUsd is Claude Code's API-equivalent estimate, not necessarily what was billed"}
		for _, key := range []string{"users", "models", "tools", "errors"} {
			result[key] = topOf(response[key], limit)
		}
		return result, nil
	case "llm":
		query.Set("groupBy", orDefault(args.GroupBy, "service"))
		var response map[string]any
		if err := t.exec.siteCall(ctx, user, args.Site, http.MethodGet, "/ai-usage/llm/overview/"+projectID, query,
			nil, &response); err != nil {
			return nil, err
		}
		return map[string]any{"window": windowSummary(window), "kind": "llm", "groupBy": response["groupBy"],
			"summary": response["summary"], "previous": response["previous"], "latestCallAt": response["latestCallAt"],
			"breakdown": topOf(response["breakdown"], limit)}, nil
	default:
		return nil, errors.New("kind must be claude_code or llm")
	}
}

func (t *toolSet) aiBudgetStatus(ctx context.Context, user *users_models.User, args fieldsArgs) (any, error) {
	projectID, err := t.resolveProject(ctx, user, args.projectArgs)
	if err != nil {
		return nil, err
	}
	var budgets []map[string]any
	if err := t.exec.siteCall(ctx, user, args.Site, http.MethodGet,
		"/projects/"+projectID+"/ai-budgets", nil, nil, &budgets); err != nil {
		return nil, err
	}
	var statuses []map[string]any
	if err := t.exec.siteCall(ctx, user, args.Site, http.MethodGet,
		"/projects/"+projectID+"/ai-budgets/status", nil, nil, &statuses); err != nil {
		return nil, err
	}
	byID := make(map[any]map[string]any, len(statuses))
	for _, status := range statuses {
		byID[status["budgetId"]] = status
	}
	out := make([]map[string]any, 0, len(budgets))
	for _, budget := range budgets[:min(len(budgets), maxRows)] {
		entry := map[string]any{
			"id": budget["id"], "name": budget["name"], "amountUsd": budget["amountUsd"], "period": budget["period"],
			"timeZone": budget["timeZone"], "scope": budget["scope"], "scopeValue": budget["scopeValue"],
			"thresholds": budget["thresholds"], "includeClaudeCode": budget["includeClaudeCode"],
			"includeLlmApps": budget["includeLlmApps"],
		}
		if status, ok := byID[budget["id"]]; ok {
			delete(status, "budgetId")
			entry["status"] = status
		} else {
			entry["status"] = "no data"
		}
		out = append(out, entry)
	}
	return listResult("budgets", out, len(budgets), maxRows, nil), nil
}

func (t *toolSet) listMonitors(ctx context.Context, user *users_models.User, args listMonitorsArgs) (any, error) {
	monitors, err := t.monitors(ctx, user)
	if err != nil || monitors == nil {
		return noMonitors(err)
	}
	name, folder, kind := strings.ToLower(args.Name), args.Folder, strings.ToLower(args.Type)
	out := make([]map[string]any, 0, len(monitors))
	for _, m := range monitors {
		if (name != "" && !strings.Contains(strings.ToLower(m.Name), name)) || (folder != "" && m.Folder != folder) ||
			(kind != "" && m.Type != kind) || (args.Site != "" && m.Destination.SiteID != args.Site) {
			continue
		}
		out = append(out, map[string]any{
			"id": m.ID, "name": m.Name, "type": m.Type, "enabled": m.Enabled, "target": m.target(),
			"intervalSeconds": m.IntervalSeconds, "locations": m.Locations, "downLocations": m.DownLocations,
			"folder": m.Folder, "parentIds": m.ParentIDs, "destination": m.Destination,
		})
	}
	filter := args
	filter.Cursor, filter.Limit = "", 0
	page, offset, err := pageOf("list_monitors", filter, args.Cursor, out, clampRows(args.Limit, 100))
	if err != nil {
		return nil, err
	}
	return pageResult("list_monitors", filter, "monitors", page, offset, len(out), nil), nil
}

func (t *toolSet) monitorStatus(ctx context.Context, user *users_models.User, args monitorStatusArgs) (any, error) {
	window, err := withDefault(args.windowArgs, defaultMonitorWindow).windowWithin(maxMonitorWindow)
	if err != nil {
		return nil, err
	}
	all, err := t.monitors(ctx, user)
	if err != nil || all == nil {
		return noMonitors(err)
	}

	filter := strings.ToLower(strings.TrimSpace(args.Monitor))
	matching := make([]monitor, 0, len(all))
	for _, m := range all {
		if filter != "" && m.ID != filter && !strings.Contains(strings.ToLower(m.Name), filter) {
			continue
		}
		if (args.Folder != "" && m.Folder != args.Folder) || (args.Site != "" && m.Destination.SiteID != args.Site) {
			continue
		}
		matching = append(matching, m)
	}
	pageArgs := args
	pageArgs.Cursor, pageArgs.Limit = "", 0
	selected, offset, err := pageOf("monitor_status", pageArgs, args.Cursor, matching,
		clampLimit(args.Limit, 25, maxMonitors))
	if err != nil {
		return nil, err
	}

	type destinationKey struct{ site, project string }
	groups := map[destinationKey][]monitor{}
	var order []destinationKey
	for _, m := range selected {
		key := destinationKey{m.Destination.SiteID, m.Destination.ProjectID}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], m)
	}

	results := make([]map[string]any, 0, len(selected))
	var problems []string
	for i, key := range order {
		if i >= maxMonitorSites {
			problems = append(problems, fmt.Sprintf("results of %d more destinations were not read (limit %d) - "+
				"filter by monitor or folder", len(order)-maxMonitorSites, maxMonitorSites))
			break
		}
		statuses, err := t.destinationStatus(ctx, user, key.site, key.project, groups[key], window)
		if err != nil {
			problems = append(problems, fmt.Sprintf("site %q project %s: %v", key.site, key.project, err))
			continue
		}
		results = append(results, statuses...)
	}
	slices.SortStableFunc(results, func(a, b map[string]any) int {
		return statusRank(a["status"]) - statusRank(b["status"])
	})
	// The page is whole monitors in name order; results are sorted by status, so
	// the next page starts after every monitor selected here.
	result := map[string]any{"window": windowSummary(window), "monitors": results, "returned": len(results),
		"total": len(matching), "offset": offset}
	addNextPage(result, "monitor_status", pageArgs, "monitors", offset, len(selected), len(matching))
	if len(problems) > 0 {
		result["errors"] = problems
	}
	return result, nil
}

func (t *toolSet) webAnalyticsReport(ctx context.Context, user *users_models.User, args webAnalyticsArgs) (any, error) {
	projectID, err := t.resolveProject(ctx, user, args.projectArgs)
	if err != nil {
		return nil, err
	}
	window, err := withDefault(args.windowArgs, 24*time.Hour).windowWithin(maxWebAnalyticsWindow)
	if err != nil {
		return nil, err
	}
	query := queryValues(map[string]string{
		"from": window.From.Format(time.RFC3339), "to": window.To.Format(time.RFC3339), "host": args.Host,
		"sources": strings.Join(args.Sources, ","),
	})
	var report struct {
		Rollups     map[string]any              `json:"rollups"`
		Live        map[string]any              `json:"live"`
		Sources     []map[string]any            `json:"sources"`
		Hosts       []map[string]any            `json:"hosts"`
		Totals      map[string]any              `json:"totals"`
		Previous    map[string]any              `json:"previous"`
		Panels      map[string][]map[string]any `json:"panels"`
		Geo         map[string]any              `json:"geo"`
		IPMode      string                      `json:"ipMode"`
		ExcludeBots bool                        `json:"excludeBots"`
	}
	if err := t.exec.siteCall(ctx, user, args.Site, http.MethodGet,
		"/projects/"+projectID+"/web-analytics/report", query, nil, &report); err != nil {
		return nil, err
	}
	top := clampLimit(args.Top, defaultTopRows, maxTopRows)
	sources := make([]map[string]any, 0, min(len(report.Sources), maxTopRows))
	for _, source := range report.Sources[:min(len(report.Sources), maxTopRows)] {
		sources = append(sources, map[string]any{"key": source["key"], "name": source["name"],
			"kind": source["kind"], "requests": source["requests"], "share": source["share"]})
	}
	panels := make(map[string]any, len(report.Panels))
	for name, rows := range report.Panels {
		for _, row := range rows {
			if key, ok := row["key"].(string); ok {
				row["key"] = short(key, maxFieldChars)
			}
		}
		panels[name] = rows[:min(len(rows), top)]
	}
	result := map[string]any{
		"window": windowSummary(window), "totals": report.Totals, "previous": report.Previous,
		"rollups": report.Rollups, "live": report.Live, "sources": sources, "hosts": topOf(report.Hosts, top),
		"top": panels, "ipMode": report.IPMode, "excludeBots": report.ExcludeBots,
		"geoAvailable": report.Geo["available"],
	}
	if report.Previous == nil {
		result["previous"] = "no data"
	}
	return result, nil
}

// monitors is every synthetic monitor's summary, or nil on a server without them (they live
// on a multi-site control plane).
func (t *toolSet) monitors(ctx context.Context, user *users_models.User) ([]monitor, error) {
	if !t.exec.controlPlane {
		return nil, nil
	}
	var monitors []monitor
	if err := t.exec.centralCall(ctx, user, http.MethodGet, "/synthetics/checks", nil, &monitors); err != nil {
		return nil, err
	}
	slices.SortStableFunc(monitors, func(a, b monitor) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return monitors, nil
}

// destinationStatus reads one destination's monitors' results the way the Synthetics page
// does (frontend/src/features/synthetics/lib/useMonitorData.ts): the latest run per location
// within each monitor's status window, the share of runs that passed, and the p95 of total
// durations merged across locations.
func (t *toolSet) destinationStatus(
	ctx context.Context, user *users_models.User, site, projectID string, monitors []monitor,
	window *logs_core.TimeRangeDTO,
) ([]map[string]any, error) {
	if _, err := uuid.Parse(projectID); err != nil {
		return nil, errors.New("monitor destination has no valid project")
	}
	ids := make([]string, 0, len(monitors))
	minInterval := 0
	byWindow := map[int][]string{}
	for _, m := range monitors {
		ids = append(ids, m.ID)
		if _, err := uuid.Parse(m.ID); err != nil {
			return nil, fmt.Errorf("invalid monitor id %q", m.ID)
		}
		if minInterval == 0 || m.IntervalSeconds < minInterval {
			minInterval = m.IntervalSeconds
		}
		statusWindow := 2*m.IntervalSeconds + 60
		byWindow[statusWindow] = append(byWindow[statusWindow], m.ID)
	}
	instant := func(query string) ([]metrics_querying.PromQLSeriesDTO, error) {
		request := metrics_querying.PromQLQueryRequestDTO{
			Query: query, End: window.To, Instant: true, MaxSeries: monitorMaxSeries,
		}
		var response metrics_querying.PromQLQueryResponseDTO
		if err := t.exec.siteCall(ctx, user, site, http.MethodPost, "/metrics/query/promql/"+projectID, nil,
			request, &response); err != nil {
			return nil, err
		}
		return response.Series, nil
	}

	var up, warn []metrics_querying.PromQLSeriesDTO
	for statusWindow, members := range byWindow {
		only := `check_id=~"` + strings.Join(members, "|") + `"`
		ups, err := instant(fmt.Sprintf(`min by (check_id, location) (last_over_time(synthetics_up{%s}[%ds]))`,
			only, statusWindow))
		if err != nil {
			return nil, err
		}
		warns, err := instant(fmt.Sprintf(`max by (check_id, location) (last_over_time(synthetics_warn{%s}[%ds]))`,
			only, statusWindow))
		if err != nil {
			return nil, err
		}
		up, warn = append(up, ups...), append(warn, warns...)
	}

	only := `check_id=~"` + strings.Join(ids, "|") + `"`
	seconds := int(window.To.Sub(*window.From).Seconds())
	resolution := max(30, minInterval/2)
	uptime, err := instant(fmt.Sprintf(
		`sum by (check_id) (sum_over_time(synthetics_up{%[1]s}[%[2]ds])) / `+
			`sum by (check_id) (count_over_time(synthetics_up{%[1]s}[%[2]ds]))`, only, seconds))
	if err != nil {
		return nil, err
	}
	p95, err := instant(fmt.Sprintf(
		`max by (check_id) (quantile_over_time(0.95, (max by (check_id, location) `+
			`(max_over_time(synthetics_duration_seconds{phase="total",%s}[%ds])))[%ds:%ds]))`,
		only, resolution, seconds, resolution))
	if err != nil {
		return nil, err
	}

	uptimeBy, p95By := latestByCheck(uptime), latestByCheck(p95)
	out := make([]map[string]any, 0, len(monitors))
	for _, m := range monitors {
		status, locations := m.state(up, warn)
		entry := map[string]any{
			"id": m.ID, "name": m.Name, "type": m.Type, "target": m.target(), "status": status,
			"locations": locations, "uptimePercent": "no data", "p95Ms": "no data",
			"destination": map[string]string{"siteId": site, "projectId": projectID},
		}
		if value, ok := uptimeBy[m.ID]; ok {
			entry["uptimePercent"] = value * 100
		}
		if value, ok := p95By[m.ID]; ok {
			entry["p95Ms"] = value * 1000
		}
		out = append(out, entry)
	}
	return out, nil
}

// state is the monitor's status from its latest run per location, as status.ts monitorState:
// down once downLocations (else half, at least one) fail, warn when fewer fail or a run warned.
func (m monitor) state(up, warn []metrics_querying.PromQLSeriesDTO) (string, []locationState) {
	if !m.Enabled {
		return "off", []locationState{}
	}
	latest := func(series []metrics_querying.PromQLSeriesDTO, pick func(a, b float64) float64) map[string]float64 {
		values := map[string]float64{}
		for _, item := range series {
			if item.Labels["check_id"] != m.ID || len(item.Points) == 0 {
				continue
			}
			value := item.Points[len(item.Points)-1].Value
			location := item.Labels["location"]
			if current, ok := values[location]; ok {
				value = pick(current, value)
			}
			values[location] = value
		}
		return values
	}
	ups := latest(up, func(a, b float64) float64 { return min(a, b) })
	warns := latest(warn, func(a, b float64) float64 { return max(a, b) })
	locations := make([]locationState, 0, len(ups))
	for location, value := range ups {
		if len(m.Locations) > 0 && !slices.Contains(m.Locations, location) {
			continue
		}
		locations = append(locations, locationState{Location: location, Up: value >= 1, Warn: warns[location] >= 1})
	}
	slices.SortFunc(locations, func(a, b locationState) int { return strings.Compare(a.Location, b.Location) })
	if len(locations) == 0 {
		return "no data", locations
	}
	failing, warned := 0, false
	for _, location := range locations {
		if !location.Up {
			failing++
		}
		warned = warned || location.Warn
	}
	threshold := m.DownLocations
	if threshold <= 0 {
		threshold = max(1, (len(locations)+1)/2)
	}
	switch {
	case failing >= threshold:
		return "down", locations
	case failing > 0 || warned:
		return "warn", locations
	default:
		return "up", locations
	}
}

func (m monitor) target() string {
	switch {
	case m.Definition.Request != nil && m.Definition.Request.URL != "":
		return m.Definition.Request.URL
	case m.Definition.Target != "":
		return m.Definition.Target
	case len(m.Definition.Steps) > 0:
		return m.Definition.Steps[0].Request.URL
	default:
		return ""
	}
}

func sendsSignal(source map[string]any, signal string) bool {
	signals, _ := source["signals"].([]any)
	for _, item := range signals {
		if entry, ok := item.(map[string]any); ok && entry["signal"] == signal {
			return true
		}
	}
	return false
}

func latestByCheck(series []metrics_querying.PromQLSeriesDTO) map[string]float64 {
	values := map[string]float64{}
	for _, item := range series {
		if len(item.Points) > 0 {
			values[item.Labels["check_id"]] = item.Points[len(item.Points)-1].Value
		}
	}
	return values
}

func statusRank(status any) int {
	switch status {
	case "down":
		return 0
	case "warn":
		return 1
	case "up":
		return 2
	case "no data":
		return 3
	default:
		return 4
	}
}

func noMonitors(err error) (any, error) {
	if err != nil {
		return nil, err
	}
	return map[string]any{"monitors": []any{}, "returned": 0, "total": 0,
		"note": "synthetic monitors are managed on a multi-site control plane - this server has none"}, nil
}

// compactAsset keeps what an agent needs to triage an asset; get_asset adds its attributes.
func compactAsset(asset map[string]any) map[string]any {
	out := make(map[string]any, 14)
	for _, key := range []string{"id", "projectId", "projectName", "kind", "subtype", "name", "cluster",
		"namespace", "node", "state", "health", "reasons", "metrics", "sources", "firstSeen", "lastSeen"} {
		if value, ok := asset[key]; ok && value != nil && value != "" {
			out[key] = value
		}
	}
	return out
}

// topOf is the first n items of a JSON list (anything else is returned as is).
func topOf(value any, n int) any {
	switch list := value.(type) {
	case []any:
		return list[:min(len(list), n)]
	case []map[string]any:
		return list[:min(len(list), n)]
	default:
		return value
	}
}
