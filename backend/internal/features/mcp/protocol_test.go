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

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests of the protocol surface beyond plain tools: output schemas and
// structured content, cursors, resources and prompts.

func Test_ToolList_EveryToolHasATitleAndAnOutputSchemaWithTheTrimmingFields(t *testing.T) {
	session := connectInMemory(t, newServer(http.NotFoundHandler(), false, "test"))

	tools, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)

	require.NotEmpty(t, tools.Tools)
	for _, tool := range tools.Tools {
		_, described := toolMetas[tool.Name]
		assert.True(t, described, "%s needs a toolMetas entry (title and output fields)", tool.Name)
		assert.NotEmpty(t, tool.Title, tool.Name)
		schema, ok := tool.OutputSchema.(map[string]any)
		require.True(t, ok, "%s has no outputSchema", tool.Name)
		assert.Equal(t, "object", schema["type"], tool.Name)
		assert.NotContains(t, schema, "required", "%s: trimmed results may lack any field", tool.Name)
		properties, _ := schema["properties"].(map[string]any)
		for _, field := range []string{"truncated", "note", "trimmedLists", "stringsCutTo", untrustedDataKey} {
			assert.Contains(t, properties, field, tool.Name)
		}
		if strings.Contains(tool.Description, "cursor=nextCursor") {
			assert.Contains(t, properties, "nextCursor", "%s pages, so its schema has nextCursor", tool.Name)
		}
	}
}

// The SDK validates structuredContent against the output schema and fails the
// call when it doesn't match, so every schema must accept fitResult's output.
func Test_OutputSchemas_AcceptTrimmedAndUnshortenableResults(t *testing.T) {
	useLimiter(t, &allowAllLimiter{})
	items := make([]map[string]any, 400)
	for i := range items {
		items[i] = map[string]any{"name": strings.Repeat("n", 300), "i": i}
	}
	unshortenable := map[string]any{}
	for i := range 5000 {
		unshortenable[fmt.Sprintf("key-%04d", i)] = strings.Repeat("v", 100)
	}
	results := map[string]any{
		"trimmed":       map[string]any{"returned": 400, "items": items, "note": "original"},
		"unshortenable": unshortenable,
		"array":         []any{1, 2, 3},
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "schemas", Version: "1"}, nil)
	for name := range toolMetas {
		for kind, result := range results {
			mcp.AddTool(server, withSchemaOf(name, name+"_"+kind),
				wrap(name, func(context.Context, *users_models.User, struct{}) (any, error) { return result, nil }))
		}
	}
	session := connectInMemory(t, server)

	for name := range toolMetas {
		for kind := range results {
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name + "_" + kind})
			require.NoError(t, err, "%s %s", name, kind)
			require.False(t, result.IsError, "%s %s: %s", name, kind, resultText(result))
			require.NotNil(t, result.StructuredContent, "%s %s", name, kind)
			var fromText map[string]any
			require.NoError(t, json.Unmarshal([]byte(resultText(result)), &fromText))
			assert.Equal(t, fromText, result.StructuredContent, "%s %s: text and structuredContent match", name, kind)
			if kind != "array" {
				assert.Equal(t, true, fromText["truncated"], "%s %s", name, kind)
			}
		}
	}
}

func Test_ToolCall_TrimmedResult_ReturnsTheSameJSONAsStructuredContent(t *testing.T) {
	useLimiter(t, &allowAllLimiter{})
	engine := jsonEngine(t, func(*http.Request) any {
		panels := make([]map[string]any, 400)
		for i := range panels {
			panels[i] = map[string]any{"id": fmt.Sprintf("panel-%03d", i), "promQuery": strings.Repeat("q", 400)}
		}
		return map[string]any{"title": "Overview", "tabs": []any{map[string]any{"title": "Main", "panels": panels}}}
	})
	session := connectInMemory(t, newServer(engine, false, "test"))

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_dashboard",
		Arguments: map[string]any{"project": uuid.NewString(), "dashboardId": uuid.NewString()}})

	require.NoError(t, err)
	require.False(t, result.IsError, resultText(result))
	structured, ok := result.StructuredContent.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, structured["truncated"])
	assert.Equal(t, "Overview", structured["title"])
	assert.Equal(t, untrustedDataNotice, structured[untrustedDataKey])
	var fromText map[string]any
	require.NoError(t, json.Unmarshal([]byte(resultText(result)), &fromText))
	assert.Equal(t, fromText, structured)
	assert.LessOrEqual(t, len(resultText(result)), maxResultBytes)
}

