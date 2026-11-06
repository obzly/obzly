package aiusage_tests

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"obzly/internal/features/aiusage"
	"obzly/internal/features/alerting"
	logs_core "obzly/internal/features/logs/core"
	projects_controllers "obzly/internal/features/projects/controllers"
	projects_models "obzly/internal/features/projects/models"
	projects_testing "obzly/internal/features/projects/testing"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_GetOverview_WithClaudeCodeEvents_AggregatesUsage(t *testing.T) {
	router, project, token := setupProjectWithEvents(t)

	var response aiusage.OverviewResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t,
		router,
		overviewURL(project.ID, nil),
		"Bearer "+token,
		http.StatusOK,
		&response,
	)

	summary := response.Summary
	assert.Equal(t, int64(3), summary.Requests)
	assert.InDelta(t, 0.6, summary.CostUSD, 1e-9)
	assert.Equal(t, int64(1300), summary.InputTokens)
	assert.Equal(t, int64(130), summary.OutputTokens)
	assert.Equal(t, int64(3000), summary.CacheReadTokens)
	assert.Equal(t, int64(2), summary.Sessions)
	assert.Equal(t, int64(2), summary.Users)
	assert.Equal(t, int64(1), summary.Prompts)
	assert.Equal(t, int64(1), summary.APIErrors)
	assert.Equal(t, int64(2), summary.ToolCalls)

	require.Len(t, response.Models, 2)
	assert.Equal(t, "claude-opus-5-5", response.Models[0].Key)
	assert.InDelta(t, 0.5, response.Models[0].CostUSD, 1e-9)

	require.Len(t, response.Users, 2)
	assert.Equal(t, "b@example.com", response.Users[0].Key)
	assert.InDelta(t, 0.4, response.Users[0].CostUSD, 1e-9)
	assert.Equal(t, int64(1), response.Users[0].Sessions)

	var timeseriesCost float64
	for _, point := range response.Timeseries {
		timeseriesCost += point.CostUSD
	}
	assert.InDelta(t, 0.6, timeseriesCost, 1e-9)

	require.Len(t, response.Tools, 1)
	assert.Equal(t, "Bash", response.Tools[0].Tool)
	assert.Equal(t, int64(2), response.Tools[0].Calls)
	assert.Equal(t, int64(1), response.Tools[0].Failures)
	assert.Equal(t, int64(1), response.Tools[0].Rejections)

	require.Len(t, response.Errors, 1)
	assert.Equal(t, "529", response.Errors[0].StatusCode)

	assert.Equal(t, []string{"a@example.com", "b@example.com"}, response.FilterOptions.Users)
}

func Test_GetOverview_WithUserFilter_OnlyCountsThatUser(t *testing.T) {
	router, project, token := setupProjectWithEvents(t)

	var response aiusage.OverviewResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t, router, overviewURL(project.ID, url.Values{"user": {"b@example.com"}}),
		"Bearer "+token, http.StatusOK, &response,
	)

	assert.Equal(t, int64(1), response.Summary.Requests)
	assert.InDelta(t, 0.4, response.Summary.CostUSD, 1e-9)
	assert.Equal(t, int64(1), response.Summary.Users)
	assert.Len(t, response.FilterOptions.Users, 2)
}

func Test_GetSessions_SortedByCost_ReturnsPageAndTotal(t *testing.T) {
	router, project, token := setupProjectWithEvents(t)

	var response aiusage.SessionsResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t, router, sessionsURL(project.ID, url.Values{"sortBy": {"cost"}, "limit": {"1"}}),
		"Bearer "+token, http.StatusOK, &response,
	)

	assert.Equal(t, int64(2), response.Total)
	require.Len(t, response.Sessions, 1)
	assert.Equal(t, "session-b", response.Sessions[0].SessionID)
	assert.Equal(t, "b@example.com", response.Sessions[0].User)
	assert.Equal(t, []string{"claude-opus-5-5"}, response.Sessions[0].Models)
}

func Test_GetSessions_WithSeveralModels_ListsThemSorted(t *testing.T) {
	router, project, token := setupProjectWithEvents(t)

	var response aiusage.SessionsResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t, router, sessionsURL(project.ID, url.Values{"search": {"session-a"}}),
		"Bearer "+token, http.StatusOK, &response,
	)

	require.Len(t, response.Sessions, 1)
	assert.Equal(t, []string{"claude-haiku-4-5-20251001", "claude-opus-5-5"}, response.Sessions[0].Models)
}

