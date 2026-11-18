package mcp_server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"time"

	logs_core "obzly/internal/features/logs/core"
	spans_core "obzly/internal/features/spans/core"
	users_models "obzly/internal/features/users/models"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Insight tools: the summaries behind the Logs, Traces, Incidents and Dashboards pages, so an
// agent gets an overview without scanning raw logs or spans.

const (
	// maxInsightWindow is the Logs insight endpoints' own range limit.
	maxInsightWindow = 31 * 24 * time.Hour
	// maxTraceSummaryWindow: the summary scans spans twice (the window and the one before it).
	maxTraceSummaryWindow = 7 * 24 * time.Hour
	maxSampleChars        = 300
	maxPanelBuckets       = 500
)

type traceSummaryArgs struct {
	projectArgs
	windowArgs
	Service       string   `json:"service,omitempty"       jsonschema:"Only this service's spans (default: root spans of every service)"`
	Operation     string   `json:"operation,omitempty"     jsonschema:"Only this operation (span name)"`
	MinDurationMs *float64 `json:"minDurationMs,omitempty"`
	MaxDurationMs *float64 `json:"maxDurationMs,omitempty"`
	ErrorsOnly    bool     `json:"errorsOnly,omitempty"`
}

type serviceErrorsArgs struct {
	projectArgs
	windowArgs
	Limit int `json:"limit,omitempty" jsonschema:"Max services, most errors first (default 10, max 50)"`
}

type incidentImpactArgs struct {
	projectArgs
	SessionID string `json:"sessionId" jsonschema:"Correlated session id from list_correlation_sessions"`
}

type patternTrendsArgs struct {
	projectArgs
	windowArgs
	Query         string `json:"query,omitempty"         jsonschema:"Optional search_logs syntax filter"`
	Limit         int    `json:"limit,omitempty"         jsonschema:"Biggest patterns listed (default 30, max 50); new, spiking and dropping ones are listed too"`
	IncludeSeries bool   `json:"includeSeries,omitempty" jsonschema:"Add each pattern's lines per bucket (bigger result)"`
}

type explainLogsArgs struct {
	projectArgs
	windowArgs
	Query        string `json:"query,omitempty"        jsonschema:"Optional search_logs syntax filter, e.g. level=error"`
	Baseline     string `json:"baseline,omitempty"     jsonschema:"previous (the same-length window just before, default) or day (the same window 24 h earlier)"`
	BaselineFrom string `json:"baselineFrom,omitempty" jsonschema:"Explicit baseline start, RFC3339 (overrides baseline)"`
	BaselineTo   string `json:"baselineTo,omitempty"   jsonschema:"Explicit baseline end, RFC3339"`
	MinCount     int64  `json:"minCount,omitempty"     jsonschema:"Fewest lines in the window for a value to be listed (default 20)"`
	Limit        int    `json:"limit,omitempty"        jsonschema:"Max values, largest share gain first (default 20, max 100)"`
}

type logVolumeArgs struct {
	projectArgs
	Days int `json:"days,omitempty" jsonschema:"UTC days back, today included (default 14, max 31)"`
}

type healthMapArgs struct {
	projectArgs
	windowArgs
	GroupBy string `json:"groupBy,omitempty" jsonschema:"service (default), k8s (namespace and workload) or host (host and service; reads raw logs, slower)"`
	Query   string `json:"query,omitempty"   jsonschema:"Optional search_logs syntax filter"`
}

type dashboardPanelArgs struct {
	projectArgs
	windowArgs
	DashboardID string            `json:"dashboardId"         jsonschema:"Dashboard id from list_dashboards"`
	PanelID     string            `json:"panelId,omitempty"   jsonschema:"Panel id from get_dashboard; omit to list the dashboard's panels"`
	Variables   map[string]string `json:"variables,omitempty" jsonschema:"Dashboard variable values by key; unset ones use their default (PromQL treats unset as all)"`
}

func init() {
	allowSiteCalls(http.MethodGet, "/correlation/sessions/{project}/{session}/impact")
	allowSiteCalls(http.MethodPost,
		"/traces/query/summary/{project}", "/traces/query/service-errors/{project}",
		"/logs/query/pattern-trends/{project}", "/logs/query/explain/{project}", "/logs/query/volume/{project}",
		"/logs/query/health-map/{project}",
		"/projects/dashboards/{project}/query",
	)
}