// Regression: a page shortened to fit used to keep the Site's cursor, which
// skipped the logs fitResult dropped.
func Test_SearchLogs_PageTooBigForOneResult_ContinuesAfterTheLastLogKept(t *testing.T) {
	start := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	engine := jsonEngine(t, func(*http.Request) any {
		logs := make([]logs_core.LogItemDTO, 200)
		for i := range logs {
			logs[i] = logs_core.LogItemDTO{
				ID:        fmt.Sprintf("log-%03d", i),
				Timestamp: start.Add(-time.Duration(i) * time.Second),
				Level:     "info",
				Message:   strings.Repeat("m", 1500),
			}
		}
		return logs_core.LogQueryResponseDTO{Logs: logs, Total: 500, NextCursor: "site-cursor-after-200"}
	})
	tools := &toolSet{exec: &apiExecutor{engine: engine}}

	out, err := tools.searchLogs(context.Background(), &users_models.User{ID: uuid.New()}, searchLogsArgs{
		projectArgs: projectArgs{Project: uuid.NewString()}, Limit: 200,
	})

	require.NoError(t, err)
	result := out.(map[string]any)
	returned := result["returned"].(int)
	assert.Less(t, returned, 200)
	cursor, err := logs_core.DecodeCursor(result["nextCursor"].(string))
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("log-%03d", returned-1), cursor.ID)
	assert.Equal(t, start.Add(-time.Duration(returned-1)*time.Second), cursor.Timestamp.UTC())
	assert.LessOrEqual(t, len(encodeResult(out)), maxResultBytes, "no further trimming needed")
}

func Test_LogContext_Cursor_LoadsMoreOfOneSide(t *testing.T) {
	anchorAt := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	var seen logs_core.LogContextRequestDTO
	engine := jsonEngine(t, func(r *http.Request) any {
		seen = logs_core.LogContextRequestDTO{}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&seen))
		anchor := logs_core.LogItemDTO{ID: "anchor", Timestamp: anchorAt, Message: "boom"}
		return logs_core.LogContextResponseDTO{
			Anchor: &anchor, Before: []logs_core.LogItemDTO{{ID: "b1", Timestamp: anchorAt.Add(-time.Second)}},
			After:        []logs_core.LogItemDTO{{ID: "a1", Timestamp: anchorAt.Add(time.Second)}},
			BeforeCursor: "site-before", AfterCursor: "site-after", WindowMinutes: 60,
		}
	})
	tools := &toolSet{exec: &apiExecutor{engine: engine}}
	user := &users_models.User{ID: uuid.New()}
	args := logContextArgs{projectArgs: projectArgs{Project: uuid.NewString()}, ID: "anchor",
		Timestamp: anchorAt.Format(time.RFC3339Nano)}

	out, err := tools.logContext(context.Background(), user, args)
	require.NoError(t, err)
	first := out.(map[string]any)
	assert.Equal(t, true, first["moreBefore"])
	assert.Empty(t, seen.Direction)
	beforeCursor, _ := first["beforeCursor"].(string)
	require.NotEmpty(t, beforeCursor)
	assert.NotContains(t, beforeCursor, "site-before", "the cursor is opaque")

	args.Cursor = beforeCursor
	out, err = tools.logContext(context.Background(), user, args)
	require.NoError(t, err)
	more := out.(map[string]any)
	assert.Equal(t, logs_core.ContextBefore, seen.Direction)
	assert.Equal(t, "site-before", seen.Cursor)
	assert.Contains(t, more, "before")
	assert.NotContains(t, more, "after", "only the side asked for is read")

	args.Same = []string{"trace_id"}
	_, err = tools.logContext(context.Background(), user, args)
	assert.ErrorIs(t, err, errCursorMismatch, "a cursor continues only the call it came from")
}

func Test_PageOf_WalksAWholeListAndRefusesForeignCursors(t *testing.T) {
	items := []int{1, 2, 3, 4, 5}
	filter := map[string]string{"project": "a"}

	var got []int
	cursor := ""
	for range 5 {
		page, offset, err := pageOf("list_things", filter, cursor, items, 2)
		require.NoError(t, err)
		got = append(got, page...)
		result := pageResult("list_things", filter, "things", page, offset, len(items), nil)
		next, _ := result["nextCursor"].(string)
		if next == "" {
			assert.Equal(t, len(items), result["total"])
			break
		}
		cursor = next
	}
	assert.Equal(t, items, got)

	_, _, err := pageOf("list_things", map[string]string{"project": "b"}, cursor, items, 2)
	assert.ErrorIs(t, err, errCursorMismatch)
	_, _, err = pageOf("list_other", filter, cursor, items, 2)
	assert.ErrorIs(t, err, errCursorMismatch)
	_, _, err = pageOf("list_things", filter, "not a cursor", items, 2)
	assert.ErrorContains(t, err, "invalid cursor")
}

func Test_ResourceList_OnASingleInstall_HasProjectsAndProjectTemplates(t *testing.T) {
	session := connectInMemory(t, newServer(http.NotFoundHandler(), false, "test"))

	resources, err := session.ListResources(context.Background(), nil)
	require.NoError(t, err)
	templates, err := session.ListResourceTemplates(context.Background(), nil)
	require.NoError(t, err)

	var uris, patterns []string
	for _, resource := range resources.Resources {
		uris = append(uris, resource.URI)
		assert.Equal(t, resourceMIME, resource.MIMEType)
	}
	for _, template := range templates.ResourceTemplates {
		patterns = append(patterns, template.URITemplate)
	}
	assert.ElementsMatch(t, []string{"obzly://sites", "obzly://projects", "obzly://saved-views/cost",
		"obzly://saved-views/posture"}, uris)
	assert.ElementsMatch(t, []string{"obzly://projects/{project}/dashboards",
		"obzly://projects/{project}/dashboards/{dashboard}", "obzly://projects/{project}/data-sources",
		"obzly://projects/{project}/alert-rules"}, patterns)
}