func Test_GetSessions_WithSearch_MatchesSessionID(t *testing.T) {
	router, project, token := setupProjectWithEvents(t)

	var response aiusage.SessionsResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t, router, sessionsURL(project.ID, url.Values{"search": {"SION-A"}}),
		"Bearer "+token, http.StatusOK, &response,
	)

	assert.Equal(t, int64(1), response.Total)
	require.Len(t, response.Sessions, 1)
	assert.Equal(t, "session-a", response.Sessions[0].SessionID)
	assert.Equal(t, int64(2), response.Sessions[0].Requests)
	assert.Equal(t, int64(1), response.Sessions[0].Prompts)
}

func Test_GetSessionEvents_ReturnsSessionEventsInOrder(t *testing.T) {
	router, project, token := setupProjectWithEvents(t)

	var response aiusage.SessionEventsResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t, router, withRange(fmt.Sprintf("/api/v1/ai-usage/sessions/%s/session-b/events", project.ID), nil),
		"Bearer "+token, http.StatusOK, &response,
	)

	require.Len(t, response.Events, 2)
	assert.Equal(t, "api_request", response.Events[0].Event)
	assert.Equal(t, "400000", response.Events[0].Fields["cost_usd_micros"])
	assert.Equal(t, "api_error", response.Events[1].Event)
	assert.False(t, response.Truncated)
}

func Test_GetOverview_ReturnsLatestEventAndRoundBucket(t *testing.T) {
	router, project, token := setupProjectWithEvents(t)

	var response aiusage.OverviewResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t,
		router,
		overviewURL(project.ID, nil),
		"Bearer "+token,
		http.StatusOK,
		&response,
	)

	assert.Equal(t, int64(300), response.BucketSeconds)
	latest := time.UnixMilli(response.LatestEventAt)
	assert.WithinDuration(t, time.Now().UTC().Add(-10*time.Minute+7*time.Second), latest, 5*time.Second)
}

func Test_GetEvents_WithEventFilter_ReturnsOnlyThatEventNewestFirst(t *testing.T) {
	router, project, token := setupProjectWithEvents(t)

	var response aiusage.EventsResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t, router, eventsURL(project.ID, url.Values{"event": {"tool_result"}}),
		"Bearer "+token, http.StatusOK, &response,
	)

	assert.Equal(t, int64(2), response.Total)
	require.Len(t, response.Events, 2)
	assert.Equal(t, "tool_result", response.Events[0].Event)
	assert.Equal(t, "false", response.Events[0].Fields["success"])
	assert.Greater(t, response.Events[0].Timestamp, response.Events[1].Timestamp)
	assert.ElementsMatch(t,
		[]string{"api_error", "api_request", "tool_decision", "tool_result", "user_prompt"}, response.EventTypes)
}

func Test_GetEvents_WithSearchAndPaging_ReturnsMatchingPage(t *testing.T) {
	router, project, token := setupProjectWithEvents(t)

	var search aiusage.EventsResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t, router, eventsURL(project.ID, url.Values{"search": {"overloaded"}}),
		"Bearer "+token, http.StatusOK, &search,
	)
	require.Len(t, search.Events, 1)
	assert.Equal(t, "api_error", search.Events[0].Event)

	var page aiusage.EventsResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t, router, eventsURL(project.ID, url.Values{"limit": {"3"}, "offset": {"3"}}),
		"Bearer "+token, http.StatusOK, &page,
	)
	assert.Equal(t, int64(8), page.Total)
	assert.Len(t, page.Events, 3)
}

func Test_GetOverview_WhenUserIsNotProjectMember_ReturnsForbidden(t *testing.T) {
	router, project, _ := setupProjectWithEvents(t)
	outsider := users_testing.CreateTestUser(users_enums.UserRoleMember)

	test_utils.MakeGetRequest(t, router, overviewURL(project.ID, nil), "Bearer "+outsider.Token, http.StatusForbidden)
}

func Test_GetOverview_WithoutTimeRange_ReturnsBadRequest(t *testing.T) {
	router := createRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("AI usage "+uuid.NewString()[:8], owner.Token, router)

	test_utils.MakeGetRequest(
		t,
		router,
		fmt.Sprintf("/api/v1/ai-usage/overview/%s", project.ID),
		"Bearer "+owner.Token,
		http.StatusBadRequest,
	)
}

