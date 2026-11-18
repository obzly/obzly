package mcp_server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"
	users_models "obzly/internal/features/users/models"
	"obzly/internal/util/rate_limit"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fitted is the metadata fitResult adds to a trimmed result.
type fitted struct {
	Truncated    bool       `json:"truncated"`
	TrimmedLists []listTrim `json:"trimmedLists"`
	StringsCutTo int        `json:"stringsCutTo"`
	Note         string     `json:"note"`
	PartialJSON  any        `json:"partialJson"`
}

type allowAllLimiter struct{ ids []uuid.UUID }

func Test_EncodeResult_OversizedList_KeepsWholeItemsAndValidJSON(t *testing.T) {
	logs := make([]map[string]any, 100)
	for i := range logs {
		logs[i] = map[string]any{"message": strings.Repeat("x", 1500), "i": i}
	}
	text := encodeResult(map[string]any{"totalMatches": 5000, "returned": 100, "logs": logs})

	assert.LessOrEqual(t, len(text), maxResultBytes)
	var decoded struct {
		Returned  int              `json:"returned"`
		Truncated bool             `json:"truncated"`
		Note      string           `json:"note"`
		Logs      []map[string]any `json:"logs"`
	}
	require.NoError(t, json.Unmarshal([]byte(text), &decoded))
	assert.True(t, decoded.Truncated)
	assert.Greater(t, decoded.Returned, 10)
	assert.Less(t, decoded.Returned, 100)
	assert.Len(t, decoded.Logs, decoded.Returned)
	assert.EqualValues(t, 0, decoded.Logs[0]["i"], "the newest (first) items are kept")
	assert.Contains(t, decoded.Note, "of 100 logs")
}

func Test_EncodeResult_OversizedNonList_StaysUnderCapAsStructuredJSON(t *testing.T) {
	text := encodeResult(map[string]any{
		"spans":   strings.Repeat("y", maxResultBytes*2),
		"details": map[string]any{"a": strings.Repeat("é", maxResultBytes), "b": 1},
	})

	assert.LessOrEqual(t, len(text), maxResultBytes)
	var decoded struct {
		fitted
		Spans   string         `json:"spans"`
		Details map[string]any `json:"details"`
	}
	require.NoError(t, json.Unmarshal([]byte(text), &decoded))
	assert.True(t, decoded.Truncated)
	assert.Nil(t, decoded.PartialJSON, "never a string blob")
	assert.Positive(t, decoded.StringsCutTo)
	assert.True(t, strings.HasPrefix(decoded.Spans, "yyy"))
	assert.EqualValues(t, 1, decoded.Details["b"], "the shape is kept")
	assert.Contains(t, decoded.Note, "cut strings longer than")
}

// query_metrics broke at ~20 series of 60 points: whole series must be kept.
func Test_EncodeResult_ManyMetricSeries_KeepsWholeSeriesUnderCap(t *testing.T) {
	series := make([]map[string]any, 100)
	for i := range series {
		points := make([][2]any, 60)
		for j := range points {
			points[j] = [2]any{1_790_000_000 + j*60, 12345.678 + float64(j)}
		}
		series[i] = map[string]any{
			"labels": map[string]string{"__name__": "http_requests_total", "pod": fmt.Sprintf("api-%03d", i)},
			"points": points,
		}
	}
	text := encodeResult(map[string]any{"returned": 100, "series": series, "resultType": "matrix"})

	assert.LessOrEqual(t, len(text), maxResultBytes)
	var decoded struct {
		fitted
		Returned int `json:"returned"`
		Series   []struct {
			Points [][]float64 `json:"points"`
		} `json:"series"`
	}
	require.NoError(t, json.Unmarshal([]byte(text), &decoded))
	assert.Greater(t, decoded.Returned, 20)
	assert.Less(t, decoded.Returned, 100)
	assert.Len(t, decoded.Series, decoded.Returned)
	for _, s := range decoded.Series {
		assert.Len(t, s.Points, 60, "series are cut whole, never their points")
	}
	assert.Equal(t, []listTrim{{Path: "series", Returned: decoded.Returned, Total: 100}}, decoded.TrimmedLists)
}

