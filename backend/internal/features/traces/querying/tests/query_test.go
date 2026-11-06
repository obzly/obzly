package traces_querying_tests

import (
	"fmt"
	"net/http"
	"testing"

	spans_core "obzly/internal/features/spans/core"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_ListTraces_ReturnsSeededTrace(t *testing.T) {
	router := CreateTraceQueryTestRouter()
	owner, project, traceID := SetUpProjectWithTrace(t, router, "Traces List")

	var response spans_core.ListTracesResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router,
		fmt.Sprintf("/api/v1/traces/query/list/%s", project.ID.String()),
		"Bearer "+owner.Token, spans_core.ListTracesRequestDTO{}, http.StatusOK, &response,
	)

	require.Len(t, response.Traces, 1)
	assert.Equal(t, traceID, response.Traces[0].TraceID)
	assert.Equal(t, int64(2), response.Traces[0].SpanCount)
	assert.Equal(t, int64(1), response.Traces[0].ErrorCount)
}

func Test_ListTraces_NonMember_ReturnsForbidden(t *testing.T) {
	router := CreateTraceQueryTestRouter()
	_, project, _ := SetUpProjectWithTrace(t, router, "Traces Forbidden")
	outsider := users_testing.CreateTestUser(users_enums.UserRoleMember)

	test_utils.MakePostRequest(
		t, router,
		fmt.Sprintf("/api/v1/traces/query/list/%s", project.ID.String()),
		"Bearer "+outsider.Token, spans_core.ListTracesRequestDTO{}, http.StatusForbidden,
	)
}

func Test_GetTrace_ReturnsAllSpans(t *testing.T) {
	router := CreateTraceQueryTestRouter()
	owner, project, traceID := SetUpProjectWithTrace(t, router, "Traces Get")

	var response spans_core.GetTraceResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t, router,
		fmt.Sprintf("/api/v1/traces/query/get/%s/%s", project.ID.String(), traceID),
		"Bearer "+owner.Token, http.StatusOK, &response,
	)

	require.Len(t, response.Spans, 2)
	assert.Equal(t, "s1", response.Spans[0].SpanID)
}

func Test_ListServices_ReturnsSeededServices(t *testing.T) {
	router := CreateTraceQueryTestRouter()
	owner, project, _ := SetUpProjectWithTrace(t, router, "Traces Services")

	var response spans_core.ListServicesResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t, router,
		fmt.Sprintf("/api/v1/traces/query/services/%s", project.ID.String()),
		"Bearer "+owner.Token, http.StatusOK, &response,
	)

	assert.ElementsMatch(t, []string{"gateway", "payments"}, response.Services)
}

func Test_ListOperations_RequiresServiceParam(t *testing.T) {
	router := CreateTraceQueryTestRouter()
	owner, project, _ := SetUpProjectWithTrace(t, router, "Traces Operations Missing")

	test_utils.MakeGetRequest(
		t, router,
		fmt.Sprintf("/api/v1/traces/query/operations/%s", project.ID.String()),
		"Bearer "+owner.Token, http.StatusBadRequest,
	)
}

func Test_ListOperations_ReturnsOperationsForService(t *testing.T) {
	router := CreateTraceQueryTestRouter()
	owner, project, _ := SetUpProjectWithTrace(t, router, "Traces Operations")

	var response spans_core.ListOperationsResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t, router,
		fmt.Sprintf("/api/v1/traces/query/operations/%s?service=gateway", project.ID.String()),
		"Bearer "+owner.Token, http.StatusOK, &response,
	)

	assert.Equal(t, []string{"GET /checkout"}, response.Operations)
}
