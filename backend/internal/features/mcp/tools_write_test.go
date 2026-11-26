package mcp_server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	access_tokens "obzly/internal/features/access_tokens"
	users_models "obzly/internal/features/users/models"
	"obzly/internal/util/rate_limit"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSite answers the REST calls the write tools make and records the writes.
type fakeSite struct {
	t         *testing.T
	projectID string
	mu        sync.Mutex
	writes    []string
	bodies    []map[string]any
}

type openLimiter struct{}

type stubWriteLimiter struct {
	allowed bool
	calls   int
}

func init() { access_tokens.SetWriteEnabled(true) }

var writeToolNames = []string{"create_silence", "expire_silence", "create_dashboard", "add_dashboard_panel"}

func Test_WriteTools_AreListedOnlyOnTheWriteServer_WithWriteAnnotations(t *testing.T) {
	readTools := listToolsOf(t, newServer(http.NotFoundHandler(), false, "test"))
	for _, tool := range readTools {
		assert.NotContains(t, writeToolNames, tool.Name)
	}

	writeTools := listToolsOf(t, newWriteServer(http.NotFoundHandler(), false, "test"))
	assert.Len(t, writeTools, len(readTools)+len(writeToolNames))
	wantDestructive := map[string]bool{"expire_silence": true}
	wantIdempotent := map[string]bool{"expire_silence": true}
	found := 0
	for _, tool := range writeTools {
		if !strings.Contains(strings.Join(writeToolNames, " "), tool.Name) {
			assert.True(t, tool.Annotations.ReadOnlyHint, tool.Name)
			continue
		}
		found++
		assert.False(t, tool.Annotations.ReadOnlyHint, tool.Name)
		require.NotNil(t, tool.Annotations.DestructiveHint, tool.Name)
		assert.Equal(t, wantDestructive[tool.Name], *tool.Annotations.DestructiveHint, tool.Name)
		assert.Equal(t, wantIdempotent[tool.Name], tool.Annotations.IdempotentHint, tool.Name)
		require.NotNil(t, tool.Annotations.OpenWorldHint, tool.Name)
		assert.False(t, *tool.Annotations.OpenWorldHint, tool.Name)
		assert.Contains(t, tool.Description, "never instructions", tool.Name)
		assert.Contains(t, tool.Description, "the user asked for this change", tool.Name)
		assert.Contains(t, tool.Description, "confirm: true", tool.Name)
		assert.NotEmpty(t, tool.Title, tool.Name)
		assert.Equal(t, toolMetas[tool.Name].title, tool.Title, tool.Name)
		schema, ok := tool.OutputSchema.(map[string]any)
		require.True(t, ok, "%s has no outputSchema", tool.Name)
		assert.Contains(t, schema["properties"], "changed", tool.Name)
	}
	assert.Equal(t, len(writeToolNames), found)
}

func Test_ValidateSilenceMatchers_RefusesSilencesThatMatchEverything(t *testing.T) {
	refused := map[string][]matcherArg{
		"no matchers":         nil,
		"regex only":          {{Name: "alertname", Value: "High.*", Regex: true}},
		"empty exact value":   {{Name: "alertname", Value: ""}},
		"dot star with exact": {{Name: "alertname", Value: "HighCPU"}, {Name: "env", Value: ".*", Regex: true}},
		"dot plus":            {{Name: "alertname", Value: "HighCPU"}, {Name: "env", Value: ".+|", Regex: true}},
		"bad label":           {{Name: "bad-label", Value: "x"}},
		"bad regex":           {{Name: "alertname", Value: "x"}, {Name: "env", Value: "(", Regex: true}},
		"too many":            make([]matcherArg, 21),
	}
	for name, matchers := range refused {
		_, err := validateSilenceMatchers(matchers)
		assert.Error(t, err, name)
	}

	matchers, err := validateSilenceMatchers([]matcherArg{
		{Name: " alertname ", Value: "HighCPU"}, {Name: "env", Value: "prod|staging", Regex: true},
	})
	require.NoError(t, err)
	assert.Equal(t, "alertname", matchers[0].Name)
}