func (t *toolSet) registerInsights(server *mcp.Server, add func(name, description string) *mcp.Tool) {
	mcp.AddTool(server, add("trace_summary",
		"Request count, error rate, p50 and p95 for a window against the same-length window before it, with "+
			"bucketed trends and a capped count of matching traces. Root spans unless a service is picked. "+
			"Window default 1h, at most 7 days. Use it before list_traces to see whether latency or errors moved."),
		wrap("trace_summary", t.traceSummary))
	mcp.AddTool(server, add("service_errors",
		"Services with the most failed entry spans (SERVER/CONSUMER and trace roots) in a window, with the "+
			"previous window's counts. Window default 1h; over 7 days is clamped to the last 7. Use it to find "+
			"which service is failing."+untrustedNote),
		wrap("service_errors", untrusted(t.serviceErrors)))
	mcp.AddTool(server, add("incident_impact",
		"For one correlated session (incident), error logs and error spans per service inside its window, most "+
			"first. Use it after list_correlation_sessions/get_correlation_session to see the blast radius."+untrustedNote),
		wrap("incident_impact", untrusted(t.incidentImpact)))
	mcp.AddTool(server, add("log_pattern_trends",
		"Message patterns over a window from per-minute counts: the biggest plus every new, spiking or dropping "+
			"one, with lines 24 h earlier and in the 7 days before. Window default 1h, at most 31 days. Cheaper "+
			"than log_patterns for long windows; use it to spot what changed."+untrustedNote),
		wrap("log_pattern_trends", untrusted(t.logPatternTrends)))
	mcp.AddTool(server, add("explain_logs",
		"Which field values (service, namespace, container, pod, ReplicaSet, level) grew most in share among "+
			"matching logs from a baseline to the window, from per-minute counts. Window default 1h, each range "+
			"at most 31 days. Use it to explain a spike, e.g. query level=error."+untrustedNote),
		wrap("explain_logs", untrusted(t.explainLogs)))
	mcp.AddTool(server, add("log_volume",
		"Log volume: lines per UTC day by service (default 14 days, max 31), lines per level over 7 days, "+
			"today against yesterday so far, bytes per line, and the noisiest patterns per service and level. "+
			"Use it for cost and noise questions."+untrustedNote),
		wrap("log_volume", untrusted(t.logVolume)))
	mcp.AddTool(server, add("log_health_map",
		"Lines, error and warning lines per service (or Kubernetes workload, or host) over a window with each "+
			"tile's lines 24 h earlier; at most 150 tiles, biggest first. Window default 1h, at most 31 days. "+
			"Use it to see which workloads are erroring."+untrustedNote),
		wrap("log_health_map", untrusted(t.logHealthMap)))
	mcp.AddTool(server, add("query_dashboard_panel",
		"Run one saved dashboard panel (logs, spans or metrics) over a window and return its data points "+
			"(at most 500); a logs_sql panel returns columns and rows (at most 200). Without panelId it lists "+
			"the dashboard's panels. One panel per call; window default "+
			"1h, at most 31 days."+untrustedNote),
		wrap("query_dashboard_panel", untrusted(t.queryDashboardPanel)))
}

func (t *toolSet) traceSummary(ctx context.Context, user *users_models.User, args traceSummaryArgs) (any, error) {
	projectID, err := t.resolveProject(ctx, user, args.projectArgs)
	if err != nil {
		return nil, err
	}
	window, err := args.windowWithin(maxTraceSummaryWindow)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"timeRange":   spans_core.TimeRangeDTO{From: window.From, To: window.To},
		"serviceName": args.Service, "operationName": args.Operation, "minDurationMs": args.MinDurationMs,
		"maxDurationMs": args.MaxDurationMs, "errorsOnly": args.ErrorsOnly,
	}
	var response map[string]any
	if err := t.exec.siteCall(ctx, user, args.Site, http.MethodPost,
		"/traces/query/summary/"+projectID, nil, body, &response); err != nil {
		return nil, err
	}
	response["window"] = windowSummary(window)
	return response, nil
}