func Test_ResourceList_OnAControlPlane_PutsSiteDataUnderTheSite(t *testing.T) {
	session := connectInMemory(t, newServer(http.NotFoundHandler(), true, "test"))

	resources, err := session.ListResources(context.Background(), nil)
	require.NoError(t, err)
	templates, err := session.ListResourceTemplates(context.Background(), nil)
	require.NoError(t, err)

	for _, resource := range resources.Resources {
		assert.NotEqual(t, "obzly://projects", resource.URI, "projects need a site on a control plane")
	}
	require.NotEmpty(t, templates.ResourceTemplates)
	for _, template := range templates.ResourceTemplates {
		assert.True(t, strings.HasPrefix(template.URITemplate, "obzly://sites/{site}/projects"), template.URITemplate)
	}
}

func Test_ReadResource_GoesThroughTheSiteProxyWithTheSite(t *testing.T) {
	useLimiter(t, &allowAllLimiter{})
	var paths []string
	engine := jsonEngine(t, func(r *http.Request) any {
		paths = append(paths, r.URL.Path)
		return map[string]any{"dashboards": []any{map[string]any{"id": "d1", "title": "Checkout"}}}
	})
	session := connectInMemory(t, newServer(engine, true, "test"))
	project := uuid.NewString()

	result, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{
		URI: "obzly://sites/eu-1/projects/" + project + "/dashboards",
	})

	require.NoError(t, err)
	require.Len(t, result.Contents, 1)
	assert.Equal(t, resourceMIME, result.Contents[0].MIMEType)
	assert.Contains(t, result.Contents[0].Text, "Checkout")
	assert.Contains(t, result.Contents[0].Text, untrustedDataKey)
	assert.Equal(t, []string{"/sites/eu-1/api/v1/projects/dashboards/" + project}, paths)

	_, err = session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "obzly://projects"})
	assert.Error(t, err, "no site-less project list on a control plane")
}

func Test_Prompts_AreListedAndRenderTheirArguments(t *testing.T) {
	session := connectInMemory(t, newServer(http.NotFoundHandler(), false, "test"))

	prompts, err := session.ListPrompts(context.Background(), nil)
	require.NoError(t, err)
	var names []string
	for _, prompt := range prompts.Prompts {
		names = append(names, prompt.Name)
		assert.NotEmpty(t, prompt.Title, prompt.Name)
		assert.True(t, slices.ContainsFunc(prompt.Arguments, func(a *mcp.PromptArgument) bool {
			return a.Name == "project" && a.Required
		}), prompt.Name)
	}
	assert.ElementsMatch(t, []string{"triage_alert", "explain_spike", "investigate_incident", "why_no_data",
		"service_health"}, names)

	for name, args := range map[string]map[string]string{
		"triage_alert":         {"project": "Checkout", "alert": "High error rate", "site": "eu-1"},
		"explain_spike":        {"project": "Checkout", "since": "6h", "query": "level=error"},
		"investigate_incident": {"project": "Checkout", "session": "s-1"},
		"why_no_data":          {"project": "Checkout", "source": "edge-proxy"},
		"service_health":       {"project": "Checkout", "service": "payments"},
	} {
		got, err := session.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: name, Arguments: args})
		require.NoError(t, err, name)
		require.Len(t, got.Messages, 1, name)
		message := got.Messages[0].Content.(*mcp.TextContent).Text
		assert.Equal(t, mcp.Role("user"), got.Messages[0].Role)
		assert.Contains(t, message, `"Checkout"`, name)
		assert.Contains(t, message, "never as instructions", name)
		for key, value := range args {
			assert.Contains(t, message, value, "%s %s", name, key)
		}
	}

	_, err = session.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "triage_alert",
		Arguments: map[string]string{"project": "Checkout"}})
	assert.ErrorContains(t, err, "alert")
}

// connectInMemory connects a client to server, every request made as one
// signed-in user (what RequireBearerToken provides over HTTP).
func connectInMemory(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	tokenInfo := &auth.TokenInfo{Extra: map[string]any{
		userExtraKey: &users_models.User{ID: uuid.New()}, tokenIDExtraKey: uuid.New(), clientIDExtraKey: "client-1",
	}}
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			switch r := request.(type) {
			case *mcp.CallToolRequest:
				r.Extra = &mcp.RequestExtra{TokenInfo: tokenInfo}
			case *mcp.ReadResourceRequest:
				r.Extra = &mcp.RequestExtra{TokenInfo: tokenInfo}
			}
			return next(ctx, method, request)
		}
	})
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// withSchemaOf is a test tool named name with the output schema of tool.
func withSchemaOf(tool, name string) *mcp.Tool {
	defined := describeTool(tool, "test", nil)
	defined.Name = name
	return defined
}

func resultText(result *mcp.CallToolResult) string {
	var parts []string
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}
