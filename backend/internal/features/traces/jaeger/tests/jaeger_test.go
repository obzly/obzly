package traces_jaeger_tests

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"obzly/internal/config"
	spans_testsupport "obzly/internal/features/spans/testsupport"
	traces_jaeger "obzly/internal/features/traces/jaeger"
	"obzly/internal/query"
	test_utils "obzly/internal/util/testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requestMark marks a test request's context, to tell it from
// context.Background().
type requestMark struct{}

func jaegerGet(t *testing.T, fixture *TestFixture, path, apiKey string, expectedStatus int) *traces_jaeger.Response {
	url := fmt.Sprintf("/api/v1/jaeger/%s/api/%s", fixture.Project.ID.String(), path)

	headers := map[string]string{}
	if apiKey != "" {
		headers["X-API-Key"] = apiKey
	}

	resp := test_utils.MakeRequest(t, fixture.Router, test_utils.RequestOptions{
		Method:         "GET",
		URL:            url,
		Headers:        headers,
		ExpectedStatus: expectedStatus,
	})

	if expectedStatus != http.StatusOK {
		return nil
	}

	var response traces_jaeger.Response
	require.NoError(t, json.Unmarshal(resp.Body, &response))
	return &response
}

func Test_JaegerListServices_ReturnsSeededServices(t *testing.T) {
	fixture := SetUpFixtureWithTrace(t, "Jaeger Services")

	response := jaegerGet(t, fixture, "services", fixture.ApiKey.Token, http.StatusOK)
	require.NotNil(t, response)

	services, ok := response.Data.([]any)
	require.True(t, ok)
	assert.ElementsMatch(t, []any{"gateway", "payments"}, services)
}

func Test_JaegerListServices_WithoutApiKey_Unauthorized(t *testing.T) {
	fixture := SetUpFixtureWithTrace(t, "Jaeger Services No Key")
	jaegerGet(t, fixture, "services", "", http.StatusUnauthorized)
}

func Test_JaegerListServices_WithApiKeyFromAnotherProject_Unauthorized(t *testing.T) {
	fixture := SetUpFixtureWithTrace(t, "Jaeger Services Wrong Project")
	otherFixture := SetUpFixtureWithTrace(t, "Jaeger Services Other")

	jaegerGet(t, fixture, "services", otherFixture.ApiKey.Token, http.StatusUnauthorized)
}

func Test_JaegerListOperations_ReturnsOperationsForService(t *testing.T) {
	fixture := SetUpFixtureWithTrace(t, "Jaeger Operations")

	response := jaegerGet(t, fixture, "services/gateway/operations", fixture.ApiKey.Token, http.StatusOK)
	require.NotNil(t, response)

	operations, ok := response.Data.([]any)
	require.True(t, ok)
	assert.Equal(t, []any{"GET /checkout"}, operations)
}

func Test_JaegerGetTrace_ReturnsFullTraceWithProcesses(t *testing.T) {
	fixture := SetUpFixtureWithTrace(t, "Jaeger GetTrace")

	response := jaegerGet(t, fixture, "traces/"+fixture.TraceID, fixture.ApiKey.Token, http.StatusOK)
	require.NotNil(t, response)

	traces, ok := response.Data.([]any)
	require.True(t, ok)
	require.Len(t, traces, 1)

	trace, ok := traces[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, fixture.TraceID, trace["traceID"])

	spans, ok := trace["spans"].([]any)
	require.True(t, ok)
	require.Len(t, spans, 2)

	processes, ok := trace["processes"].(map[string]any)
	require.True(t, ok)
	assert.Len(t, processes, 2) // gateway + payments

	// The child span must carry a CHILD_OF reference to the root.
	var childSpan map[string]any
	for _, s := range spans {
		span := s.(map[string]any)
		if span["spanID"] == "s2" {
			childSpan = span
		}
	}
	require.NotNil(t, childSpan)
	references, ok := childSpan["references"].([]any)
	require.True(t, ok)
	require.Len(t, references, 1)
	ref := references[0].(map[string]any)
	assert.Equal(t, "CHILD_OF", ref["refType"])
	assert.Equal(t, "s1", ref["spanID"])
}