func (t *toolSet) serviceErrors(ctx context.Context, user *users_models.User, args serviceErrorsArgs) (any, error) {
	projectID, err := t.resolveProject(ctx, user, args.projectArgs)
	if err != nil {
		return nil, err
	}
	// The endpoint clamps longer ranges itself and says so.
	window, err := args.windowWithin(0)
	if err != nil {
		return nil, err
	}
	limit := clampLimit(args.Limit, 10, 50)
	body := map[string]any{"timeRange": spans_core.TimeRangeDTO{From: window.From, To: window.To}, "limit": limit}
	var response struct {
		From         time.Time        `json:"from"`
		To           time.Time        `json:"to"`
		PreviousFrom time.Time        `json:"previousFrom"`
		Clamped      bool             `json:"clamped"`
		Services     []map[string]any `json:"services"`
	}
	if err := t.exec.siteCall(ctx, user, args.Site, http.MethodPost,
		"/traces/query/service-errors/"+projectID, nil, body, &response); err != nil {
		return nil, err
	}
	return listResult("services", response.Services, len(response.Services), limit, map[string]any{
		"from": response.From, "to": response.To, "previousFrom": response.PreviousFrom,
		"clampedTo7Days": response.Clamped,
	}), nil
}

func (t *toolSet) incidentImpact(ctx context.Context, user *users_models.User, args incidentImpactArgs) (any, error) {
	projectID, err := t.resolveProject(ctx, user, args.projectArgs)
	if err != nil {
		return nil, err
	}
	if _, err := uuid.Parse(strings.TrimSpace(args.SessionID)); err != nil {
		return nil, errors.New("sessionId must be a session id from list_correlation_sessions")
	}
	var response struct {
		SessionID   string           `json:"sessionId"`
		WindowStart time.Time        `json:"windowStart"`
		WindowEnd   time.Time        `json:"windowEnd"`
		Services    []map[string]any `json:"services"`
	}
	if err := t.exec.siteCall(ctx, user, args.Site, http.MethodGet,
		"/correlation/sessions/"+projectID+"/"+strings.TrimSpace(args.SessionID)+"/impact",
		nil, nil, &response); err != nil {
		return nil, err
	}
	total := len(response.Services)
	services := response.Services[:min(total, maxRows)]
	return listResult("services", services, total, maxRows, map[string]any{
		"sessionId": response.SessionID, "windowStart": response.WindowStart, "windowEnd": response.WindowEnd,
	}), nil
}

func (t *toolSet) logPatternTrends(ctx context.Context, user *users_models.User, args patternTrendsArgs) (any, error) {
	projectID, err := t.resolveProject(ctx, user, args.projectArgs)
	if err != nil {
		return nil, err
	}
	window, err := args.windowWithin(maxInsightWindow)
	if err != nil {
		return nil, err
	}
	query, err := logs_core.ParseSearchQuery(args.Query)
	if err != nil {
		return nil, fmt.Errorf("invalid query: %w", err)
	}
	limit := clampLimit(args.Limit, 30, 50)
	request := logs_core.PatternTrendsRequestDTO{Query: query, TimeRange: window, Limit: limit}
	var response logs_core.PatternTrendsResponseDTO
	if err := t.exec.siteCall(ctx, user, args.Site, http.MethodPost,
		"/logs/query/pattern-trends/"+projectID, nil, request, &response); err != nil {
		return nil, err
	}

	patterns := make([]map[string]any, 0, len(response.Patterns))
	for _, p := range response.Patterns {
		entry := map[string]any{
			"hash": p.Hash, "pattern": short(p.Pattern, maxFieldChars*2), "sample": short(p.Sample, maxSampleChars),
			"level": p.Level, "services": p.Services, "count": p.Count, "yesterday": p.Yesterday,
			"previousWeek": p.PreviousWeek, "change": p.Change,
		}
		if args.IncludeSeries {
			entry["series"] = p.Series
		}
		patterns = append(patterns, entry)
	}
	extra := map[string]any{
		"window": windowSummary(window), "totalLines": response.Total, "distinctPatterns": response.Distinct,
		"newCount": response.NewCount, "spikeCount": response.SpikeCount, "dropCount": response.DropCount,
		"partial": response.Partial, "filterHint": "filter on a pattern with pattern_hash=<hash> in search_logs",
	}
	if args.IncludeSeries {
		extra["bucketMs"], extra["buckets"], extra["other"] = response.BucketMs, response.Buckets, response.Other
	}
	return listResult("patterns", patterns, response.Distinct, len(patterns), extra), nil
}