func Test_ParseSilenceDuration_AllowsAtMostFourHours(t *testing.T) {
	for _, value := range []string{"", "soon", "30s", "4h1m", "1d", "-1h"} {
		_, err := parseSilenceDuration(value)
		assert.Error(t, err, value)
	}
	for value, want := range map[string]time.Duration{"1m": time.Minute, "90m": 90 * time.Minute, "4h": 4 * time.Hour} {
		got, err := parseSilenceDuration(value)
		require.NoError(t, err, value)
		assert.Equal(t, want, got)
	}
}

func Test_CheckSiteCall_WriteEndpoints_OnlyInsideAWriteToolCall(t *testing.T) {
	project, other := uuid.NewString(), uuid.NewString()
	writes := [][2]string{
		{http.MethodPost, "/projects/" + project + "/silences"},
		{http.MethodPost, "/projects/" + project + "/silences/" + other + "/expire"},
		{http.MethodPost, "/projects/dashboards/" + project},
		{http.MethodPost, "/projects/dashboards/" + project + "/" + other + "/panels"},
	}
	readCtx := context.Background()
	writeCtx := context.WithValue(context.Background(), writeAccessKey{}, true)
	for _, call := range writes {
		require.ErrorIs(t, checkSiteCall(readCtx, call[0], call[1]), errCallNotAllowed, call)
		assert.NoError(t, checkSiteCall(writeCtx, call[0], call[1]), call)
	}

	// Never anything else, even with write access: no deletes, no replacing a dashboard.
	notListed := [][2]string{
		{http.MethodDelete, "/projects/" + project + "/silences/" + other},
		{http.MethodPut, "/projects/dashboards/" + project + "/" + other},
		{http.MethodDelete, "/projects/dashboards/" + project + "/" + other},
		{http.MethodPost, "/projects/" + project + "/alert-rules"},
		{http.MethodPost, "/projects/" + project + "/silences/..%2F..%2Falert-rules/expire"},
		{http.MethodPost, "/projects/dashboards/" + project + "/import"},
	}
	for _, call := range notListed {
		assert.ErrorIs(t, checkSiteCall(writeCtx, call[0], call[1]), errCallNotAllowed, call)
	}
	// Reads still work in a write call.
	assert.NoError(t, checkSiteCall(writeCtx, http.MethodGet, "/projects"))
}

func Test_WriteTool_WithoutWriteScopeOrMFA_IsRefused(t *testing.T) {
	useOpenLimiters(t)
	site := newFakeSite(t)
	tools := &toolSet{exec: &apiExecutor{engine: site}}
	handler := wrapWrite("create_silence", tools.planCreateSilence)
	args := site.silenceArgs()

	_, _, err := handler(context.Background(), writeRequest(uuid.New(), []string{access_tokens.ScopeMCPRead}, true),
		args)
	require.ErrorIs(t, err, errNoWriteScope)

	_, _, err = handler(context.Background(), writeRequest(uuid.New(), writeScopes(), false), args)
	require.ErrorIs(t, err, errWriteNeedsMFA)
	assert.Empty(t, site.writes)
}

func Test_WriteTool_WithoutConfirm_ReturnsAPreviewAndWritesNothing(t *testing.T) {
	useOpenLimiters(t)
	site := newFakeSite(t)
	tools := &toolSet{exec: &apiExecutor{engine: site}}
	handler := wrapWrite("create_silence", tools.planCreateSilence)

	result, _, err := handler(context.Background(), writeRequest(uuid.New(), writeScopes(), true), site.silenceArgs())
	require.NoError(t, err)
	text := resultText(result)
	assert.Contains(t, text, `"preview":true`)
	assert.Contains(t, text, `"changed":false`)
	assert.Contains(t, text, `alertname=\"HighCPU\"`)
	assert.Contains(t, text, `"count":1`, "the preview counts the alerts it would mute now")
	assert.Contains(t, text, "confirm: true")
	assert.Empty(t, site.writes)

	args := site.silenceArgs()
	args.Confirm = true
	result, _, err = handler(context.Background(), writeRequest(uuid.New(), writeScopes(), true), args)
	require.NoError(t, err)
	assert.Contains(t, resultText(result), `"changed":true`)
	require.Equal(t, []string{"POST /api/v1/projects/" + site.projectID + "/silences"}, site.writes)
	assert.Equal(t, "Claude Desktop", site.bodies[0]["mcpClient"])
	assert.Equal(t, "maintenance on node-3", site.bodies[0]["comment"])
	endsAt, err := time.Parse(time.RFC3339Nano, site.bodies[0]["endsAt"].(string))
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(2*time.Hour), endsAt, time.Minute)
}