func Test_JaegerGetTrace_UnknownTraceID_ReturnsNotFound(t *testing.T) {
	fixture := SetUpFixtureWithTrace(t, "Jaeger GetTrace Missing")
	jaegerGet(t, fixture, "traces/does-not-exist", fixture.ApiKey.Token, http.StatusNotFound)
}

func Test_JaegerSearchTraces_FiltersByService(t *testing.T) {
	fixture := SetUpFixtureWithTrace(t, "Jaeger Search")

	response := jaegerGet(t, fixture, "traces?service=payments", fixture.ApiKey.Token, http.StatusOK)
	require.NotNil(t, response)

	traces, ok := response.Data.([]any)
	require.True(t, ok)
	require.Len(t, traces, 1)
	spans, ok := traces[0].(map[string]any)["spans"].([]any)
	require.True(t, ok)
	require.Len(t, spans, 2, "whole traces, fetched together after the search")
}

func Test_JaegerSearchTraces_NoMatch_ReturnsEmptyData(t *testing.T) {
	fixture := SetUpFixtureWithTrace(t, "Jaeger Search No Match")

	response := jaegerGet(t, fixture, "traces?service=does-not-exist", fixture.ApiKey.Token, http.StatusOK)
	require.NotNil(t, response)

	traces, ok := response.Data.([]any)
	require.True(t, ok)
	assert.Empty(t, traces)
}

// Jaeger's services and operations calls carry no range. They read the last
// 31 days, under the scan budget and the request's context: they read from
// 1970 on with neither, as the trace query API's own lookups did.
func Test_JaegerListServicesAndOperations_AreBoundedLikeTheSearches(t *testing.T) {
	fixture := SetUpFixtureWithTrace(t, "Jaeger Lookups Bounded")
	recorder := spans_testsupport.RecordLookups(t)
	before := time.Now().UTC()

	for _, path := range []string{"services", "services/gateway/operations", "operations?service=gateway"} {
		request, err := http.NewRequestWithContext(context.WithValue(context.Background(), requestMark{}, true),
			http.MethodGet, fmt.Sprintf("/api/v1/jaeger/%s/api/%s", fixture.Project.ID.String(), path), nil)
		require.NoError(t, err)
		request.Header.Set("X-API-Key", fixture.ApiKey.Token)
		response := httptest.NewRecorder()
		fixture.Router.ServeHTTP(response, request)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	}

	calls := recorder.Calls()
	require.Len(t, calls, 3)
	for _, call := range calls {
		require.NotNil(t, call.TimeRange, call.Method)
		assert.WithinDuration(t, before.AddDate(0, 0, -31), *call.TimeRange.From, time.Minute, call.Method)
		assert.Nil(t, call.TimeRange.To, call.Method)
		assert.Equal(t, config.GetEnv().QueryMaxScanBytes, call.ScanBudget, call.Method)
		assert.Equal(t, true, call.Ctx.Value(requestMark{}), "%s runs under the request's context", call.Method)
	}
}

// A lookup planned past the scan budget is refused (422), as a search is.
func Test_JaegerListServices_PastTheScanBudget_IsRefused(t *testing.T) {
	fixture := SetUpFixtureWithTrace(t, "Jaeger Lookups Budget")
	recorder := spans_testsupport.RecordLookups(t)
	recorder.Err = &query.ScanBudgetError{Planned: 2 << 30, Budget: 1 << 30}

	jaegerGet(t, fixture, "services", fixture.ApiKey.Token, http.StatusUnprocessableEntity)
	jaegerGet(t, fixture, "services/gateway/operations", fixture.ApiKey.Token, http.StatusUnprocessableEntity)
}
