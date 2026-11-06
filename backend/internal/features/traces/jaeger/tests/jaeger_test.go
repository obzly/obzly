package traces_jaeger_tests

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	traces_jaeger "obzly/internal/features/traces/jaeger"
	test_utils "obzly/internal/util/testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