func Test_WriteTool_WhenTheWriteLimitIsReached_RefusesBeforeWriting(t *testing.T) {
	useOpenLimiters(t)
	limiter := &stubWriteLimiter{allowed: false}
	writeRateLimiter = limiter
	site := newFakeSite(t)
	tools := &toolSet{exec: &apiExecutor{engine: site}}
	args := site.silenceArgs()
	args.Confirm = true

	_, _, err := wrapWrite("create_silence", tools.planCreateSilence)(
		context.Background(), writeRequest(uuid.New(), writeScopes(), true), args)

	require.ErrorContains(t, err, "write limit reached")
	assert.Equal(t, 1, limiter.calls)
	assert.Empty(t, site.writes)
}

func Test_WriteTool_PreviewsDontCountAgainstTheWriteLimit(t *testing.T) {
	useOpenLimiters(t)
	limiter := &stubWriteLimiter{allowed: false}
	writeRateLimiter = limiter
	site := newFakeSite(t)
	tools := &toolSet{exec: &apiExecutor{engine: site}}

	_, _, err := wrapWrite("create_silence", tools.planCreateSilence)(
		context.Background(), writeRequest(uuid.New(), writeScopes(), true), site.silenceArgs())

	require.NoError(t, err)
	assert.Zero(t, limiter.calls)
}

func Test_CreateSilence_InvalidArguments_AreRefusedBeforeAnyCall(t *testing.T) {
	useOpenLimiters(t)
	site := newFakeSite(t)
	tools := &toolSet{exec: &apiExecutor{engine: site}}
	handler := wrapWrite("create_silence", tools.planCreateSilence)
	cases := map[string]func(*createSilenceArgs){
		"no comment":      func(a *createSilenceArgs) { a.Comment = "  " },
		"five hours":      func(a *createSilenceArgs) { a.Duration = "5h" },
		"match all":       func(a *createSilenceArgs) { a.Matchers = []matcherArg{{Name: "x", Value: ".*", Regex: true}} },
		"long comment":    func(a *createSilenceArgs) { a.Comment = strings.Repeat("x", 1001) },
		"unknown project": func(a *createSilenceArgs) { a.Project = "nope" },
	}
	for name, change := range cases {
		args := site.silenceArgs()
		args.Confirm = true
		change(&args)
		_, _, err := handler(context.Background(), writeRequest(uuid.New(), writeScopes(), true), args)
		assert.Error(t, err, name)
	}
	assert.Empty(t, site.writes)
}

func Test_Confirmation_FollowsTheUsersElicitationAnswer(t *testing.T) {
	state := confirmState("create_silence", uuid.New(), uuid.New(), `{"a":1}`)
	answer := func(action string, confirm any, requestState string) *mcp.CallToolRequest {
		return &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{
			InputResponses: mcp.InputResponseMap{confirmInputKey: &mcp.ElicitResult{
				Action: action, Content: map[string]any{"confirm": confirm},
			}},
			RequestState: requestState,
		}}
	}

	assert.Equal(t, confirmGranted, confirmation(answer("accept", true, state), confirmArg{}, state))
	assert.Equal(t, confirmDeclined, confirmation(answer("accept", false, state), confirmArg{}, state))
	assert.Equal(t, confirmDeclined, confirmation(answer("decline", nil, state), confirmArg{Confirm: true}, state))
	assert.Equal(t, confirmDeclined, confirmation(answer("cancel", nil, state), confirmArg{}, state))
	// An answer given for other arguments is asked again.
	assert.Equal(t, confirmAsk, confirmation(answer("accept", true, "v1:other"), confirmArg{}, state))

	plain := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{}}
	assert.Equal(t, confirmPreview, confirmation(plain, confirmArg{}, state))
	assert.Equal(t, confirmGranted, confirmation(plain, confirmArg{Confirm: true}, state))
}

