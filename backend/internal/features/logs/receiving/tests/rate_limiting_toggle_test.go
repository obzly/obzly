package logs_receiving_tests

import (
	"fmt"
	"net/http"
	"testing"

	logs_receiving "obzly/internal/features/logs/receiving"
	projects_testing "obzly/internal/features/projects/testing"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func boolPtr(b bool) *bool { return &b }

// Test_SubmitLogs_WithRateLimitingDisabled_BypassesPerSecondLimit: with
// IsRateLimitingEnabled false, a burst far over LogsPerSecondLimit is accepted.
func Test_SubmitLogs_WithRateLimitingDisabled_BypassesPerSecondLimit(t *testing.T) {
	users_testing.CleanupPlans()
	router := CreateLogsTestRouter()
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	uniqueID := uuid.New().String()
	project := projects_testing.CreateTestProjectWithConfiguration(
		fmt.Sprintf("Rate Limit Disabled Test %s", uniqueID[:8]), user, router,
		&projects_testing.ProjectConfigurationDTO{
			LogsPerSecondLimit:    1, // would normally allow ~1 log/sec (x5 burst = 5)
			MaxLogSizeKB:          64,
			IsRateLimitingEnabled: boolPtr(false),
		},
	)

	logItems := CreateValidLogItems(20, uniqueID)
	request := &logs_receiving.SubmitLogsRequestDTO{Logs: logItems}
	resp := makeRateLimitTestRequest(t, router, project.ID, request)

	var response logs_receiving.SubmitLogsResponseDTO
	require.NoError(t, resp.UnmarshalResponse(&response))
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
	assert.Equal(t, 20, response.Accepted, "every log should be accepted with rate limiting disabled")
	assert.Equal(t, 0, response.Rejected)
}

// Test_SubmitLogs_WithRateLimitingDisabled_BypassesMaxLogSize: the same for
// the per-log size cap.
func Test_SubmitLogs_WithRateLimitingDisabled_BypassesMaxLogSize(t *testing.T) {
	users_testing.CleanupPlans()
	router := CreateLogsTestRouter()
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	uniqueID := uuid.New().String()
	project := projects_testing.CreateTestProjectWithConfiguration(
		fmt.Sprintf("Size Limit Disabled Test %s", uniqueID[:8]), user, router,
		&projects_testing.ProjectConfigurationDTO{
			LogsPerSecondLimit:    0,
			MaxLogSizeKB:          1, // 1KB - the oversized log below is well over this
			IsRateLimitingEnabled: boolPtr(false),
		},
	)

	oversizedMessage := make([]byte, 5*1024) // 5KB, well over the 1KB configured limit
	for i := range oversizedMessage {
		oversizedMessage[i] = 'x'
	}
	logItems := CreateValidLogItems(1, uniqueID)
	logItems[0].Message = string(oversizedMessage)
	request := &logs_receiving.SubmitLogsRequestDTO{Logs: logItems}
	resp := makeRateLimitTestRequest(t, router, project.ID, request)

	var response logs_receiving.SubmitLogsResponseDTO
	require.NoError(t, resp.UnmarshalResponse(&response))
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
	assert.Equal(t, 1, response.Accepted, "an oversized log should be accepted with rate limiting disabled")
	assert.Equal(t, 0, response.Rejected)
}

// Test_SubmitLogs_WithMaxLogSizeKBZero_AndRateLimitingEnabled_AcceptsAnySize:
// MaxLogSizeKB 0 means unlimited even with rate limiting on.
func Test_SubmitLogs_WithMaxLogSizeKBZero_AndRateLimitingEnabled_AcceptsAnySize(t *testing.T) {
	users_testing.CleanupPlans()
	router := CreateLogsTestRouter()
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	uniqueID := uuid.New().String()
	project := projects_testing.CreateTestProjectWithConfiguration(
		fmt.Sprintf("Size Unlimited Test %s", uniqueID[:8]), user, router,
		&projects_testing.ProjectConfigurationDTO{
			LogsPerSecondLimit:    0, // also unlimited, so it can't be the reason this passes
			MaxLogSizeKB:          0, // unlimited - the field under test
			IsRateLimitingEnabled: boolPtr(true),
		},
	)

	oversizedMessage := make([]byte, 5*1024) // 5KB - would be rejected by any positive MaxLogSizeKB below 5
	for i := range oversizedMessage {
		oversizedMessage[i] = 'x'
	}
	logItems := CreateValidLogItems(1, uniqueID)
	logItems[0].Message = string(oversizedMessage)
	request := &logs_receiving.SubmitLogsRequestDTO{Logs: logItems}
	resp := makeRateLimitTestRequest(t, router, project.ID, request)

	var response logs_receiving.SubmitLogsResponseDTO
	require.NoError(t, resp.UnmarshalResponse(&response))
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
	assert.Equal(t, 1, response.Accepted, "MaxLogSizeKB=0 should mean unlimited, even with rate limiting enabled")
	assert.Equal(t, 0, response.Rejected)
}
