package traces_querying_tests

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"obzly/internal/config"
	spans_core "obzly/internal/features/spans/core"
	spans_testsupport "obzly/internal/features/spans/testsupport"
	"obzly/internal/query"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requestMark marks a test request's context, to tell it from
// context.Background().
type requestMark struct{}

// The service and operation pickers send no range. They read the last 31
// days, under the scan budget and the request's context: they used to read
// from 1970 on with neither, so a loop of them tied up storage unchecked.
func Test_ListServicesAndOperations_WithoutARange_AreBoundedLikeTheSearches(t *testing.T) {
	router := CreateTraceQueryTestRouter()
	owner, project, _ := SetUpProjectWithTrace(t, router, "Traces Lookups Bounded")
	recorder := spans_testsupport.RecordLookups(t)
	before := time.Now().UTC()

	for _, path := range []string{"services/%s", "operations/%s?service=gateway"} {
		response := getWithMarkedContext(t, router,
			"/api/v1/traces/query/"+fmt.Sprintf(path, project.ID.String()), owner.Token)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	}

	calls := recorder.Calls()
	require.Len(t, calls, 2)
	assert.Equal(t, "ListServices", calls[0].Method)
	assert.Equal(t, "ListOperations", calls[1].Method)
	for _, call := range calls {
		require.NotNil(t, call.TimeRange, call.Method)
		require.NotNil(t, call.TimeRange.From, call.Method)
		assert.Nil(t, call.TimeRange.To, "%s: a span stamped ahead of this clock is still found", call.Method)
		assert.WithinDuration(t, before.AddDate(0, 0, -31), *call.TimeRange.From, time.Minute, call.Method)
		assert.Equal(t, config.GetEnv().QueryMaxScanBytes, call.ScanBudget, call.Method)
		assert.Equal(t, true, call.Ctx.Value(requestMark{}), "%s runs under the request's context", call.Method)
	}
}

// A range longer than 31 days keeps its newest 31; a shorter one is kept.
func Test_ListServices_WithARange_ReadsAtMost31Days(t *testing.T) {
	router := CreateTraceQueryTestRouter()
	owner, project, _ := SetUpProjectWithTrace(t, router, "Traces Lookups Range")
	recorder := spans_testsupport.RecordLookups(t)
	to := time.Now().UTC().Add(time.Minute).Truncate(time.Second)

	for _, days := range []int{90, 7} {
		var response spans_core.ListServicesResponseDTO
		test_utils.MakeGetRequestAndUnmarshal(t, router,
			fmt.Sprintf("/api/v1/traces/query/services/%s?from=%s&to=%s", project.ID.String(),
				to.AddDate(0, 0, -days).Format(time.RFC3339), to.Format(time.RFC3339)),
			"Bearer "+owner.Token, http.StatusOK, &response)
		assert.ElementsMatch(t, []string{"gateway", "payments"}, response.Services)
	}

	calls := recorder.Calls()
	require.Len(t, calls, 2)
	assert.True(t, to.Equal(*calls[0].TimeRange.To))
	assert.True(t, to.AddDate(0, 0, -31).Equal(*calls[0].TimeRange.From), "90 days are clamped to 31")
	assert.True(t, to.AddDate(0, 0, -7).Equal(*calls[1].TimeRange.From), "7 days are kept")
}

// A service whose only spans are older than the default range isn't listed
// without a range, and is with one that reaches it.
func Test_ListServices_WithoutARange_LeavesOutServicesLastSeenOver31DaysAgo(t *testing.T) {
	router := CreateTraceQueryTestRouter()
	owner, project, _ := SetUpProjectWithTrace(t, router, "Traces Lookups Old Service")
	old := time.Now().UTC().AddDate(0, 0, -40).Truncate(time.Millisecond)
	require.NoError(t, spans_core.GetSpanCoreRepository().StoreSpansBatch(map[uuid.UUID][]*spans_core.Span{
		project.ID: {{
			ID: uuid.New(), ProjectID: project.ID, TraceID: uuid.NewString(), SpanID: "old",
			Name: "GET /legacy", ServiceName: "legacy-billing",
			StartTime: old, EndTime: old.Add(time.Millisecond), StatusCode: spans_core.StatusCodeOK,
		}},
	}))
	require.NoError(t, spans_core.GetSpanCoreRepository().ForceFlush())
	url := fmt.Sprintf("/api/v1/traces/query/services/%s", project.ID.String())

	var recent, reaching spans_core.ListServicesResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(t, router, url, "Bearer "+owner.Token, http.StatusOK, &recent)
	test_utils.MakeGetRequestAndUnmarshal(t, router,
		fmt.Sprintf("%s?from=%s&to=%s", url, old.Add(-time.Hour).Format(time.RFC3339),
			old.Add(time.Hour).Format(time.RFC3339)),
		"Bearer "+owner.Token, http.StatusOK, &reaching)

	assert.ElementsMatch(t, []string{"gateway", "payments"}, recent.Services)
	assert.Equal(t, []string{"legacy-billing"}, reaching.Services)
}

// A lookup planned past the scan budget is refused (422), as a search is.
func Test_ListServicesAndOperations_PastTheScanBudget_AreRefused(t *testing.T) {
	router := CreateTraceQueryTestRouter()
	owner, project, _ := SetUpProjectWithTrace(t, router, "Traces Lookups Budget")
	recorder := spans_testsupport.RecordLookups(t)
	recorder.Err = fmt.Errorf("failed to list services: %w", &query.ScanBudgetError{Planned: 2 << 30, Budget: 1 << 30})

	for _, path := range []string{"services/%s", "operations/%s?service=gateway"} {
		response := test_utils.MakeGetRequest(t, router,
			"/api/v1/traces/query/"+fmt.Sprintf(path, project.ID.String()),
			"Bearer "+owner.Token, http.StatusUnprocessableEntity)
		assert.Contains(t, string(response.Body), "QUERY_TOO_LARGE", path)
	}
}

// getWithMarkedContext is a GET whose request context carries requestMark,
// so a test can tell that context from context.Background().
func getWithMarkedContext(t *testing.T, router *gin.Engine, url, token string) *httptest.ResponseRecorder {
	t.Helper()
	request, err := http.NewRequestWithContext(
		context.WithValue(context.Background(), requestMark{}, true), http.MethodGet, url, nil)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+token)
	request.RemoteAddr = "10.20.30.40:50000"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}