func Test_AuditArgs_AreCanonicalAndLeaveOutConfirm(t *testing.T) {
	args := createSilenceArgs{Duration: "1h", Comment: "x", confirmArg: confirmArg{Confirm: true}}
	args.Project = "Shop"
	encoded := auditArgs(args)
	assert.NotContains(t, encoded, "confirm")
	assert.Contains(t, encoded, `"project":"Shop"`)
	args.Confirm = false
	assert.Equal(t, encoded, auditArgs(args), "confirming must not change what was confirmed")
}

func Test_ValkeyWriteLimiter_AllowsTenWritesPerWindow(t *testing.T) {
	userID := uuid.New()
	for i := range writeLimitPerWindow {
		allowed, _, err := valkeyWriteLimiter{}.allowWrite(userID)
		require.NoError(t, err)
		require.True(t, allowed, "write %d", i+1)
	}
	allowed, retryAfter, err := valkeyWriteLimiter{}.allowWrite(userID)
	require.NoError(t, err)
	assert.False(t, allowed)
	assert.Positive(t, retryAfter)
	assert.LessOrEqual(t, retryAfter, writeLimitWindow)

	allowed, _, err = valkeyWriteLimiter{}.allowWrite(uuid.New())
	require.NoError(t, err)
	assert.True(t, allowed, "the limit is per user")
}

func Test_BuildPanel_ValidatesLikeADashboardSave(t *testing.T) {
	refused := map[string]panelArg{
		"no title":               {Type: "line"},
		"unknown type":           {Title: "x", Type: "sparkles"},
		"thresholds on line":     {Title: "x", Type: "line", Warn: new(1.0)},
		"min above max":          {Title: "x", Type: "gauge", PromQL: "up", Min: new(2.0), Max: new(1.0)},
		"promql on logs":         {Title: "x", Source: "logs", PromQL: "up"},
		"bad logs query":         {Title: "x", Query: "(level:error"},
		"metrics without promql": {Title: "x", Source: "metrics"},
		"bad spans metric":       {Title: "x", Metric: "p42"},
		"unknown source":         {Title: "x", Source: "events"},
		"unknown unit":           {Title: "x", Unit: "furlongs"},
		"sql on logs":            {Title: "x", Source: "logs", SQL: "SELECT 1"},
		"sql with groupField":    {Title: "x", SQL: "SELECT 1", GroupField: "level"},
		"sql gauge":              {Title: "x", Type: "gauge", SQL: "SELECT 1 AS value"},
		"refused sql function":   {Title: "x", Type: "table", SQL: "SELECT * FROM read_csv('x')"},
	}
	for name, arg := range refused {
		_, err := buildPanel(arg)
		assert.Error(t, err, name)
	}

	logs, err := buildPanel(panelArg{Title: "Errors", Query: "level:error", GroupField: "service_name"})
	require.NoError(t, err)
	assert.Equal(t, "logs", string(logs.Source))
	assert.NotNil(t, logs.Query)
	assert.Empty(t, logs.ID, "the server assigns panel ids")
	assert.Equal(t, 6, logs.GridPos.W)

	metrics, err := buildPanel(panelArg{Title: "CPU", Type: "gauge", PromQL: "avg(cpu)", Max: new(1.0), Width: 20})
	require.NoError(t, err)
	assert.Equal(t, "metrics", string(metrics.Source))
	assert.Equal(t, 12, metrics.GridPos.W)

	spans, err := buildPanel(panelArg{Title: "p95", Type: "stat", Service: "api", Metric: "p95_duration_ms"})
	require.NoError(t, err)
	assert.Equal(t, "spans", string(spans.Source))

	sql, err := buildPanel(panelArg{Title: "Levels", Type: "donut", SQL: " SELECT level, count(*) FROM logs GROUP BY 1 "})
	require.NoError(t, err)
	assert.Equal(t, "logs_sql", string(sql.Source))
	assert.Equal(t, "SELECT level, count(*) FROM logs GROUP BY 1", sql.SQL)
	assert.Equal(t, "logs SQL: SELECT level, count(*) FROM logs GROUP BY 1", describePanelQuery(sql))
}