func Test_EncodeResult_SiblingLists_AreCutByTheSameFraction(t *testing.T) {
	nodes := make([]map[string]any, 400)
	for i := range nodes {
		nodes[i] = map[string]any{"name": fmt.Sprintf("service-%04d", i), "requests": i}
	}
	edges := make([]map[string]any, 2000)
	for i := range edges {
		edges[i] = map[string]any{"source": fmt.Sprintf("service-%04d", i%400), "target": "db", "p95": 1.5}
	}
	text := encodeResult(map[string]any{"result": map[string]any{"nodes": nodes, "edges": edges}})

	assert.LessOrEqual(t, len(text), maxResultBytes)
	var decoded fitted
	require.NoError(t, json.Unmarshal([]byte(text), &decoded))
	require.Len(t, decoded.TrimmedLists, 2)
	assert.Equal(t, "result.edges", decoded.TrimmedLists[0].Path)
	assert.Equal(t, "result.nodes", decoded.TrimmedLists[1].Path)
	assert.Greater(t, decoded.TrimmedLists[1].Returned, 1, "both lists keep a share")
	assert.InDelta(t, float64(decoded.TrimmedLists[0].Returned)/2000,
		float64(decoded.TrimmedLists[1].Returned)/400, 0.01)
}

func Test_EncodeResult_OneHugeItem_CutsTheListsInsideIt(t *testing.T) {
	events := make([]map[string]any, 3000)
	for i := range events {
		events[i] = map[string]any{"name": "retry", "attempt": i, "detail": strings.Repeat("d", 40)}
	}
	trace := map[string]any{"traceId": "abc", "spans": []any{
		map[string]any{"spanId": "s1", "events": events},
		map[string]any{"spanId": "s2", "events": events},
	}}

	text := encodeResult(trace)

	assert.LessOrEqual(t, len(text), maxResultBytes)
	var decoded struct {
		fitted
		Spans []struct {
			SpanID string `json:"spanId"`
			Events []any  `json:"events"`
		} `json:"spans"`
	}
	require.NoError(t, json.Unmarshal([]byte(text), &decoded))
	require.Len(t, decoded.Spans, 1)
	assert.Equal(t, "s1", decoded.Spans[0].SpanID)
	assert.NotEmpty(t, decoded.Spans[0].Events)
	assert.Less(t, len(decoded.Spans[0].Events), 3000)
	paths := []string{}
	for _, trim := range decoded.TrimmedLists {
		paths = append(paths, trim.Path)
	}
	assert.Equal(t, []string{"spans", "spans[0].events"}, paths)
}

func Test_EncodeResult_LongListOfPlainValues_IsCut(t *testing.T) {
	services := make([]string, 20_000)
	for i := range services {
		services[i] = fmt.Sprintf("service-%05d", i)
	}
	text := encodeResult(map[string]any{"services": services})

	assert.LessOrEqual(t, len(text), maxResultBytes)
	var decoded struct {
		fitted
		Services []string `json:"services"`
	}
	require.NoError(t, json.Unmarshal([]byte(text), &decoded))
	assert.Equal(t, "service-00000", decoded.Services[0])
	require.Len(t, decoded.TrimmedLists, 1)
	assert.Len(t, decoded.Services, decoded.TrimmedLists[0].Returned)
}

func Test_EncodeResult_TopLevelArray_IsWrapped(t *testing.T) {
	items := make([]map[string]any, 2000)
	for i := range items {
		items[i] = map[string]any{"id": i, "name": strings.Repeat("n", 50)}
	}
	text := encodeResult(items)

	assert.LessOrEqual(t, len(text), maxResultBytes)
	var decoded struct {
		fitted
		Result []map[string]any `json:"result"`
	}
	require.NoError(t, json.Unmarshal([]byte(text), &decoded))
	assert.NotEmpty(t, decoded.Result)
	assert.Equal(t, "result", decoded.TrimmedLists[0].Path)
}