func (t *toolSet) explainLogs(ctx context.Context, user *users_models.User, args explainLogsArgs) (any, error) {
	projectID, err := t.resolveProject(ctx, user, args.projectArgs)
	if err != nil {
		return nil, err
	}
	current, err := args.windowWithin(maxInsightWindow)
	if err != nil {
		return nil, err
	}
	baseline, err := explainBaseline(args, current)
	if err != nil {
		return nil, err
	}
	query, err := logs_core.ParseSearchQuery(args.Query)
	if err != nil {
		return nil, fmt.Errorf("invalid query: %w", err)
	}
	request := logs_core.ExplainRequestDTO{Query: query, Current: current, Baseline: baseline, MinCount: args.MinCount}
	var response logs_core.ExplainResponseDTO
	if err := t.exec.siteCall(ctx, user, args.Site, http.MethodPost,
		"/logs/query/explain/"+projectID, nil, request, &response); err != nil {
		return nil, err
	}
	limit := clampLimit(args.Limit, 20, 100)
	fields := response.Fields[:min(len(response.Fields), limit)]
	return listResult("fields", fields, len(response.Fields), limit, map[string]any{
		"window": windowSummary(current), "baseline": windowSummary(baseline),
		"currentTotal": response.CurrentTotal, "baselineTotal": response.BaselineTotal, "partial": response.Partial,
	}), nil
}

func (t *toolSet) logVolume(ctx context.Context, user *users_models.User, args logVolumeArgs) (any, error) {
	projectID, err := t.resolveProject(ctx, user, args.projectArgs)
	if err != nil {
		return nil, err
	}
	request := logs_core.VolumeRequestDTO{Days: clampLimit(args.Days, 14, 31)}
	var response logs_core.VolumeResponseDTO
	if err := t.exec.siteCall(ctx, user, args.Site, http.MethodPost,
		"/logs/query/volume/"+projectID, nil, request, &response); err != nil {
		return nil, err
	}
	days := make([]string, 0, len(response.Days))
	for _, day := range response.Days {
		days = append(days, day.Format(time.DateOnly))
	}
	noisy := make([]map[string]any, 0, len(response.Noisy))
	for _, p := range response.Noisy {
		noisy = append(noisy, map[string]any{
			"hash": p.Hash, "pattern": short(p.Pattern, maxFieldChars*2), "sample": short(p.Sample, maxSampleChars),
			"service": p.Service, "level": p.Level, "count7d": p.Count, "perDay": p.PerDay,
		})
	}
	return listResult("services", response.Services, len(response.Services), len(response.Services),
		map[string]any{
			"days": days, "levels7d": response.Levels, "today": response.Today,
			"yesterdaySoFar": response.YesterdaySoFar, "bytesPerLine": response.BytesPerLine,
			"rawBytesPerLine": response.RawBytesPerLine, "storedBytes": response.StoredBytes,
			"noisyPatterns": noisy, "partial": response.Partial,
		}), nil
}

func (t *toolSet) logHealthMap(ctx context.Context, user *users_models.User, args healthMapArgs) (any, error) {
	projectID, err := t.resolveProject(ctx, user, args.projectArgs)
	if err != nil {
		return nil, err
	}
	window, err := args.windowWithin(maxInsightWindow)
	if err != nil {
		return nil, err
	}
	query, err := logs_core.ParseSearchQuery(args.Query)
	if err != nil {
		return nil, fmt.Errorf("invalid query: %w", err)
	}
	groupBy := strings.ToLower(orDefault(args.GroupBy, logs_core.HealthMapByService))
	request := logs_core.HealthMapRequestDTO{Query: query, TimeRange: window, GroupBy: groupBy}
	var response logs_core.HealthMapResponseDTO
	if err := t.exec.siteCall(ctx, user, args.Site, http.MethodPost,
		"/logs/query/health-map/"+projectID, nil, request, &response); err != nil {
		return nil, err
	}
	return listResult("groups", response.Groups, len(response.Groups), len(response.Groups), map[string]any{
		"window": windowSummary(window), "groupBy": response.GroupBy, "hiddenTiles": response.HiddenTiles,
		"partial": response.Partial,
	}), nil
}