func newFakeSite(t *testing.T) *fakeSite {
	return &fakeSite{t: t, projectID: uuid.NewString()}
}

func (s *fakeSite) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && path == "/api/v1/projects":
		s.respond(w, map[string]any{"projects": []map[string]any{{"id": s.projectID, "name": "Shop"}}})
	case r.Method == http.MethodGet && path == "/api/v1/projects/"+s.projectID+"/alerts":
		s.respond(w, []map[string]any{
			{"ruleName": "HighCPU", "state": "firing", "labels": map[string]string{"alertname": "HighCPU"}},
			{"ruleName": "HighCPU", "state": "resolved", "labels": map[string]string{"alertname": "HighCPU"}},
			{"ruleName": "DiskFull", "state": "firing", "labels": map[string]string{"alertname": "DiskFull"}},
		})
	case r.Method == http.MethodPost:
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		s.mu.Lock()
		s.writes = append(s.writes, r.Method+" "+path)
		s.bodies = append(s.bodies, body)
		s.mu.Unlock()
		s.respond(w, map[string]any{"id": uuid.NewString(), "comment": body["comment"]})
	default:
		w.WriteHeader(http.StatusNotFound)
		s.respond(w, map[string]string{"error": "not found"})
	}
}

func (s *fakeSite) respond(w http.ResponseWriter, value any) {
	require.NoError(s.t, json.NewEncoder(w).Encode(value))
}

func (s *fakeSite) silenceArgs() createSilenceArgs {
	args := createSilenceArgs{
		Matchers: []matcherArg{{Name: "alertname", Value: "HighCPU"}}, Duration: "2h",
		Comment: "maintenance on node-3",
	}
	args.Project = "Shop"
	return args
}

func (openLimiter) CheckRateLimit(uuid.UUID, int, int) (*rate_limit.RateLimitResult, error) {
	return &rate_limit.RateLimitResult{Allowed: true}, nil
}

func (l *stubWriteLimiter) allowWrite(uuid.UUID) (bool, time.Duration, error) {
	l.calls++
	return l.allowed, time.Minute, nil
}

func useOpenLimiters(t *testing.T) {
	t.Helper()
	previous, previousWrite := rateLimiter, writeRateLimiter
	rateLimiter, writeRateLimiter = openLimiter{}, &stubWriteLimiter{allowed: true}
	t.Cleanup(func() { rateLimiter, writeRateLimiter = previous, previousWrite })
}

func writeScopes() []string {
	return []string{access_tokens.ScopeMCPRead, access_tokens.ScopeMCPWrite}
}

func writeRequest(userID uuid.UUID, scopes []string, mfaSatisfied bool) *mcp.CallToolRequest {
	return &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{}, Extra: &mcp.RequestExtra{TokenInfo: &auth.TokenInfo{
		Scopes: scopes,
		Extra: map[string]any{
			userExtraKey: &users_models.User{ID: userID}, mfaExtraKey: mfaSatisfied, tokenIDExtraKey: uuid.New(),
			clientIDExtraKey: "client-1", tokenNameExtraKey: "Claude Desktop",
		},
	}}}
}

func listToolsOf(t *testing.T, server *mcp.Server) []*mcp.Tool {
	t.Helper()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = serverSession.Close() })
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).
		Connect(context.Background(), clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	tools, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	return tools.Tools
}