func Test_EncodeResult_SmallResult_Unchanged(t *testing.T) {
	assert.JSONEq(t, `{"a":1}`, encodeResult(map[string]any{"a": 1}))
}

func Test_SelectFields(t *testing.T) {
	fields := map[string]any{"service": "api", "pod": "p-1", "big": strings.Repeat("z", 1000)}

	all := selectFields(fields, nil)
	assert.Len(t, all, 3)
	assert.Len(t, all["big"], maxFieldChars+len("…"))

	assert.Equal(t, map[string]any{"service": "api"}, selectFields(fields, []string{"service"}))
	assert.Nil(t, selectFields(fields, []string{"-"}))
}

func Test_TruncateUTF8_CutsOnARuneBoundary(t *testing.T) {
	cut, truncated := truncateUTF8("ab€€", 4) // € is 3 bytes

	assert.True(t, truncated)
	assert.Equal(t, "ab…", cut)
	unchanged, truncated := truncateUTF8("abc", 3)
	assert.False(t, truncated)
	assert.Equal(t, "abc", unchanged)
}

func Test_LogEntry_LongMultibyteMessage_IsCutToValidUTF8(t *testing.T) {
	truncated := 0
	entry := logEntry(logs_core.LogItemDTO{Message: strings.Repeat("日本", maxMessageChars)}, nil, &truncated)

	message := entry["message"].(string)
	assert.Equal(t, 1, truncated)
	assert.LessOrEqual(t, len(message), maxMessageChars+len("…"))
	assert.True(t, strings.HasSuffix(message, "…"))
	encoded, err := json.Marshal(entry)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), `�`)
}

func Test_Window_AboveTheCap_IsRefused(t *testing.T) {
	_, err := windowArgs{Since: "3650d"}.window()
	assert.ErrorContains(t, err, "at most 31 days")

	_, err = windowArgs{Since: "31d"}.window()
	assert.NoError(t, err)
	_, err = windowArgs{From: "2026-01-01", To: "2026-03-01"}.windowWithin(maxMetricWindow)
	assert.NoError(t, err)
	_, err = windowArgs{From: "2026-01-01", To: "2026-03-01"}.windowWithin(maxTraceWindow)
	assert.ErrorContains(t, err, "at most 31 days")
}

func Test_Window_AcceptsDatesAndRFC3339(t *testing.T) {
	w, err := windowArgs{From: "2026-09-24", To: "2026-09-24"}.window()
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC), *w.From)
	assert.Equal(t, time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC), *w.To, "a date as to covers that day")

	w, err = windowArgs{From: "2026-09-24T06:00:00.5+02:00", To: "2026-09-24T06:00:00Z"}.window()
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 9, 24, 4, 0, 0, 500_000_000, time.UTC), *w.From)

	_, err = windowArgs{From: "yesterday"}.window()
	assert.ErrorContains(t, err, "RFC3339")

	day, err := dayArg("from", "2026-09-24T23:30:00-02:00")
	require.NoError(t, err)
	assert.Equal(t, "2026-09-25", day)
}

func Test_MarkUntrusted_AddsTheNotice(t *testing.T) {
	marked := markUntrusted(map[string]any{"logs": []any{}})
	assert.Equal(t, untrustedDataNotice, marked[untrustedDataKey])

	wrapped := markUntrusted([]any{"a"})
	assert.Equal(t, []any{"a"}, wrapped["result"])
	assert.Equal(t, untrustedDataNotice, wrapped[untrustedDataKey])
}