func (t *toolSet) queryDashboardPanel(
	ctx context.Context, user *users_models.User, args dashboardPanelArgs,
) (any, error) {
	projectID, err := t.resolveProject(ctx, user, args.projectArgs)
	if err != nil {
		return nil, err
	}
	if _, err := uuid.Parse(args.DashboardID); err != nil {
		return nil, errors.New("dashboardId must be a dashboard id from list_dashboards")
	}
	var dashboard struct {
		Title string `json:"title"`
		Tabs  []struct {
			Title  string           `json:"title"`
			Panels []map[string]any `json:"panels"`
		} `json:"tabs"`
		Variables []struct {
			Key          string `json:"key"`
			DefaultValue string `json:"defaultValue"`
		} `json:"variables"`
	}
	if err := t.exec.siteCall(ctx, user, args.Site, http.MethodGet,
		"/projects/dashboards/"+projectID+"/"+args.DashboardID, nil, nil, &dashboard); err != nil {
		return nil, err
	}

	var panel map[string]any
	var panels []map[string]any
	for _, tab := range dashboard.Tabs {
		for _, p := range tab.Panels {
			if args.PanelID != "" && p["id"] == args.PanelID {
				panel = p
			}
			panels = append(panels, map[string]any{
				"id":     p["id"],
				"title":  p["title"],
				"type":   p["type"],
				"source": orDefault(asString(p["source"]), "logs"),
				"tab":    tab.Title,
			})
		}
	}
	if panel == nil {
		result := listResult("panels", panels[:min(len(panels), maxRows)], len(panels), maxRows, map[string]any{
			"dashboard": dashboard.Title, "hint": "call again with panelId to run one",
		})
		if args.PanelID != "" {
			result["note"] = fmt.Sprintf("no panel %q on this dashboard", args.PanelID)
		}
		return result, nil
	}

	window, err := args.windowWithin(maxInsightWindow)
	if err != nil {
		return nil, err
	}
	values := map[string]string{}
	for _, variable := range dashboard.Variables {
		if variable.DefaultValue != "" {
			values[variable.Key] = variable.DefaultValue
		}
	}
	for key, value := range args.Variables {
		values[key] = value
	}
	applyPanelVariables(panel, values)

	var response struct {
		Buckets []map[string]any `json:"buckets"`
		// Table: a logs_sql panel's result.
		Table *struct {
			Columns []string `json:"columns"`
			Rows    [][]any  `json:"rows"`
		} `json:"table"`
	}
	if err := t.exec.siteCall(ctx, user, args.Site, http.MethodPost, "/projects/dashboards/"+projectID+"/query",
		nil, map[string]any{"panel": panel, "timeRange": window}, &response); err != nil {
		return nil, err
	}
	extra := map[string]any{
		"window": windowSummary(window), "dashboard": dashboard.Title,
		"panel": map[string]any{"id": panel["id"], "title": panel["title"], "type": panel["type"],
			"source": orDefault(asString(panel["source"]), "logs"), "unit": panel["unit"]},
	}
	if response.Table != nil {
		extra["columns"] = response.Table.Columns
		rows := response.Table.Rows[:min(len(response.Table.Rows), maxRows)]
		return listResult("rows", rows, len(response.Table.Rows), maxRows, extra), nil
	}
	buckets := response.Buckets[:min(len(response.Buckets), maxPanelBuckets)]
	return listResult("buckets", buckets, len(response.Buckets), maxPanelBuckets, extra), nil
}

// variableToken is a dashboard variable reference, ${key}, substituted like the frontend does
// (features/dashboards/lib/dashboardVariables.ts) since the backend stores the tokens untouched.
var variableToken = regexp.MustCompile(`\$\{(\w+)\}`)

