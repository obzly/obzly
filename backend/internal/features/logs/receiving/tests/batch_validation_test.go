package logs_receiving_tests

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	logs_core "obzly/internal/features/logs/core"
	logs_receiving "obzly/internal/features/logs/receiving"
	projects_models "obzly/internal/features/projects/models"
	projects_testing "obzly/internal/features/projects/testing"
	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func Test_SubmitLogs_WithMaxAllowedBatchSize_LogsAccepted(t *testing.T) {
	users_testing.CleanupPlans()

	testData := setupBatchTest("Max Allowed Batch Size Test")

	maxBatchSize := logs_receiving.MaxBatchSize
	logItems := CreateValidLogItems(maxBatchSize, testData.UniqueID)

	response := submitLogsForBatch(t, testData.Router, testData.Project.ID, logItems)

	assert.Equal(t, maxBatchSize, response.Accepted)
	assert.Equal(t, 0, response.Rejected)
	assert.Empty(t, response.Errors)
}

func Test_SubmitLogs_ExceedingMaxBatchCount_ReturnsBadRequest(t *testing.T) {
	users_testing.CleanupPlans()

	testData := setupBatchTest("Exceeding Max Batch Count Test")

	exceedingBatchSize := logs_receiving.MaxBatchSize + 1
	logItems := CreateValidLogItems(exceedingBatchSize, testData.UniqueID)

	resp := submitLogsForBatchExpectingError(
		t,
		testData.Router,
		testData.Project.ID,
		logItems,
		http.StatusBadRequest,
	)

	assert.Contains(t, string(resp.Body), "BATCH_TOO_LARGE")
}

func Test_SubmitLogs_ExceedingMaxBatchSizeBytes_ReturnsBadRequest(t *testing.T) {
	users_testing.CleanupPlans()

	testData := setupBatchTest("Exceeding Max Batch Size Bytes Test")

	// Create logs that exceed 10MB total size
	largeLogCount := 200
	logItems := make([]logs_receiving.LogItemRequestDTO, largeLogCount)
	largeMessage := strings.Repeat("A", 60*1_024) // 60KB per log

	for i := range largeLogCount {
		logItems[i] = logs_receiving.LogItemRequestDTO{
			Level:   logs_core.LogLevelInfo,
			Message: fmt.Sprintf("%s - Log %d", largeMessage, i+1),
			Fields: map[string]any{
				"test_id":   testData.UniqueID,
				"log_index": i + 1,
			},
		}
	}

	resp := submitLogsForBatchExpectingError(
		t,
		testData.Router,
		testData.Project.ID,
		logItems,
		http.StatusBadRequest,
	)

	assert.Contains(t, string(resp.Body), "BATCH_TOO_LARGE")
}

// The batch size check runs after decoding: without a body limit, any
// size was read into memory first.
func Test_SubmitLogs_BodyOverTwiceTheBatchLimit_ReturnsRequestEntityTooLarge(t *testing.T) {
	users_testing.CleanupPlans()

	testData := setupBatchTest("Oversized Body Test")
	largeMessage := strings.Repeat("A", 64*1_024)
	logItems := make([]logs_receiving.LogItemRequestDTO, 2*logs_receiving.MaxBatchSizeBytes/len(largeMessage)+1)
	for i := range logItems {
		logItems[i] = logs_receiving.LogItemRequestDTO{Level: logs_core.LogLevelInfo, Message: largeMessage}
	}

	submitLogsForBatchExpectingError(
		t,
		testData.Router,
		testData.Project.ID,
		logItems,
		http.StatusRequestEntityTooLarge,
	)
}

