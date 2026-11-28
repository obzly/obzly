package logs_receiving_tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"
	logs_receiving "obzly/internal/features/logs/receiving"
	projects_models "obzly/internal/features/projects/models"
	projects_testing "obzly/internal/features/projects/testing"
	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func Test_SubmitLogs_WithinRateLimit_LogsAccepted(t *testing.T) {
	users_testing.CleanupPlans()
	testData := setupRateLimitTest("Within Rate Limit Test", 10) // 10 logs per second

	// Submit 5 logs (well within limit)
	for i := range 5 {
		response := submitTestLogsForRateLimit(
			t,
			testData.Router,
			testData.Project.ID,
			fmt.Sprintf("%s_%d", testData.UniqueID, i),
		)

		assert.Equal(t, 1, response.Accepted)
		assert.Equal(t, 0, response.Rejected)
		assert.Empty(t, response.Errors)

		// Small delay to avoid hitting any burst limits
		time.Sleep(50 * time.Millisecond)
	}
}

func Test_SubmitLogs_ExceedingRateLimit_ReturnsTooManyRequestsWithRetryAfter(t *testing.T) {
	users_testing.CleanupPlans()
	testData := setupRateLimitTest("Exceeding Rate Limit Test", 2) // 2 per second, burst 10

	accepted, throttled := 0, 0
	var throttledResponse *TestResponse
	for i := range 20 {
		resp := makeRateLimitTestRequest(
			t,
			testData.Router,
			testData.Project.ID,
			&logs_receiving.SubmitLogsRequestDTO{
				Logs: CreateValidLogItems(1, fmt.Sprintf("%s_%d", testData.UniqueID, i)),
			},
		)
		switch resp.StatusCode {
		case http.StatusAccepted:
			accepted++
		case http.StatusTooManyRequests:
			throttled++
			throttledResponse = resp
		default:
			t.Fatalf("unexpected status %d: %s", resp.StatusCode, resp.Body)
		}
	}

	assert.GreaterOrEqual(t, accepted, 10, "the burst is accepted")
	assert.Greater(t, throttled, 0, "past the burst, batches are throttled")
	assert.NotEmpty(t, throttledResponse.Header.Get("Retry-After"))
	assert.Contains(t, string(throttledResponse.Body), logs_core.ErrorRateLimitExceeded)
}

func Test_SubmitLogs_AfterRateLimitReset_LogsAccepted(t *testing.T) {
	users_testing.CleanupPlans()
	testData := setupRateLimitTest("Rate Limit Reset Test", 3) // 3 per second, burst 15

	exhaust := CreateValidLogItems(15, testData.UniqueID+"_exhaust")
	resp := makeRateLimitTestRequest(t, testData.Router, testData.Project.ID,
		&logs_receiving.SubmitLogsRequestDTO{Logs: exhaust})
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)

	resp = makeRateLimitTestRequest(t, testData.Router, testData.Project.ID,
		&logs_receiving.SubmitLogsRequestDTO{Logs: CreateValidLogItems(1, testData.UniqueID+"_should_fail")})
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)

	// One second refills 3 tokens.
	time.Sleep(1200 * time.Millisecond)

	response := submitTestLogsForRateLimit(t, testData.Router, testData.Project.ID, testData.UniqueID+"_after_reset")
	assert.Equal(t, 1, response.Accepted)
	assert.Equal(t, 0, response.Rejected)
}

func Test_SubmitLogs_WithZeroRateLimit_UnlimitedAccess(t *testing.T) {
	users_testing.CleanupPlans()
	testData := setupRateLimitTest("Zero Rate Limit Test", 0) // 0 = unlimited

	// Submit many logs rapidly - should all be accepted with unlimited rate
	requestCount := 15
	successCount := 0

	for i := 0; i < requestCount; i++ {
		response := submitTestLogsForRateLimit(
			t,
			testData.Router,
			testData.Project.ID,
			fmt.Sprintf("%s_unlimited_%d", testData.UniqueID, i),
		)

		if response.Accepted > 0 {
			successCount++
		}
	}

	// With unlimited rate (0), all should succeed
	assert.Equal(t, requestCount, successCount, "Should accept all requests with unlimited rate limit")
}

func Test_SubmitLogs_MultipleSmallBatches_ThrottlesWholeBatchesPastTheBurst(t *testing.T) {
	users_testing.CleanupPlans()
	testData := setupRateLimitTest("Multiple Small Batches", 3) // 3 per second, burst 15

	acceptedLogs := 0
	throttled := 0
	for i := range 10 {
		items := CreateValidLogItems(2, fmt.Sprintf("%s_batch_%d", testData.UniqueID, i))
		resp := makeRateLimitTestRequest(t, testData.Router, testData.Project.ID,
			&logs_receiving.SubmitLogsRequestDTO{Logs: items})
		if resp.StatusCode == http.StatusTooManyRequests {
			throttled++
			continue
		}
		assert.Equal(t, http.StatusAccepted, resp.StatusCode)
		var response logs_receiving.SubmitLogsResponseDTO
		assert.NoError(t, resp.UnmarshalResponse(&response))
		assert.Equal(t, 0, response.Rejected, "an accepted batch keeps every log")
		acceptedLogs += response.Accepted
	}

	// 20 logs against a burst of 15: about 7 batches in, the rest throttled.
	assert.GreaterOrEqual(t, acceptedLogs, 14)
	assert.LessOrEqual(t, acceptedLogs, 16)
	assert.Greater(t, throttled, 0)
}

