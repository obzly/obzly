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

func Test_GetServiceErrors_CountsEntrySpansPerServiceForBothPeriods(t *testing.T) {
	router := CreateTraceQueryTestRouter()
	now := time.Now().UTC().Truncate(time.Second)
	owner, project := SetUpProjectWithSummarySpans(t, router, "Service Errors Periods", now)

	response := postServiceErrors(t, router, owner.Token, project.ID.String(), serviceErrorsRequest(now, 10*time.Minute), http.StatusOK)

	assert.False(t, response.Clamped)
	assert.True(t, response.PreviousFrom.Equal(now.Add(-20*time.Minute)))
	require.Len(t, response.Services, 2)
	byName := map[string]traces_querying.ServiceErrorsDTO{}
	for _, service := range response.Services {
		byName[service.ServiceName] = service
	}
	assert.Equal(t, traces_querying.ServiceErrorsDTO{
		ServiceName: "gateway", Requests: 4, Errors: 1, PreviousRequests: 2, PreviousErrors: 0,
	}, byName["gateway"])
	assert.Equal(t, traces_querying.ServiceErrorsDTO{
		ServiceName: "payments", Requests: 4, Errors: 1, PreviousRequests: 2, PreviousErrors: 0,
	}, byName["payments"])
}

func Test_GetServiceErrors_Limit_KeepsServicesWithMostErrors(t *testing.T) {
	router := CreateTraceQueryTestRouter()
	now := time.Now().UTC().Truncate(time.Second)
	owner, project := SetUpProjectWithSummarySpans(t, router, "Service Errors Limit", now)

	// The 30-minute-old trace fails in both services; over 40 minutes gateway has 2 errors, payments 2.
	request := serviceErrorsRequest(now, 40*time.Minute)
	request.Limit = 1
	response := postServiceErrors(t, router, owner.Token, project.ID.String(), request, http.StatusOK)

	require.Len(t, response.Services, 1)
	assert.Equal(t, int64(2), response.Services[0].Errors)
	assert.Equal(t, int64(7), response.Services[0].Requests)
}

// The Overview's 30-day range is counted whole: it reads span rollups now,
// not a week of raw spans at most.
func Test_GetServiceErrors_ThirtyDays_IsNotClamped(t *testing.T) {
	router := CreateTraceQueryTestRouter()
	now := time.Now().UTC().Truncate(time.Second)
	owner, project := SetUpProjectWithSummarySpans(t, router, "Service Errors Thirty Days", now)

	response := postServiceErrors(t, router, owner.Token, project.ID.String(), serviceErrorsRequest(now, 30*24*time.Hour), http.StatusOK)

	assert.False(t, response.Clamped)
	assert.True(t, response.From.Equal(now.Add(-30*24*time.Hour)))
	assert.True(t, response.PreviousFrom.Equal(now.Add(-60*24*time.Hour)))
	require.Len(t, response.Services, 2)
}

func Test_GetServiceErrors_RangeOver31Days_IsClampedToTheLast31(t *testing.T) {
	router := CreateTraceQueryTestRouter()
	now := time.Now().UTC().Truncate(time.Second)
	owner, project := SetUpProjectWithSummarySpans(t, router, "Service Errors Clamp", now)

	response := postServiceErrors(t, router, owner.Token, project.ID.String(), serviceErrorsRequest(now, 90*24*time.Hour), http.StatusOK)

	assert.True(t, response.Clamped)
	assert.True(t, response.From.Equal(now.Add(-31*24*time.Hour)))
	assert.True(t, response.PreviousFrom.Equal(now.Add(-62*24*time.Hour)))
	require.Len(t, response.Services, 2)
}

func Test_GetServiceErrors_NoSpans_ReturnsEmptyList(t *testing.T) {
	router := CreateTraceQueryTestRouter()
	now := time.Now().UTC().Truncate(time.Second)
	owner, project := SetUpProjectWithSummarySpans(t, router, "Service Errors Empty", now)

	// A range a day ago, before any seeded span.
	request := serviceErrorsRequest(now.Add(-24*time.Hour), 10*time.Minute)
	response := postServiceErrors(t, router, owner.Token, project.ID.String(), request, http.StatusOK)

	assert.NotNil(t, response.Services)
	assert.Empty(t, response.Services)
}

func Test_GetServiceErrors_MissingRange_ReturnsBadRequest(t *testing.T) {
	router := CreateTraceQueryTestRouter()
	now := time.Now().UTC()
	owner, project := SetUpProjectWithSummarySpans(t, router, "Service Errors Bad Range", now)

	test_utils.MakePostRequest(
		t, router,
		fmt.Sprintf("/api/v1/traces/query/service-errors/%s", project.ID.String()),
		"Bearer "+owner.Token, traces_querying.ServiceErrorsRequestDTO{}, http.StatusBadRequest,
	)
}

func Test_GetServiceErrors_NonMember_ReturnsForbidden(t *testing.T) {
	router := CreateTraceQueryTestRouter()
	now := time.Now().UTC()
	_, project := SetUpProjectWithSummarySpans(t, router, "Service Errors Forbidden", now)
	outsider := users_testing.CreateTestUser(users_enums.UserRoleMember)

	test_utils.MakePostRequest(
		t, router,
		fmt.Sprintf("/api/v1/traces/query/service-errors/%s", project.ID.String()),
		"Bearer "+outsider.Token, serviceErrorsRequest(now, 10*time.Minute), http.StatusForbidden,
	)
}

func serviceErrorsRequest(now time.Time, window time.Duration) traces_querying.ServiceErrorsRequestDTO {
	from := now.Add(-window)
	return traces_querying.ServiceErrorsRequestDTO{
		TimeRange: &spans_core.TimeRangeDTO{From: &from, To: &now},
	}
}

func postServiceErrors(
	t *testing.T,
	router *gin.Engine,
	token, projectID string,
	request traces_querying.ServiceErrorsRequestDTO,
	status int,
) traces_querying.ServiceErrorsResponseDTO {
	t.Helper()
	var response traces_querying.ServiceErrorsResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router,
		fmt.Sprintf("/api/v1/traces/query/service-errors/%s", projectID),
		"Bearer "+token, request, status, &response,
	)
	return response
}
