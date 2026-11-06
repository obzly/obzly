package aiusage_tests

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"obzly/internal/features/aiusage"
	projects_models "obzly/internal/features/projects/models"
	projects_testing "obzly/internal/features/projects/testing"
	spans_core "obzly/internal/features/spans/core"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const secretContent = "CONTOSO-SECRET-PROMPT"

func Test_GetLLMOverview_WithNewAndOldAttributeNames_AggregatesCallsAndCost(t *testing.T) {
	router, project, token := setupProjectWithLLMSpans(t)

	var response aiusage.LLMOverviewResponseDTO
	body := test_utils.MakeGetRequestAndUnmarshal(t, router, llmURL("overview", project.ID, nil),
		"Bearer "+token, http.StatusOK, &response)

	summary := response.Summary
	assert.Equal(t, int64(4), summary.Calls, "the span without gen_ai.operation.name isn't a call")
	assert.Equal(t, int64(2), summary.Errors, "error.type and an ERROR status both count")
	assert.Equal(t, int64(1600), summary.InputTokens, "prompt_tokens counts as input")
	assert.Equal(t, int64(260), summary.OutputTokens, "completion_tokens counts as output")
	assert.Equal(t, int64(400), summary.CachedTokens)
	assert.Equal(t, int64(3), summary.Services)
	assert.InDelta(t, 0.00492+0.00028, summary.CostUSD, 1e-9)
	assert.Equal(t, []string{"mistral-large-latest"}, summary.UnpricedModels)
	assert.NotZero(t, response.LatestCallAt)
	assert.NotEmpty(t, response.Timeseries)

	byKey := map[string]aiusage.LLMBreakdownRowDTO{}
	for _, row := range response.Breakdown {
		byKey[row.Key] = row
	}
	require.Len(t, byKey, 3)
	summarizer := byKey["doc-summarizer"]
	assert.Equal(t, int64(2), summarizer.Calls)
	assert.Equal(t, int64(2), summarizer.Errors)
	require.NotNil(t, summarizer.CostUSD)
	assert.InDelta(t, 0.00028, *summarizer.CostUSD, 1e-9)
	assert.InDelta(t, 5000, summarizer.P95Ms, 2000)
	rerank := byKey["search-rerank"]
	assert.Nil(t, rerank.CostUSD, "no price set")
	assert.True(t, rerank.Unpriced)
	assert.True(t, rerank.HasTokens)

	assert.NotContains(t, string(body.Body), secretContent)
}

func Test_GetLLMOverview_GroupedByProvider_ReadsOldSystemAttribute(t *testing.T) {
	router, project, token := setupProjectWithLLMSpans(t)

	var response aiusage.LLMOverviewResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t,
		router,
		llmURL("overview", project.ID, url.Values{"groupBy": {"provider"}}),
		"Bearer "+token,
		http.StatusOK,
		&response,
	)

	keys := map[string]int64{}
	for _, row := range response.Breakdown {
		keys[row.Key] = row.Calls
	}
	assert.Equal(t, int64(1), keys["anthropic"])
	assert.Equal(t, int64(1), keys["openai"], "from gen_ai.system")
	assert.Equal(t, int64(1), keys["mistral_ai"])
	assert.Equal(t, int64(1), keys[""], "a tool span has no provider")
}

func Test_GetLLMOverview_WithUnknownGroup_ReturnsBadRequest(t *testing.T) {
	router, project, token := setupProjectWithLLMSpans(t)

	test_utils.MakeGetRequest(t, router, llmURL("overview", project.ID, url.Values{"groupBy": {"prompt"}}),
		"Bearer "+token, http.StatusBadRequest)
}

func Test_GetLLMCalls_ReturnsSlowestAndFailedWithTraceIDsAndNoContent(t *testing.T) {
	router, project, token := setupProjectWithLLMSpans(t)

	var response aiusage.LLMCallsResponseDTO
	body := test_utils.MakeGetRequestAndUnmarshal(t, router, llmURL("calls", project.ID, url.Values{"limit": {"2"}}),
		"Bearer "+token, http.StatusOK, &response)

	require.Len(t, response.Slowest, 2)
	assert.Equal(t, "doc-summarizer", response.Slowest[0].Service)
	assert.InDelta(t, 5000, response.Slowest[0].DurationMs, 1)
	assert.Equal(t, "timeout", response.Slowest[0].ErrorType)
	assert.Equal(t, []string{"length"}, response.Slowest[0].FinishReasons)
	assert.NotEmpty(t, response.Slowest[0].TraceID)
	require.Len(t, response.Failed, 2)
	for _, call := range response.Failed {
		assert.True(t, call.IsError)
	}
	assert.NotContains(t, string(body.Body), secretContent)
}

func Test_GetLLMOverview_WhenUserIsNotProjectMember_ReturnsForbidden(t *testing.T) {
	router, project, _ := setupProjectWithLLMSpans(t)
	outsider := users_testing.CreateTestUser(users_enums.UserRoleMember)

	test_utils.MakeGetRequest(t, router, llmURL("overview", project.ID, nil), "Bearer "+outsider.Token,
		http.StatusForbidden)
}