func Test_GetSessions_WithLimitAboveMax_ReturnsBadRequest(t *testing.T) {
	router, project, token := setupProjectWithEvents(t)

	test_utils.MakeGetRequest(
		t, router, sessionsURL(project.ID, url.Values{"limit": {"1000"}}), "Bearer "+token, http.StatusBadRequest,
	)
}

func setupProjectWithEvents(t *testing.T) (*gin.Engine, *projects_models.Project, string) {
	t.Helper()

	router := createRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("AI usage "+uuid.NewString()[:8], owner.Token, router)

	base := time.Now().UTC().Add(-10 * time.Minute)
	event := func(offset time.Duration, name string, fields map[string]any) *logs_core.LogItem {
		return &logs_core.LogItem{
			ID:        uuid.New(),
			ProjectID: project.ID,
			Timestamp: base.Add(offset),
			Level:     logs_core.LogLevelInfo,
			Message:   "claude_code." + name,
			Fields:    fields,
		}
	}
	sessionA := map[string]any{"session_id": "session-a", "user_email": "a@example.com", "terminal_type": "vscode"}
	sessionB := map[string]any{"session_id": "session-b", "user_email": "b@example.com", "terminal_type": "screen"}
	with := func(base map[string]any, extra map[string]any) map[string]any {
		merged := map[string]any{}
		for k, v := range base {
			merged[k] = v
		}
		for k, v := range extra {
			merged[k] = v
		}
		return merged
	}

	items := []*logs_core.LogItem{
		event(0, "user_prompt", with(sessionA, map[string]any{"prompt": "<REDACTED>", "prompt_length": "12"})),
		event(time.Second, "api_request", with(sessionA, map[string]any{
			"model": "claude-opus-5-5", "cost_usd_micros": "100000", "input_tokens": "500", "output_tokens": "50",
			"cache_read_tokens": "1000", "cache_creation_tokens": "0", "duration_ms": "1000", "ttft_ms": "300",
		})),
		event(2*time.Second, "api_request", with(sessionA, map[string]any{
			"model": "claude-haiku-4-5-20251001", "cost_usd_micros": "100000", "input_tokens": "300",
			"output_tokens": "30", "cache_read_tokens": "0", "cache_creation_tokens": "0", "duration_ms": "500",
		})),
		event(
			3*time.Second,
			"tool_decision",
			with(sessionA, map[string]any{"tool_name": "Bash", "decision": "reject"}),
		),
		event(4*time.Second, "tool_result", with(sessionA, map[string]any{
			"tool_name": "Bash", "success": "true", "duration_ms": "20",
		})),
		event(5*time.Second, "tool_result", with(sessionA, map[string]any{
			"tool_name": "Bash", "success": "false", "duration_ms": "40",
		})),
		event(6*time.Second, "api_request", with(sessionB, map[string]any{
			"model": "claude-opus-5-5", "cost_usd_micros": "400000", "input_tokens": "500", "output_tokens": "50",
			"cache_read_tokens": "2000", "cache_creation_tokens": "0", "duration_ms": "1500",
		})),
		event(7*time.Second, "api_error", with(sessionB, map[string]any{
			"model": "claude-opus-5-5", "status_code": "529", "error": "Overloaded",
		})),
	}

	repository := logs_core.GetAIRepository()
	require.NoError(t, repository.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{project.ID: items}))
	require.NoError(t, repository.ForceFlush())

	return router, project, owner.Token
}

func createRouter() *gin.Engine {
	return projects_testing.CreateTestRouter(
		projects_controllers.GetProjectController(),
		projects_controllers.GetMembershipController(),
		aiusage.GetAIUsageController(),
		aiusage.GetBudgetController(),
		alerting.GetController(),
	)
}

func overviewURL(projectID uuid.UUID, params url.Values) string {
	return withRange(fmt.Sprintf("/api/v1/ai-usage/overview/%s", projectID), params)
}

func eventsURL(projectID uuid.UUID, params url.Values) string {
	return withRange(fmt.Sprintf("/api/v1/ai-usage/events/%s", projectID), params)
}

func sessionsURL(projectID uuid.UUID, params url.Values) string {
	return withRange(fmt.Sprintf("/api/v1/ai-usage/sessions/%s", projectID), params)
}

func withRange(path string, params url.Values) string {
	if params == nil {
		params = url.Values{}
	}
	now := time.Now().UTC()
	params.Set("from", now.Add(-time.Hour).Format(time.RFC3339))
	params.Set("to", now.Add(time.Minute).Format(time.RFC3339))
	return path + "?" + params.Encode()
}