func Test_SearchLogs_PartialSearch_ReportsWhatWasNotSearched(t *testing.T) {
	searched := time.Now().UTC().Add(-20 * time.Minute)
	var seen logs_core.LogQueryRequestDTO
	engine := jsonEngine(t, func(r *http.Request) any {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&seen))
		return logs_core.LogQueryResponseDTO{
			Logs:  []logs_core.LogItemDTO{{ID: "1", Message: "boom", Level: "error"}},
			Total: logs_core.TotalNotCounted, CountSkipped: true,
			Partial: true, SearchedThrough: &searched, NextCursor: "next-page",
		}
	})
	tools := &toolSet{exec: &apiExecutor{engine: engine}}

	out, err := tools.searchLogs(context.Background(), &users_models.User{ID: uuid.New()}, searchLogsArgs{
		projectArgs: projectArgs{Project: uuid.NewString()}, Cursor: "previous-page",
	})

	require.NoError(t, err)
	assert.Equal(t, "previous-page", seen.Cursor)
	result := out.(map[string]any)
	assert.Nil(t, result["totalMatches"])
	assert.Equal(t, true, result["partial"])
	assert.Equal(t, searched.Format(time.RFC3339Nano), result["searchedThrough"])
	assert.Equal(t, searched.Format(time.RFC3339Nano), result["unsearched"].(map[string]string)["to"])
	assert.Equal(t, "next-page", result["nextCursor"])
	assert.Contains(t, result["note"], "scan budget")
	assert.Contains(t, result["note"], "partial")
	assert.Contains(t, result["note"], "cursor=nextCursor")
}

func Test_QueryMetrics_PassesMaxSeriesAndCompactsPoints(t *testing.T) {
	var seen map[string]any
	engine := jsonEngine(t, func(r *http.Request) any {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&seen))
		return map[string]any{"resultType": "matrix", "truncated": true, "stepSeconds": 60, "series": []any{
			map[string]any{"labels": map[string]string{"pod": "a"},
				"points": []any{map[string]any{"timestamp": "2026-09-24T06:00:00Z", "value": 1.5}}},
		}}
	})
	tools := &toolSet{exec: &apiExecutor{engine: engine}}
	project := projectArgs{Project: uuid.NewString()}
	user := &users_models.User{ID: uuid.New()}

	out, err := tools.queryMetrics(context.Background(), user, promQLArgs{projectArgs: project, PromQL: "up"})
	require.NoError(t, err)
	assert.EqualValues(t, defaultMetricSeries, seen["maxSeries"])
	encoded := encodeResult(out)
	assert.Contains(t, encoded, `"points":[[1790229600,1.5]]`)
	assert.Contains(t, encoded, `"moreSeries":true`)

	_, err = tools.queryMetrics(context.Background(), user,
		promQLArgs{projectArgs: project, PromQL: "up", MaxSeries: 5000})
	require.NoError(t, err)
	assert.EqualValues(t, maxMetricSeries, seen["maxSeries"])
}

func Test_ToolCall_OversizedNonListResult_IsTrimmedThroughTheHandler(t *testing.T) {
	useLimiter(t, &allowAllLimiter{})
	engine := jsonEngine(t, func(*http.Request) any {
		panels := map[string]any{}
		for i := range 400 {
			panels[fmt.Sprintf("panel-%03d", i)] = map[string]any{"query": strings.Repeat("q", 400)}
		}
		return map[string]any{"name": "Overview", "panels": panels}
	})
	tools := &toolSet{exec: &apiExecutor{engine: engine}}
	handler := wrap("get_dashboard", untrusted(tools.getDashboard))

	result, _, err := handler(context.Background(), toolRequest(uuid.New()), dashboardArgs{
		projectArgs: projectArgs{Project: uuid.NewString()}, DashboardID: uuid.NewString(),
	})

	require.NoError(t, err)
	text := result.Content[0].(*mcp.TextContent).Text
	assert.LessOrEqual(t, len(text), maxResultBytes)
	var decoded struct {
		fitted
		Name          string         `json:"name"`
		Panels        map[string]any `json:"panels"`
		UntrustedData string         `json:"untrustedData"`
	}
	require.NoError(t, json.Unmarshal([]byte(text), &decoded))
	assert.True(t, decoded.Truncated)
	assert.Equal(t, "Overview", decoded.Name)
	assert.Len(t, decoded.Panels, 400)
	assert.Equal(t, untrustedDataNotice, decoded.UntrustedData)
}