func Test_SubmitLogs_WithMixedValidInvalidLogs_PartialAcceptance(t *testing.T) {
	users_testing.CleanupPlans()

	testData := setupBatchTest("Mixed Valid Invalid Logs Test")

	validLog1 := logs_receiving.LogItemRequestDTO{
		Level:   logs_core.LogLevelInfo,
		Message: fmt.Sprintf("Valid log 1 %s", testData.UniqueID),
		Fields:  map[string]any{"test_id": testData.UniqueID},
	}

	invalidLog := logs_receiving.LogItemRequestDTO{
		Level:   "INVALID_LEVEL",
		Message: fmt.Sprintf("Invalid log %s", testData.UniqueID),
		Fields:  map[string]any{"test_id": testData.UniqueID},
	}

	validLog2 := logs_receiving.LogItemRequestDTO{
		Level:   logs_core.LogLevelError,
		Message: fmt.Sprintf("Valid log 2 %s", testData.UniqueID),
		Fields:  map[string]any{"test_id": testData.UniqueID},
	}

	emptyMessageLog := logs_receiving.LogItemRequestDTO{
		Level:   logs_core.LogLevelWarn,
		Message: "",
		Fields:  map[string]any{"test_id": testData.UniqueID},
	}

	mixedLogs := []logs_receiving.LogItemRequestDTO{validLog1, invalidLog, validLog2, emptyMessageLog}
	response := submitLogsForBatch(t, testData.Router, testData.Project.ID, mixedLogs)

	assert.Equal(t, 2, response.Accepted)
	assert.Equal(t, 2, response.Rejected)
	assert.Len(t, response.Errors, 2)

	errorMessages := make([]string, len(response.Errors))
	for i, err := range response.Errors {
		errorMessages[i] = err.Message
	}
	assert.Contains(t, strings.Join(errorMessages, " "), "INVALID_LOG_LEVEL")
	assert.Contains(t, strings.Join(errorMessages, " "), "MESSAGE_EMPTY")
}

type BatchTestData struct {
	Router   *gin.Engine
	User     *users_dto.SignInResponseDTO
	Project  *projects_models.Project
	UniqueID string
}

func setupBatchTest(testPrefix string) *BatchTestData {
	router := CreateLogsTestRouter()
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	uniqueID := uuid.New().String()
	projectName := fmt.Sprintf("%s %s", testPrefix, uniqueID[:8])

	// LogsPerSecondLimit 0 disables rate limiting, so batches near MaxBatchSize
	// aren't partly rejected as RATE_LIMIT_EXCEEDED.
	project := projects_testing.CreateTestProjectWithConfiguration(
		projectName,
		user,
		router,
		&projects_testing.ProjectConfigurationDTO{
			LogsPerSecondLimit: 0,
			MaxLogSizeKB:       64,
		},
	)

	return &BatchTestData{
		Router:   router,
		User:     user,
		Project:  project,
		UniqueID: uniqueID,
	}
}

func submitLogsForBatch(
	t *testing.T,
	router *gin.Engine,
	projectID uuid.UUID,
	logItems []logs_receiving.LogItemRequestDTO,
) *logs_receiving.SubmitLogsResponseDTO {
	request := &logs_receiving.SubmitLogsRequestDTO{
		Logs: logItems,
	}

	resp := test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method:         "POST",
		URL:            fmt.Sprintf("/api/v1/logs/receiving/%s", projectID.String()),
		Body:           request,
		ExpectedStatus: http.StatusAccepted,
	})

	var response logs_receiving.SubmitLogsResponseDTO
	if err := json.Unmarshal(resp.Body, &response); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	return &response
}

func submitLogsForBatchExpectingError(
	t *testing.T,
	router *gin.Engine,
	projectID uuid.UUID,
	logItems []logs_receiving.LogItemRequestDTO,
	expectedStatus int,
) *test_utils.TestResponse {
	request := &logs_receiving.SubmitLogsRequestDTO{
		Logs: logItems,
	}

	return test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method:         "POST",
		URL:            fmt.Sprintf("/api/v1/logs/receiving/%s", projectID.String()),
		Body:           request,
		ExpectedStatus: expectedStatus,
	})
}