func Test_SubmitLogs_BatchLargerThanTokensLeft_ThrottledWhole(t *testing.T) {
	users_testing.CleanupPlans()
	testData := setupRateLimitTest("Batch Larger Than Tokens Left", 10) // 10 per second, burst 50

	resp := makeRateLimitTestRequest(t, testData.Router, testData.Project.ID,
		&logs_receiving.SubmitLogsRequestDTO{Logs: CreateValidLogItems(43, testData.UniqueID+"_exhaust")})
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)

	// 7 tokens left for 15 logs: the batch is refused whole, never cut to 7,
	// and the sender is told to come back once 8 more have refilled.
	resp = makeRateLimitTestRequest(t, testData.Router, testData.Project.ID,
		&logs_receiving.SubmitLogsRequestDTO{Logs: CreateValidLogItems(15, testData.UniqueID+"_partial")})
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.Equal(t, "1", resp.Header.Get("Retry-After"))
}

func Test_SubmitLogs_BatchLargerThanBurst_AcceptedWhenBucketIsFull(t *testing.T) {
	users_testing.CleanupPlans()
	testData := setupRateLimitTest("Batch Larger Than Burst", 5) // 5 per second, burst 25

	resp := makeRateLimitTestRequest(t, testData.Router, testData.Project.ID,
		&logs_receiving.SubmitLogsRequestDTO{Logs: CreateValidLogItems(30, testData.UniqueID+"_big")})
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
	var response logs_receiving.SubmitLogsResponseDTO
	assert.NoError(t, resp.UnmarshalResponse(&response))
	assert.Equal(t, 30, response.Accepted)

	// The bucket owes 5 tokens: the next log waits about 1.2 s.
	resp = makeRateLimitTestRequest(t, testData.Router, testData.Project.ID,
		&logs_receiving.SubmitLogsRequestDTO{Logs: CreateValidLogItems(1, testData.UniqueID+"_next")})
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.Equal(t, "2", resp.Header.Get("Retry-After"))
}

func Test_SubmitLogs_BatchAboveLPSWithinBurst_AllAccepted(t *testing.T) {
	users_testing.CleanupPlans()
	testData := setupRateLimitTest("Batch Above LPS Within Burst", 10) // 10 per second, burst 50

	resp := makeRateLimitTestRequest(t, testData.Router, testData.Project.ID,
		&logs_receiving.SubmitLogsRequestDTO{Logs: CreateValidLogItems(15, testData.UniqueID+"_single_batch")})
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)

	var response logs_receiving.SubmitLogsResponseDTO
	assert.NoError(t, resp.UnmarshalResponse(&response))
	assert.Equal(t, 15, response.Accepted)
	assert.Equal(t, 0, response.Rejected)
}

type RateLimitTestData struct {
	Router   *gin.Engine
	User     *users_dto.SignInResponseDTO
	Project  *projects_models.Project
	UniqueID string
}

func setupRateLimitTest(testPrefix string, logsPerSecondLimit int) *RateLimitTestData {
	router := CreateLogsTestRouter()
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	uniqueID := uuid.New().String()
	projectName := fmt.Sprintf("%s %s", testPrefix, uniqueID[:8])

	config := &projects_testing.ProjectConfigurationDTO{
		IsApiKeyRequired:   false,
		IsFilterByDomain:   false,
		AllowedDomains:     nil,
		IsFilterByIP:       false,
		AllowedIPs:         nil,
		LogsPerSecondLimit: logsPerSecondLimit,
		MaxLogSizeKB:       64,
	}
	project := projects_testing.CreateTestProjectWithConfiguration(projectName, user, router, config)

	return &RateLimitTestData{
		Router:   router,
		User:     user,
		Project:  project,
		UniqueID: uniqueID,
	}
}

func submitTestLogsForRateLimit(
	t *testing.T,
	router *gin.Engine,
	projectID uuid.UUID,
	uniqueID string,
) *logs_receiving.SubmitLogsResponseDTO {
	resp := submitTestLogsForRateLimitRaw(t, router, projectID, uniqueID)

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("Expected successful log submission, got status %d: %s", resp.StatusCode, string(resp.Body))
	}

	return resp.Response
}

type RateLimitTestResponse struct {
	StatusCode int
	Body       []byte
	Response   *logs_receiving.SubmitLogsResponseDTO
}

func submitTestLogsForRateLimitRaw(
	t *testing.T,
	router *gin.Engine,
	projectID uuid.UUID,
	uniqueID string,
) *RateLimitTestResponse {
	logItems := CreateValidLogItems(1, uniqueID)
	request := &logs_receiving.SubmitLogsRequestDTO{
		Logs: logItems,
	}

	resp := makeRateLimitTestRequest(t, router, projectID, request)

	result := &RateLimitTestResponse{
		StatusCode: resp.StatusCode,
		Body:       resp.Body,
	}

	// Only try to parse response if it was successful
	if resp.StatusCode == http.StatusAccepted {
		var response logs_receiving.SubmitLogsResponseDTO
		if err := resp.UnmarshalResponse(&response); err != nil {
			t.Fatalf("Failed to unmarshal successful response: %v", err)
		}
		result.Response = &response
	}

	return result
}

type TestResponse struct {
	StatusCode int
	Body       []byte
	Header     http.Header
}

func (tr *TestResponse) UnmarshalResponse(target interface{}) error {
	return json.Unmarshal(tr.Body, target)
}

func makeRateLimitTestRequest(
	t *testing.T,
	router *gin.Engine,
	projectID uuid.UUID,
	request *logs_receiving.SubmitLogsRequestDTO,
) *TestResponse {
	jsonBody, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("Failed to marshal request: %v", err)
	}

	req := httptest.NewRequest(
		"POST",
		fmt.Sprintf("/api/v1/logs/receiving/%s", projectID.String()),
		bytes.NewReader(jsonBody),
	)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	return &TestResponse{
		StatusCode: w.Code,
		Body:       w.Body.Bytes(),
		Header:     w.Header(),
	}
}
