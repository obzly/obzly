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

func Test_SubmitLogs_ExceedingRateLimit_ReturnsTooManyRequests(t *testing.T) {
	users_testing.CleanupPlans()
	testData := setupRateLimitTest("Exceeding Rate Limit Test", 2) // Very low limit: 2 per second

	// Submit logs rapidly to exceed the rate limit
	// With 2 RPS limit and 5x burst multiplier, we start with 10 tokens
	// We need to submit more than 10 logs rapidly to exhaust token bucket
	successCount := 0
	rateLimitedCount := 0

	// Try to submit 20 logs very quickly to exhaust token bucket
	for i := range 20 {
		resp := submitTestLogsForRateLimitRaw(
			t,
			testData.Router,
			testData.Project.ID,
			fmt.Sprintf("%s_%d", testData.UniqueID, i),
		)

		if resp.StatusCode == http.StatusAccepted && resp.Response != nil {
			if resp.Response.Accepted > 0 {
				successCount++
			}
			if resp.Response.Rejected > 0 {
				rateLimitedCount++
			}
		}

		// No delay to simulate rapid burst requests
	}

	// Should have had some successful logs accepted, and some rejected due to rate limiting
	assert.Greater(t, successCount, 0, "Should have at least some successful logs")
	assert.Greater(t, rateLimitedCount, 0, "Should have at least some rate limited logs")
}