func Test_ToolCall_ChecksTheTokenAndTheUserRateLimit(t *testing.T) {
	limiter := &allowAllLimiter{}
	useLimiter(t, limiter)
	user := uuid.New()
	request := toolRequest(user)
	token := request.Extra.TokenInfo.Extra[tokenIDExtraKey].(uuid.UUID)
	handler := wrap("test_tool", func(context.Context, *users_models.User, struct{}) (any, error) {
		return "ok", nil
	})

	_, _, err := handler(context.Background(), request, struct{}{})

	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{token, user}, limiter.ids)
}

// The snapshot of every tool and its annotations: a new tool, a renamed one or
// a changed hint must be a deliberate change here.
func Test_ToolList_MatchesTheSnapshot(t *testing.T) {
	untrustedTools := []string{
		"cost_anomalies", "cost_explore", "cost_recommendations", "cost_summary", "get_correlation_session",
		"get_dashboard", "get_trace", "list_correlation_sessions", "list_dashboards", "list_metrics",
		"list_operations", "list_services", "list_traces", "log_context", "log_fields", "log_patterns",
		"metric_label_values", "posture_findings", "query_logs_sql", "query_metrics", "search_logs", "service_map",
		"list_alerts", "list_alert_rules", "alert_rule_activity", "list_silences",
		"service_errors", "incident_impact", "log_pattern_trends", "explain_logs", "log_volume", "log_health_map",
		"query_dashboard_panel", "ingestion_status", "list_assets", "get_asset", "asset_changes",
		"ai_usage_overview", "ai_budget_status", "list_monitors", "monitor_status", "web_analytics_report",
	}
	trustedTools := []string{
		"cost_budgets", "list_projects", "list_sites", "posture_coverage", "posture_scores",
		"alert_summary", "trace_summary", "inventory_summary",
	}
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	server := newServer(http.NotFoundHandler(), false, "test")
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "snapshot", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })

	tools, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)

	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
		require.NotNil(t, tool.Annotations, tool.Name)
		assert.True(t, tool.Annotations.ReadOnlyHint, tool.Name)
		require.NotNil(t, tool.Annotations.OpenWorldHint, tool.Name)
		assert.False(t, *tool.Annotations.OpenWorldHint, tool.Name)
		assert.Nil(t, tool.Annotations.DestructiveHint, tool.Name)
		marked := strings.Contains(tool.Description, "untrusted")
		assert.Equal(t, slices.Contains(untrustedTools, tool.Name), marked, tool.Name)
	}
	assert.ElementsMatch(t, append(untrustedTools, trustedTools...), names)
}

func (l *allowAllLimiter) CheckRateLimit(id uuid.UUID, _, _ int) (*rate_limit.RateLimitResult, error) {
	l.ids = append(l.ids, id)
	return &rate_limit.RateLimitResult{Allowed: true}, nil
}

// jsonEngine answers every request with respond's value as JSON.
func jsonEngine(t *testing.T, respond func(*http.Request) any) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(respond(r)))
	})
}

func toolRequest(userID uuid.UUID) *mcp.CallToolRequest {
	user := &users_models.User{ID: userID}
	return &mcp.CallToolRequest{Extra: &mcp.RequestExtra{TokenInfo: &auth.TokenInfo{Extra: map[string]any{
		userExtraKey: user, tokenIDExtraKey: uuid.New(), clientIDExtraKey: "client-1",
	}}}}
}

func useLimiter(t *testing.T, limiter rateChecker) {
	t.Helper()
	previous := rateLimiter
	rateLimiter = limiter
	t.Cleanup(func() { rateLimiter = previous })
}
