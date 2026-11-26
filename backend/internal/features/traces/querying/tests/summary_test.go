package traces_querying_tests

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	spans_core "obzly/internal/features/spans/core"
	traces_querying "obzly/internal/features/traces/querying"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_GetTraceSummary_RootSpans_ReturnsCurrentPreviousTrendsAndCount(t *testing.T) {
	router := CreateTraceQueryTestRouter()
	now := time.Now().UTC().Truncate(time.Second)
	owner, project := SetUpProjectWithSummarySpans(t, router, "Traces Summary Root", now)

	response := postSummary(t, router, owner.Token, project.ID.String(), summaryRequest(now), http.StatusOK)

	assert.True(t, response.RootSpansOnly)
	assert.InDelta(t, 10, response.WindowMinutes, 1e-9)
	require.NotNil(t, response.Current.Count)
	assert.Equal(t, int64(4), *response.Current.Count)
	assert.Equal(t, int64(1), *response.Current.Errors, "only trace 3's root failed")
	require.NotNil(t, response.Current.P50Ms)
	require.NotNil(t, response.Current.P95Ms)
	assert.InDelta(t, 35, *response.Current.P95Ms, 6)

	require.NotNil(t, response.Previous.Count)
	assert.Equal(t, int64(2), *response.Previous.Count)
	assert.Equal(t, int64(0), *response.Previous.Errors)
	require.NotNil(t, response.Previous.P95Ms)
	// Percentiles come from the duration histogram: within ~4.5%.
	assert.InEpsilon(t, 50, *response.Previous.P95Ms, 0.05)

	// 10 minutes in 20 s steps: 30 steps, 31 slots over the aligned range.
	assert.Equal(t, int64(20_000), response.StepMs)
	assert.Len(t, response.CountTrend, 31)
	assert.Equal(t, 4.0, sum(response.CountTrend))
	assert.ElementsMatch(t, []float64{0, 0, 0, 100}, response.ErrorRateTrend)
	assert.Len(t, response.P50Trend, 4)
	assert.Len(t, response.P95Trend, 4)

	assert.Equal(t, int64(4), response.MatchingTraces)
	assert.False(t, response.MatchingTracesCapped)
}

func Test_GetTraceSummary_ServicePicked_CountsThatServicesSpansAndItsTraces(t *testing.T) {
	router := CreateTraceQueryTestRouter()
	now := time.Now().UTC().Truncate(time.Second)
	owner, project := SetUpProjectWithSummarySpans(t, router, "Traces Summary Service", now)

	request := summaryRequest(now)
	request.ServiceName = "payments"
	response := postSummary(t, router, owner.Token, project.ID.String(), request, http.StatusOK)

	assert.False(t, response.RootSpansOnly)
	assert.Equal(t, int64(4), *response.Current.Count)
	assert.Equal(t, int64(1), *response.Current.Errors, "trace 1's payments span failed")
	assert.Equal(t, int64(2), *response.Previous.Count)
	assert.Equal(t, int64(4), response.MatchingTraces, "every current trace has a payments span")
}

func Test_GetTraceSummary_ErrorsOnly_CountsTracesWithAnyErrorSpan(t *testing.T) {
	router := CreateTraceQueryTestRouter()
	now := time.Now().UTC().Truncate(time.Second)
	owner, project := SetUpProjectWithSummarySpans(t, router, "Traces Summary Errors", now)

	request := summaryRequest(now)
	request.ErrorsOnly = true
	response := postSummary(t, router, owner.Token, project.ID.String(), request, http.StatusOK)

	// As in trace search: trace 1 (a failed child) and trace 3 (a failed root).
	assert.Equal(t, int64(2), response.MatchingTraces)
	// The tiles count failed root spans only.
	assert.Equal(t, int64(1), *response.Current.Count)
	assert.Equal(t, int64(0), *response.Previous.Count)
	assert.Nil(t, response.Previous.P95Ms, "no spans: unknown, not 0")
}

func Test_GetTraceSummary_MinDuration_FiltersTilesAndCount(t *testing.T) {
	router := CreateTraceQueryTestRouter()
	now := time.Now().UTC().Truncate(time.Second)
	owner, project := SetUpProjectWithSummarySpans(t, router, "Traces Summary Duration", now)

	request := summaryRequest(now)
	minDuration := 25.0
	request.MinDurationMs = &minDuration
	response := postSummary(t, router, owner.Token, project.ID.String(), request, http.StatusOK)

	assert.Equal(t, int64(2), response.MatchingTraces)
	assert.Equal(t, int64(2), *response.Current.Count)
	assert.Equal(t, int64(2), *response.Previous.Count, "the 50 ms roots pass too")
}