func applyPanelVariables(panel map[string]any, values map[string]string) {
	substitute := func(text string) string {
		return variableToken.ReplaceAllStringFunc(text, func(token string) string {
			if value, ok := values[token[2:len(token)-1]]; ok {
				return value
			}
			return token
		})
	}
	if promQuery, ok := panel["promQuery"].(string); ok && promQuery != "" {
		// An unset variable means "all" in PromQL, as in the UI.
		panel["promQuery"] = variableToken.ReplaceAllString(substitute(promQuery), ".*")
	}
	for _, key := range []string{"serviceName", "operationName"} {
		if text, ok := panel[key].(string); ok {
			panel[key] = substitute(text)
		}
	}
	var walk func(node any)
	walk = func(node any) {
		object, ok := node.(map[string]any)
		if !ok {
			return
		}
		if condition, ok := object["condition"].(map[string]any); ok {
			switch value := condition["value"].(type) {
			case string:
				condition["value"] = substitute(value)
			case []any:
				for i, item := range value {
					if text, ok := item.(string); ok {
						value[i] = substitute(text)
					}
				}
			}
		}
		if logic, ok := object["logic"].(map[string]any); ok {
			if children, ok := logic["children"].([]any); ok {
				for _, child := range children {
					walk(child)
				}
			}
		}
	}
	walk(panel["query"])
}

func explainBaseline(args explainLogsArgs, current *logs_core.TimeRangeDTO) (*logs_core.TimeRangeDTO, error) {
	if args.BaselineFrom != "" || args.BaselineTo != "" {
		if args.BaselineFrom == "" || args.BaselineTo == "" {
			return nil, errors.New("baselineFrom and baselineTo go together")
		}
		return windowArgs{From: args.BaselineFrom, To: args.BaselineTo}.windowWithin(maxInsightWindow)
	}
	length := current.To.Sub(*current.From)
	var from, to time.Time
	switch strings.ToLower(orDefault(args.Baseline, "previous")) {
	case "previous":
		from, to = current.From.Add(-length), *current.From
	case "day":
		from, to = current.From.Add(-24*time.Hour), current.To.Add(-24*time.Hour)
	default:
		return nil, errors.New("baseline must be previous or day")
	}
	return &logs_core.TimeRangeDTO{From: &from, To: &to}, nil
}

// withDefault is the window with defaultWindow when none is given (windowWithin defaults to 1h).
func withDefault(args windowArgs, defaultWindow time.Duration) windowArgs {
	if args.Since == "" && strings.TrimSpace(args.From) == "" {
		args.Since = defaultWindow.String()
	}
	return args
}

// listResult is the shape every list tool returns: the list under key, how many were returned
// out of total, and a truncated note when the limit cut it.
func listResult(key string, list any, total, limit int, extra map[string]any) map[string]any {
	returned := reflect.ValueOf(list).Len()
	result := make(map[string]any, len(extra)+4)
	for k, v := range extra {
		result[k] = v
	}
	result[key], result["returned"], result["total"] = list, returned, total
	if total > returned {
		result["truncated"] = true
		result["note"] = fmt.Sprintf(
			"returned %d of %d %s (limit %d) - narrow the filters",
			returned,
			total,
			key,
			limit,
		)
	}
	return result
}

func clampLimit(requested, fallback, maximum int) int {
	if requested <= 0 {
		return fallback
	}
	return min(requested, maximum)
}

func short(text string, maxBytes int) string {
	cut, _ := truncateUTF8(text, maxBytes)
	return cut
}

func asString(value any) string {
	text, _ := value.(string)
	return text
}

// compareNumber orders two JSON numbers (float64 after decoding); anything else counts as 0.
func compareNumber(a, b any) int {
	x, _ := a.(float64)
	y, _ := b.(float64)
	switch {
	case x < y:
		return -1
	case x > y:
		return 1
	default:
		return 0
	}
}

// queryValues drops empty values.
func queryValues(pairs map[string]string) url.Values {
	query := url.Values{}
	for name, value := range pairs {
		if strings.TrimSpace(value) != "" {
			query.Set(name, value)
		}
	}
	return query
}
