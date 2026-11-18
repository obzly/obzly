package mcp_tools_tests

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"obzly/internal/features/alerting"
	dashboards_dto "obzly/internal/features/dashboards/dto"
	dashboards_models "obzly/internal/features/dashboards/models"
	"obzly/internal/features/savedviews"
	test_utils "obzly/internal/util/testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_MCP_Resources_ReadTheCallersProjectData(t *testing.T) {
	e := newEnv(t, "MCP resources")
	auth := "Bearer " + e.owner.Token
	project := e.project.ID.String()
	var dashboard dashboards_models.Dashboard
	test_utils.MakePostRequestAndUnmarshal(t, e.router, "/api/v1/projects/dashboards/"+project, auth,
		dashboards_dto.CreateDashboardRequestDTO{Title: "Checkout overview"}, http.StatusOK, &dashboard)
	test_utils.MakePostRequest(t, e.router, "/api/v1/projects/"+project+"/alert-rules", auth, alerting.RuleRequest{
		Name: "Checkout errors", Source: "metrics", Query: alerting.RuleQuery{PromQL: "vector(1)"},
		Comparison: ">", Threshold: 1,
	}, http.StatusOK)
	test_utils.MakePostRequest(t, e.router, "/api/v1/saved-views", auth, savedviews.ViewRequest{
		Kind: savedviews.KindCost, Name: "Monthly by service", State: json.RawMessage(`{"groupBy":"service"}`),
	}, http.StatusOK)

	projects := readResource(t, e.session, "obzly://projects")
	assert.Contains(t, projectIDs(t, projects), project)

	dashboards := readResource(t, e.session, "obzly://projects/"+project+"/dashboards")
	assert.Contains(t, dashboards[untrustedKey], "untrusted")
	listed := list(t, dashboards, "dashboards")
	require.NotEmpty(t, listed)

	detail := readResource(t, e.session, "obzly://projects/"+project+"/dashboards/"+dashboard.ID.String())
	assert.Equal(t, "Checkout overview", detail["title"])

	rules := readResource(t, e.session, "obzly://projects/"+project+"/alert-rules")
	require.Len(t, list(t, rules, "rules"), 1)
	assert.Equal(t, "Checkout errors", list(t, rules, "rules")[0].(map[string]any)["name"])

	sources := readResource(t, e.session, "obzly://projects/"+project+"/data-sources")
	assert.Contains(t, sources, "sources")

	views := readResource(t, e.session, "obzly://saved-views/cost")
	require.Len(t, list(t, views, "views"), 1)
	assert.Equal(t, "Monthly by service", list(t, views, "views")[0].(map[string]any)["name"])

	sites := readResource(t, e.session, "obzly://sites")
	assert.Len(t, list(t, sites, "sites"), 1)
}

func Test_MCP_Resources_OfAnotherUsersProject_AreNotReadable(t *testing.T) {
	e := newEnv(t, "MCP private resources")
	project := e.project.ID.String()

	for _, uri := range []string{
		"obzly://projects/" + project + "/dashboards",
		"obzly://projects/" + project + "/dashboards/00000000-0000-0000-0000-000000000001",
		"obzly://projects/" + project + "/data-sources",
		"obzly://projects/" + project + "/alert-rules",
	} {
		_, err := e.outsider.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
		assert.Error(t, err, uri)
	}
	projects := readResource(t, e.outsider, "obzly://projects")
	assert.NotContains(t, projectIDs(t, projects), project)
}

func Test_MCP_ToolResults_ComeAsStructuredContentToo(t *testing.T) {
	e := newEnv(t, "MCP structured")

	for name, args := range map[string]map[string]any{
		"alert_summary":    {"project": e.project.ID.String()},
		"list_alert_rules": {"project": e.project.ID.String()},
		"ingestion_status": {"project": e.project.ID.String()},
		"list_projects":    {},
		"list_sites":       {},
	} {
		result, err := e.session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
		require.NoError(t, err, name)
		require.False(t, result.IsError, "%s: %s", name, text(result))
		var fromText map[string]any
		require.NoError(t, json.Unmarshal([]byte(text(result)), &fromText), name)
		assert.Equal(t, fromText, result.StructuredContent, name)
	}
}

const untrustedKey = "untrustedData"

// readResource reads a resource that must exist and decodes its JSON.
func readResource(t *testing.T, session *mcp.ClientSession, uri string) map[string]any {
	t.Helper()
	result, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
	require.NoError(t, err, uri)
	require.Len(t, result.Contents, 1, uri)
	assert.Equal(t, "application/json", result.Contents[0].MIMEType, uri)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Contents[0].Text), &decoded), uri)
	return decoded
}

func projectIDs(t *testing.T, result map[string]any) []string {
	t.Helper()
	var ids []string
	for _, item := range list(t, result, "projects") {
		id, _ := item.(map[string]any)["id"].(string)
		ids = append(ids, id)
	}
	return ids
}