func Test_GetTraceSummary_CountOnly_SkipsTiles(t *testing.T) {
	router := CreateTraceQueryTestRouter()
	now := time.Now().UTC().Truncate(time.Second)
	owner, project := SetUpProjectWithSummarySpans(t, router, "Traces Summary Count Only", now)

	request := summaryRequest(now)
	request.CountOnly = true
	response := postSummary(t, router, owner.Token, project.ID.String(), request, http.StatusOK)

	assert.Equal(t, int64(4), response.MatchingTraces)
	assert.Nil(t, response.Current.Count)
	assert.Empty(t, response.CountTrend)
}

func Test_GetTraceSummary_MoreMatchesThanCap_ReturnsCappedCount(t *testing.T) {
	router := CreateTraceQueryTestRouter()
	now := time.Now().UTC().Truncate(time.Second)
	owner, project := SetUpProjectWithManyRootSpans(t, router, "Traces Summary Capped", now, 10_005)

	response := postSummary(t, router, owner.Token, project.ID.String(), summaryRequest(now), http.StatusOK)

	assert.True(t, response.MatchingTracesCapped)
	assert.Equal(t, int64(10_000), response.MatchingTraces)
	assert.Equal(t, int64(10_000), response.MatchingTracesCap)
	assert.Equal(t, int64(10_005), *response.Current.Count, "the tiles count every span")
}

// A day's trend is 24 hourly points: whole hours are what span rollups
// (spans_1h) answer.
func Test_GetTraceSummary_DayWindow_StepsInWholeHours(t *testing.T) {
	router := CreateTraceQueryTestRouter()
	now := time.Now().UTC().Truncate(time.Second)
	owner, project := SetUpProjectWithSummarySpans(t, router, "Traces Summary Day", now)

	request := summaryRequest(now)
	from := now.Add(-24 * time.Hour)
	request.TimeRange.From = &from
	response := postSummary(t, router, owner.Token, project.ID.String(), request, http.StatusOK)

	assert.Equal(t, int64(3_600_000), response.StepMs)
	assert.Len(t, response.CountTrend, 25)
	assert.Equal(t, 7.0, sum(response.CountTrend), "every root span of the day")
}

func Test_GetTraceSummary_MissingRange_ReturnsBadRequest(t *testing.T) {
	router := CreateTraceQueryTestRouter()
	now := time.Now().UTC()
	owner, project := SetUpProjectWithSummarySpans(t, router, "Traces Summary Bad Range", now)

	test_utils.MakePostRequest(
		t, router,
		fmt.Sprintf("/api/v1/traces/query/summary/%s", project.ID.String()),
		"Bearer "+owner.Token, traces_querying.TraceSummaryRequestDTO{}, http.StatusBadRequest,
	)
}

func Test_GetTraceSummary_NonMember_ReturnsForbidden(t *testing.T) {
	router := CreateTraceQueryTestRouter()
	now := time.Now().UTC()
	_, project := SetUpProjectWithSummarySpans(t, router, "Traces Summary Forbidden", now)
	outsider := users_testing.CreateTestUser(users_enums.UserRoleMember)

	test_utils.MakePostRequest(
		t, router,
		fmt.Sprintf("/api/v1/traces/query/summary/%s", project.ID.String()),
		"Bearer "+outsider.Token, summaryRequest(now), http.StatusForbidden,
	)
}

func summaryRequest(now time.Time) traces_querying.TraceSummaryRequestDTO {
	from := now.Add(-10 * time.Minute)
	return traces_querying.TraceSummaryRequestDTO{
		TimeRange: &spans_core.TimeRangeDTO{From: &from, To: &now},
	}
}

func postSummary(
	t *testing.T,
	router *gin.Engine,
	token, projectID string,
	request traces_querying.TraceSummaryRequestDTO,
	status int,
) traces_querying.TraceSummaryResponseDTO {
	t.Helper()
	var response traces_querying.TraceSummaryResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router,
		fmt.Sprintf("/api/v1/traces/query/summary/%s", projectID),
		"Bearer "+token, request, status, &response,
	)
	return response
}

func sum(values []float64) float64 {
	total := 0.0
	for _, value := range values {
		total += value
	}
	return total
}
