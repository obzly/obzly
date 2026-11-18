package logs_receiving_tests

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"obzly/internal/features/ingestion"
	logs_receiving "obzly/internal/features/logs/receiving"
	projects_testing "obzly/internal/features/projects/testing"
	system_metrics "obzly/internal/features/system/metrics"
	users_enums "obzly/internal/features/users/enums"
	users_middleware "obzly/internal/features/users/middleware"
	users_services "obzly/internal/features/users/services"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_SubmitLogs_WithValidData_LogsAccepted(t *testing.T) {
	users_testing.CleanupPlans()
	router := CreateLogsTestRouter()
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	uniqueID := uuid.New().String()
	projectName := fmt.Sprintf("Valid Data Test %s", uniqueID[:8])
	project := projects_testing.CreateTestProject(projectName, user, router)

	logItems := CreateValidLogItems(1, uniqueID)
	request := &logs_receiving.SubmitLogsRequestDTO{
		Logs: logItems,
	}

	var response logs_receiving.SubmitLogsResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t,
		router,
		fmt.Sprintf("/api/v1/logs/receiving/%s", project.ID.String()),
		"",
		request,
		http.StatusAccepted,
		&response,
	)

	assert.Equal(t, 1, response.Accepted)
	assert.Equal(t, 0, response.Rejected)
	assert.Empty(t, response.Errors)
}

func Test_SubmitLogs_WithValidData_RecordsIngestionRate(t *testing.T) {
	users_testing.CleanupPlans()
	router := CreateLogsTestRouter()
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	uniqueID := uuid.New().String()
	projectName := fmt.Sprintf("Ingestion Rate Test %s", uniqueID[:8])
	project := projects_testing.CreateTestProject(projectName, user, router)

	before, err := system_metrics.GetIngestionRateLastNMinutes(1)
	require.NoError(t, err)

	logItems := CreateValidLogItems(3, uniqueID)
	request := &logs_receiving.SubmitLogsRequestDTO{Logs: logItems}

	var response logs_receiving.SubmitLogsResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t,
		router,
		fmt.Sprintf("/api/v1/logs/receiving/%s", project.ID.String()),
		"",
		request,
		http.StatusAccepted,
		&response,
	)
	require.Equal(t, 3, response.Accepted)

	after, err := system_metrics.GetIngestionRateLastNMinutes(1)
	require.NoError(t, err)

	// GreaterOrEqual: the ingestion rate is system-wide, and other packages'
	// tests share the same Valkey concurrently.
	assert.GreaterOrEqual(t, after.TotalCount-before.TotalCount, int64(3),
		"a real accepted SubmitLogs call should be reflected in the ingestion-rate counters")
	assert.Positive(t, after.TotalRawBytes-before.TotalRawBytes)
}

func Test_SubmitLogs_WithValidData_ShowsTheSourceInIngestionStatus(t *testing.T) {
	users_testing.CleanupPlans()
	router := CreateLogsTestRouter()
	ingestion.GetController().RegisterRoutes(router.Group("/api/v1",
		users_middleware.AuthMiddleware(users_services.GetUserService())))
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	uniqueID := uuid.New().String()
	project := projects_testing.CreateTestProject(fmt.Sprintf("Ingestion Status Test %s", uniqueID[:8]), user, router)

	logItems := CreateValidLogItems(3, uniqueID)
	for i := range logItems {
		logItems[i].Fields["resource.service.name"] = "checkout"
		logItems[i].Fields["resource.host.name"] = "vm-" + uniqueID[:8]
	}
	test_utils.MakePostRequest(t, router, fmt.Sprintf("/api/v1/logs/receiving/%s", project.ID.String()), "",
		&logs_receiving.SubmitLogsRequestDTO{Logs: logItems}, http.StatusAccepted)
	require.NoError(t, ingestion.GetTracker().Flush(context.Background()))

	var status ingestion.IngestionStatusDTO
	test_utils.MakeGetRequestAndUnmarshal(t, router,
		fmt.Sprintf("/api/v1/projects/%s/ingestion-status", project.ID.String()), "Bearer "+user.Token,
		http.StatusOK, &status)
	require.Len(t, status.Sources, 1)
	assert.Equal(t, "checkout", status.Sources[0].Name)
	assert.Equal(t, "vm-"+uniqueID[:8], status.Sources[0].Location.Host)
	require.Len(t, status.Sources[0].Signals, 1)
	assert.Equal(t, int64(3), status.Sources[0].Signals[0].RecentCount)
}

func Test_SubmitLogs_WithMultipleLogs_AllLogsAccepted(t *testing.T) {
	users_testing.CleanupPlans()
	router := CreateLogsTestRouter()
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	uniqueID := uuid.New().String()
	projectName := fmt.Sprintf("Multiple Logs Test %s", uniqueID[:8])
	project := projects_testing.CreateTestProject(projectName, user, router)

	logCount := 5
	logItems := CreateValidLogItems(logCount, uniqueID)
	request := &logs_receiving.SubmitLogsRequestDTO{
		Logs: logItems,
	}

	var response logs_receiving.SubmitLogsResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t,
		router,
		fmt.Sprintf("/api/v1/logs/receiving/%s", project.ID.String()),
		"",
		request,
		http.StatusAccepted,
		&response,
	)

	assert.Equal(t, logCount, response.Accepted)
	assert.Equal(t, 0, response.Rejected)
	assert.Empty(t, response.Errors)
}

func Test_SubmitLogs_WithEmptyBatch_ReturnsBadRequest(t *testing.T) {
	users_testing.CleanupPlans()
	router := CreateLogsTestRouter()
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	uniqueID := uuid.New().String()
	projectName := fmt.Sprintf("Empty Batch Test %s", uniqueID[:8])
	project := projects_testing.CreateTestProject(projectName, user, router)

	request := &logs_receiving.SubmitLogsRequestDTO{
		Logs: []logs_receiving.LogItemRequestDTO{},
	}

	resp := test_utils.MakePostRequest(
		t,
		router,
		fmt.Sprintf("/api/v1/logs/receiving/%s", project.ID.String()),
		"",
		request,
		http.StatusBadRequest,
	)

	assert.Contains(t, string(resp.Body), "Invalid request format")
}

func Test_SubmitLogs_WithInvalidProjectID_ReturnsBadRequest(t *testing.T) {
	users_testing.CleanupPlans()
	router := CreateLogsTestRouter()
	uniqueID := uuid.New().String()

	logItems := CreateValidLogItems(1, uniqueID)
	request := &logs_receiving.SubmitLogsRequestDTO{
		Logs: logItems,
	}

	resp := test_utils.MakePostRequest(
		t,
		router,
		"/api/v1/logs/receiving/invalid-uuid",
		"",
		request,
		http.StatusBadRequest,
	)

	assert.Contains(t, string(resp.Body), "Invalid project ID")
}

func Test_SubmitLogs_WithNonExistentProject_ReturnsNotFound(t *testing.T) {
	users_testing.CleanupPlans()
	router := CreateLogsTestRouter()
	uniqueID := uuid.New().String()
	nonExistentProjectID := uuid.New()

	logItems := CreateValidLogItems(1, uniqueID)
	request := &logs_receiving.SubmitLogsRequestDTO{
		Logs: logItems,
	}

	resp := test_utils.MakePostRequest(
		t,
		router,
		fmt.Sprintf("/api/v1/logs/receiving/%s", nonExistentProjectID.String()),
		"",
		request,
		http.StatusNotFound,
	)

	assert.Contains(t, string(resp.Body), "project not found")
}