func Test_SubmitLogs_AfterRateLimitReset_LogsAccepted(t *testing.T) {
	users_testing.CleanupPlans()
	testData := setupRateLimitTest("Rate Limit Reset Test", 3) // 3 logs per second

	// First, exhaust the rate limit
	// With 3 RPS limit and 5x burst multiplier, we start with 15 tokens
	// Submit 15 logs to exhaust all tokens
	for i := 0; i < 15; i++ {
		submitTestLogsForRateLimitRaw(
			t,
			testData.Router,
			testData.Project.ID,
			fmt.Sprintf("%s_exhaust_%d", testData.UniqueID, i),
		)
	}

	// Try to submit one more log - should be rejected due to no tokens
	resp := submitTestLogsForRateLimitRaw(
		t,
		testData.Router,
		testData.Project.ID,
		fmt.Sprintf("%s_should_fail", testData.UniqueID),
	)

	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
	assert.Equal(t, 0, resp.Response.Accepted, "Should reject log when no tokens available")
	assert.Equal(t, 1, resp.Response.Rejected, "Should have 1 rejected log")

	// Wait for rate limit window to reset (1 second should give us at least 3 new tokens)
	time.Sleep(1200 * time.Millisecond)

	// Now submit should work again
	response := submitTestLogsForRateLimit(
		t,
		testData.Router,
		testData.Project.ID,
		fmt.Sprintf("%s_after_reset", testData.UniqueID),
	)

	assert.Equal(t, 1, response.Accepted)
	assert.Equal(t, 0, response.Rejected)
	assert.Empty(t, response.Errors)
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

func Test_SubmitLogs_MultipleSmallBatches_EnforcesRateLimit(t *testing.T) {
	users_testing.CleanupPlans()
	testData := setupRateLimitTest("Multiple Small Batches", 3) // 3 logs per second, burst = 15

	successfulLogs := 0
	rejectedLogs := 0

	// Try submitting batches of 2 logs each, should accept first 7-8 batches (15-16 logs), then start rejecting
	for i := range 10 {
		items := CreateValidLogItems(2, fmt.Sprintf("%s_batch_%d", testData.UniqueID, i))
		req := &logs_receiving.SubmitLogsRequestDTO{Logs: items}
		resp := makeRateLimitTestRequest(t, testData.Router, testData.Project.ID, req)

		assert.Equal(t, http.StatusAccepted, resp.StatusCode)
		var response logs_receiving.SubmitLogsResponseDTO
		err := resp.UnmarshalResponse(&response)
		assert.NoError(t, err)
		successfulLogs += response.Accepted
		rejectedLogs += response.Rejected
	}

	// Total submitted: 10 batches * 2 logs = 20 logs
	// Burst capacity: 15 tokens
	// Should accept ~15 logs and reject ~5 logs
	assert.GreaterOrEqual(t, successfulLogs, 15, "Should accept at least burst capacity worth of logs")
	assert.LessOrEqual(t, successfulLogs, 16, "Should not accept significantly more than burst capacity")
	assert.Greater(t, rejectedLogs, 0, "Should have some rejected logs due to rate limiting")
}

func Test_SubmitLogs_SingleBatchPartiallyRateLimited_AcceptsAvailableLogsOnly(t *testing.T) {
	users_testing.CleanupPlans()
	testData := setupRateLimitTest("Single Batch Partial Rate Limit", 10) // 10 logs per second, burst = 50

	// First, exhaust most tokens - leave only 7 tokens available
	// Submit 43 logs to leave 7 tokens (50 - 43 = 7)
	for i := range 43 {
		items := CreateValidLogItems(1, fmt.Sprintf("%s_exhaust_%d", testData.UniqueID, i))
		req := &logs_receiving.SubmitLogsRequestDTO{Logs: items}
		resp := makeRateLimitTestRequest(t, testData.Router, testData.Project.ID, req)
		assert.Equal(t, http.StatusAccepted, resp.StatusCode)
	}

	// Now submit a batch of 15 logs when only 7 tokens remain
	// Expected: Accept 7 logs, Reject 8 logs with rate limit errors
	logItems := CreateValidLogItems(15, testData.UniqueID+"_partial")
	request := &logs_receiving.SubmitLogsRequestDTO{
		Logs: logItems,
	}

	resp := makeRateLimitTestRequest(t, testData.Router, testData.Project.ID, request)
	assert.Equal(t, http.StatusAccepted, resp.StatusCode, "Should return 202 Accepted even with partial acceptance")

	var response logs_receiving.SubmitLogsResponseDTO
	err := resp.UnmarshalResponse(&response)
	assert.NoError(t, err)

	assert.Equal(t, 7, response.Accepted, "Should accept exactly 7 logs (available tokens)")
	assert.Equal(t, 8, response.Rejected, "Should reject exactly 8 logs (excess over available tokens)")

	// Verify all rejection errors are rate limit errors
	assert.Len(t, response.Errors, 8, "Should have 8 error entries")
	for _, err := range response.Errors {
		assert.Contains(t, err.Message, logs_core.ErrorRateLimitExceeded, "All rejections should be due to rate limit")
	}
}

func Test_SubmitLogs_ZeroTokensAvailable_RejectsAllLogsInBatch(t *testing.T) {
	users_testing.CleanupPlans()
	testData := setupRateLimitTest("Zero Tokens Available", 5) // 5 logs per second, burst = 25

	// Exhaust all tokens completely (send 25 logs)
	for i := 0; i < 25; i++ {
		items := CreateValidLogItems(1, fmt.Sprintf("%s_exhaust_%d", testData.UniqueID, i))
		req := &logs_receiving.SubmitLogsRequestDTO{Logs: items}
		resp := makeRateLimitTestRequest(t, testData.Router, testData.Project.ID, req)
		assert.Equal(t, http.StatusAccepted, resp.StatusCode)
	}

	// Submit batch of 5 logs when 0 tokens available
	logItems := CreateValidLogItems(5, testData.UniqueID+"_zero_tokens")
	request := &logs_receiving.SubmitLogsRequestDTO{
		Logs: logItems,
	}

	resp := makeRateLimitTestRequest(t, testData.Router, testData.Project.ID, request)
	assert.Equal(t, http.StatusAccepted, resp.StatusCode, "Should return 202 Accepted")

	var response logs_receiving.SubmitLogsResponseDTO
	err := resp.UnmarshalResponse(&response)
	assert.NoError(t, err)

	assert.Equal(t, 0, response.Accepted, "Should accept 0 logs")
	assert.Equal(t, 5, response.Rejected, "Should reject all 5 logs")
	assert.Len(t, response.Errors, 5, "Should have 5 error entries")

	for _, err := range response.Errors {
		assert.Contains(t, err.Message, logs_core.ErrorRateLimitExceeded)
	}
}

func Test_SubmitLogs_BatchExceedsLPSLimit_OnlyLPSLogsAccepted(t *testing.T) {
	users_testing.CleanupPlans()
	testData := setupRateLimitTest("Batch Exceeds LPS Limit", 10) // 10 logs per second

	// Submit 15 logs in a single batch when LPS is 10
	// Even though burst capacity is 50 tokens, we should only accept 10 logs (the LPS limit)
	logItems := CreateValidLogItems(15, testData.UniqueID+"_single_batch")
	request := &logs_receiving.SubmitLogsRequestDTO{
		Logs: logItems,
	}

	resp := makeRateLimitTestRequest(t, testData.Router, testData.Project.ID, request)
	assert.Equal(t, http.StatusAccepted, resp.StatusCode, "Should return 202 Accepted")

	var response logs_receiving.SubmitLogsResponseDTO
	err := resp.UnmarshalResponse(&response)
	assert.NoError(t, err)

	assert.Equal(t, 10, response.Accepted, "Should accept exactly 10 logs (the LPS limit)")
	assert.Equal(t, 5, response.Rejected, "Should reject 5 logs (excess over LPS limit)")
	assert.Len(t, response.Errors, 5, "Should have 5 error entries")

	for _, err := range response.Errors {
		assert.Contains(t, err.Message, logs_core.ErrorRateLimitExceeded, "All rejections should be due to rate limit")
	}
}

func Test_SubmitLogs_BatchExceedsLPSLimit_MultipleScenarios(t *testing.T) {
	users_testing.CleanupPlans()
	testData := setupRateLimitTest("Multiple LPS Scenarios", 5) // 5 logs per second

	// Scenario 1: Submit 8 logs, should accept 5 (LPS limit)
	logItems1 := CreateValidLogItems(8, testData.UniqueID+"_batch1")
	request1 := &logs_receiving.SubmitLogsRequestDTO{Logs: logItems1}
	resp1 := makeRateLimitTestRequest(t, testData.Router, testData.Project.ID, request1)

	var response1 logs_receiving.SubmitLogsResponseDTO
	err1 := resp1.UnmarshalResponse(&response1)
	assert.NoError(t, err1)
	assert.Equal(t, 5, response1.Accepted, "First batch: should accept 5 logs (LPS limit)")
	assert.Equal(t, 3, response1.Rejected, "First batch: should reject 3 logs")

	// Scenario 2: Submit 3 logs immediately after, should accept 3 (still have tokens)
	logItems2 := CreateValidLogItems(3, testData.UniqueID+"_batch2")
	request2 := &logs_receiving.SubmitLogsRequestDTO{Logs: logItems2}
	resp2 := makeRateLimitTestRequest(t, testData.Router, testData.Project.ID, request2)

	var response2 logs_receiving.SubmitLogsResponseDTO
	err2 := resp2.UnmarshalResponse(&response2)
	assert.NoError(t, err2)
	assert.Equal(t, 3, response2.Accepted, "Second batch: should accept 3 logs (within both LPS and available tokens)")
	assert.Equal(t, 0, response2.Rejected, "Second batch: should reject 0 logs")

	// Scenario 3: Submit 10 more logs, should accept 5 (LPS limit)
	logItems3 := CreateValidLogItems(10, testData.UniqueID+"_batch3")
	request3 := &logs_receiving.SubmitLogsRequestDTO{Logs: logItems3}
	resp3 := makeRateLimitTestRequest(t, testData.Router, testData.Project.ID, request3)

	var response3 logs_receiving.SubmitLogsResponseDTO
	err3 := resp3.UnmarshalResponse(&response3)
	assert.NoError(t, err3)
	assert.Equal(t, 5, response3.Accepted, "Third batch: should accept 5 logs (LPS limit)")
	assert.Equal(t, 5, response3.Rejected, "Third batch: should reject 5 logs (exceeds LPS)")
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
	}
}