func Test_ListModelPrices_CountsCallsSeenPerPriceAndListsUnpricedModels(t *testing.T) {
	router, project, token := setupProjectWithLLMSpans(t)

	var response aiusage.ModelPricesResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/projects/"+project.ID.String()+"/ai-model-prices",
		"Bearer "+token, http.StatusOK, &response)

	require.Len(t, response.Prices, 2)
	seen := map[string]int64{}
	for _, price := range response.Prices {
		seen[price.Model] = price.SeenCalls
	}
	assert.Equal(t, int64(1), seen["claude-sonnet-4-5*"])
	assert.Equal(t, int64(1), seen["gpt-4.1-mini*"])
	require.Len(t, response.UnpricedModels, 1)
	assert.Equal(t, "mistral-large-latest", response.UnpricedModels[0].Model)
}

func setupProjectWithLLMSpans(t *testing.T) (*gin.Engine, *projects_models.Project, string) {
	t.Helper()

	router := createRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("LLM apps "+uuid.NewString()[:8], owner.Token, router)
	seedLLMSpans(t, project.ID, time.Now().UTC().Add(-10*time.Minute))

	for _, price := range []aiusage.ModelPriceRequest{
		{Model: "claude-sonnet-4-5*", InputPerMillion: 3, OutputPerMillion: 15, CachedInputPerMillion: ptr(0.3)},
		{Model: "gpt-4.1-mini*", InputPerMillion: 0.4, OutputPerMillion: 1.6},
	} {
		test_utils.MakePostRequest(t, router, "/api/v1/projects/"+project.ID.String()+"/ai-model-prices",
			"Bearer "+owner.Token, price, http.StatusOK)
	}
	return router, project, owner.Token
}

// seedLLMSpans stores four LLM calls (current and older gen_ai attribute
// names, an error, a tool call, an unpriced model) and one plain span.
func seedLLMSpans(t *testing.T, projectID uuid.UUID, base time.Time) {
	t.Helper()
	span := func(offset, duration time.Duration, service string, status spans_core.StatusCode,
		attributes map[string]any) *spans_core.Span {
		return &spans_core.Span{
			ID: uuid.New(), ProjectID: projectID, TraceID: uuid.NewString()[:32], SpanID: uuid.NewString()[:16],
			Name: "llm", ServiceName: service, Kind: spans_core.SpanKindClient, StartTime: base.Add(offset),
			EndTime: base.Add(offset + duration), StatusCode: status, Attributes: attributes,
		}
	}
	spans := []*spans_core.Span{
		span(0, 2*time.Second, "support-bot", spans_core.StatusCodeOK, map[string]any{
			"gen_ai.operation.name": "chat", "gen_ai.provider.name": "anthropic",
			"gen_ai.request.model": "claude-sonnet-4-5", "gen_ai.response.model": "claude-sonnet-4-5-20250929",
			"gen_ai.usage.input_tokens": int64(1000), "gen_ai.usage.output_tokens": int64(200),
			"gen_ai.usage.cache_read.input_tokens": int64(400), "user.id": "a.rivera@example.com",
			"gen_ai.input.messages": secretContent,
		}),
		span(time.Second, 5*time.Second, "doc-summarizer", spans_core.StatusCodeUnset, map[string]any{
			"gen_ai.operation.name": "chat", "gen_ai.system": "openai", "gen_ai.request.model": "gpt-4.1-mini",
			"gen_ai.usage.prompt_tokens": int64(500), "gen_ai.usage.completion_tokens": int64(50),
			"error.type": "timeout", "gen_ai.response.finish_reasons": []any{"length"},
			"gen_ai.prompt.0.content": secretContent, "user.id": "j.chen@example.com",
		}),
		span(2*time.Second, 300*time.Millisecond, "doc-summarizer", spans_core.StatusCodeError, map[string]any{
			"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": "search",
		}),
		span(3*time.Second, time.Second, "search-rerank", spans_core.StatusCodeOK, map[string]any{
			"gen_ai.operation.name": "chat", "gen_ai.provider.name": "mistral_ai",
			"gen_ai.request.model": "mistral-large-latest", "gen_ai.usage.input_tokens": int64(100),
			"gen_ai.usage.output_tokens": int64(10),
		}),
		span(4*time.Second, time.Second, "checkout", spans_core.StatusCodeOK, map[string]any{"http.route": "/pay"}),
	}
	repository := spans_core.GetSpanCoreRepository()
	require.NoError(t, repository.StoreSpansBatch(map[uuid.UUID][]*spans_core.Span{projectID: spans}))
	require.NoError(t, repository.ForceFlush())
}

func llmURL(endpoint string, projectID uuid.UUID, params url.Values) string {
	return withRange(fmt.Sprintf("/api/v1/ai-usage/llm/%s/%s", endpoint, projectID), params)
}

func ptr(value float64) *float64 {
	return &value
}
