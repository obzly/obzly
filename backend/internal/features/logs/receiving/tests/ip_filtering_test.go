package logs_receiving_tests

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

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
	"github.com/stretchr/testify/require"
)

func Test_SubmitLogs_WhenIPFilterEnabled_WithAllowedIP_LogsAccepted(t *testing.T) {
	users_testing.CleanupPlans()
	testData := setupIPTest("Allowed IP Test", []string{"192.168.1.100", "10.0.0.5"})

	response := submitTestLogsWithIP(
		t,
		testData.Router,
		testData.Project.ID,
		"",
		testData.UniqueID,
		"192.168.1.100",
	)

	assert.Equal(t, 1, response.Accepted)
	assert.Equal(t, 0, response.Rejected)
	assert.Empty(t, response.Errors)
}

func Test_SubmitLogs_WhenIPFilterEnabled_WithDisallowedIP_ReturnsForbidden(t *testing.T) {
	users_testing.CleanupPlans()
	testData := setupIPTest("Disallowed IP Test", []string{"192.168.1.100"})

	resp := submitTestLogsWithIPExpectingError(
		t,
		testData.Router,
		testData.Project.ID,
		"",
		testData.UniqueID,
		"10.0.0.5", // Not in allowed list
		http.StatusForbidden,
	)

	assert.Contains(t, string(resp.Body), "IP address not allowed")
}

func Test_SubmitLogs_WhenIPFilterEnabled_WithCIDRRange_LogsAccepted(t *testing.T) {
	users_testing.CleanupPlans()
	testData := setupIPTest("CIDR Range Test", []string{"192.168.1.0/24", "10.0.0.0/8"})

	// Test IP within first CIDR range
	response1 := submitTestLogsWithIP(
		t,
		testData.Router,
		testData.Project.ID,
		"",
		testData.UniqueID+"_1",
		"192.168.1.50",
	)
	assert.Equal(t, 1, response1.Accepted)

	// Test IP within second CIDR range
	response2 := submitTestLogsWithIP(
		t,
		testData.Router,
		testData.Project.ID,
		"",
		testData.UniqueID+"_2",
		"10.5.10.20",
	)
	assert.Equal(t, 1, response2.Accepted)

	// Test IP outside both CIDR ranges should fail
	resp := submitTestLogsWithIPExpectingError(
		t,
		testData.Router,
		testData.Project.ID,
		"",
		testData.UniqueID+"_3",
		"172.16.0.10", // Not in allowed CIDR ranges
		http.StatusForbidden,
	)
	assert.Contains(t, string(resp.Body), "IP address not allowed")
}

// Without TRUSTED_PROXIES, X-Forwarded-For/X-Real-IP are attacker-controlled:
// the IP filter must use the real RemoteAddr in both directions.

func Test_SubmitLogs_WhenIPFilterEnabled_XForwardedForClaimingAllowedIP_StillUsesRealDisallowedIP(
	t *testing.T,
) {
	users_testing.CleanupPlans()
	testData := setupIPTest("XFF Spoof Test", []string{"203.0.113.45"})

	resp := submitTestLogsWithSpoofedHeader(
		t,
		testData.Router,
		testData.Project.ID,
		testData.UniqueID,
		"X-Forwarded-For",
		"203.0.113.45, 192.168.1.1", // claims the allowed IP
		"10.0.0.9",                  // real RemoteAddr is NOT allowed
		http.StatusForbidden,
	)

	assert.Contains(t, string(resp.Body), "IP address not allowed")
}

func Test_SubmitLogs_WhenIPFilterEnabled_XForwardedForClaimingDisallowedIP_RealAllowedIPStillAccepted(
	t *testing.T,
) {
	users_testing.CleanupPlans()
	testData := setupIPTest("XFF Spoof Test 2", []string{"203.0.113.45"})

	resp := submitTestLogsWithSpoofedHeader(
		t,
		testData.Router,
		testData.Project.ID,
		testData.UniqueID,
		"X-Forwarded-For",
		"10.0.0.9", // claims a disallowed IP
		"203.0.113.45",
		http.StatusAccepted,
	)

	var response logs_receiving.SubmitLogsResponseDTO
	require.NoError(t, json.Unmarshal(resp.Body, &response))
	assert.Equal(t, 1, response.Accepted)
}

func Test_SubmitLogs_WhenIPFilterEnabled_XRealIPClaimingAllowedIP_StillUsesRealDisallowedIP(
	t *testing.T,
) {
	users_testing.CleanupPlans()
	testData := setupIPTest("X-Real-IP Spoof Test", []string{"198.51.100.23"})

	resp := submitTestLogsWithSpoofedHeader(
		t,
		testData.Router,
		testData.Project.ID,
		testData.UniqueID,
		"X-Real-IP",
		"198.51.100.23", // claims the allowed IP
		"10.0.0.100",    // real RemoteAddr is NOT allowed
		http.StatusForbidden,
	)

	assert.Contains(t, string(resp.Body), "IP address not allowed")
}

func Test_SubmitLogs_WhenIPFilterEnabled_XRealIPClaimingDisallowedIP_RealAllowedIPStillAccepted(
	t *testing.T,
) {
	users_testing.CleanupPlans()
	testData := setupIPTest("X-Real-IP Spoof Test 2", []string{"198.51.100.23"})

	resp := submitTestLogsWithSpoofedHeader(
		t,
		testData.Router,
		testData.Project.ID,
		testData.UniqueID,
		"X-Real-IP",
		"10.0.0.100", // claims a disallowed IP
		"198.51.100.23",
		http.StatusAccepted,
	)

	var response logs_receiving.SubmitLogsResponseDTO
	require.NoError(t, json.Unmarshal(resp.Body, &response))
	assert.Equal(t, 1, response.Accepted)
}

// Test_SubmitLogs_WithTrustedProxy_XForwardedForIsHonored: with the proxy in
// TRUSTED_PROXIES, filtering applies to the proxy's clients.
func Test_SubmitLogs_WithTrustedProxy_XForwardedForIsHonored(t *testing.T) {
	users_testing.CleanupPlans()

	router := CreateLogsTestRouter()
	require.NoError(t, router.SetTrustedProxies([]string{"203.0.113.1/32"}))

	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	uniqueID := uuid.New().String()
	config := &projects_testing.ProjectConfigurationDTO{
		IsApiKeyRequired:   false,
		IsFilterByIP:       true,
		AllowedIPs:         []string{"198.51.100.7"},
		LogsPerSecondLimit: 1000,
		MaxLogSizeKB:       64,
	}
	project := projects_testing.CreateTestProjectWithConfiguration(
		"Trusted Proxy Test "+uniqueID[:8], user, router, config,
	)

	logItems := CreateValidLogItems(1, uniqueID)
	resp := test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method: "POST",
		URL:    fmt.Sprintf("/api/v1/logs/receiving/%s", project.ID.String()),
		Body:   &logs_receiving.SubmitLogsRequestDTO{Logs: logItems},
		Headers: map[string]string{
			// The trusted proxy's own address is the RemoteAddr below; the
			// real client's IP (allowed) is what it forwarded.
			"X-Forwarded-For": "198.51.100.7",
		},
		// The direct TCP peer IS the trusted proxy.
		RemoteAddr:     "203.0.113.1:9999",
		ExpectedStatus: http.StatusAccepted,
	})

	var response logs_receiving.SubmitLogsResponseDTO
	require.NoError(t, json.Unmarshal(resp.Body, &response))
	assert.Equal(t, 1, response.Accepted)
}

func Test_SubmitLogs_WhenIPFilterDisabled_WithAnyIP_LogsAccepted(t *testing.T) {
	users_testing.CleanupPlans()
	testData := setupIPTest("IP Filter Disabled Test", nil)

	// Test with random IP when filtering is disabled
	response := submitTestLogsWithIP(
		t,
		testData.Router,
		testData.Project.ID,
		"",
		testData.UniqueID,
		"1.2.3.4", // Any IP should work
	)

	assert.Equal(t, 1, response.Accepted)
	assert.Equal(t, 0, response.Rejected)
	assert.Empty(t, response.Errors)
}

func Test_SubmitLogs_WhenIPFilterEnabled_WithMultipleAllowedIPs_LogsAccepted(t *testing.T) {
	users_testing.CleanupPlans()
	testData := setupIPTest("Multiple IPs Test", []string{"192.168.1.100", "10.0.0.5", "203.0.113.45"})

	// Test first allowed IP
	response1 := submitTestLogsWithIP(
		t,
		testData.Router,
		testData.Project.ID,
		"",
		testData.UniqueID+"_1",
		"192.168.1.100",
	)
	assert.Equal(t, 1, response1.Accepted)

	// Test second allowed IP
	response2 := submitTestLogsWithIP(
		t,
		testData.Router,
		testData.Project.ID,
		"",
		testData.UniqueID+"_2",
		"10.0.0.5",
	)
	assert.Equal(t, 1, response2.Accepted)

	// Test third allowed IP
	response3 := submitTestLogsWithIP(
		t,
		testData.Router,
		testData.Project.ID,
		"",
		testData.UniqueID+"_3",
		"203.0.113.45",
	)
	assert.Equal(t, 1, response3.Accepted)
}

func Test_SubmitLogs_WhenIPFilterEnabled_WithMixedIPsAndCIDR_LogsAccepted(t *testing.T) {
	users_testing.CleanupPlans()
	testData := setupIPTest("Mixed IPs and CIDR Test", []string{"192.168.1.100", "10.0.0.0/24"})

	// Test specific IP
	response1 := submitTestLogsWithIP(
		t,
		testData.Router,
		testData.Project.ID,
		"",
		testData.UniqueID+"_1",
		"192.168.1.100",
	)
	assert.Equal(t, 1, response1.Accepted)

	// Test IP within CIDR range
	response2 := submitTestLogsWithIP(
		t,
		testData.Router,
		testData.Project.ID,
		"",
		testData.UniqueID+"_2",
		"10.0.0.50",
	)
	assert.Equal(t, 1, response2.Accepted)

	// Test IP outside both specific IP and CIDR range
	resp := submitTestLogsWithIPExpectingError(
		t,
		testData.Router,
		testData.Project.ID,
		"",
		testData.UniqueID+"_3",
		"172.16.0.10",
		http.StatusForbidden,
	)
	assert.Contains(t, string(resp.Body), "IP address not allowed")
}

type IPTestData struct {
	Router   *gin.Engine
	User     *users_dto.SignInResponseDTO
	Project  *projects_models.Project
	UniqueID string
}

func setupIPTest(testPrefix string, allowedIPs []string) *IPTestData {
	router := CreateLogsTestRouter()
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	uniqueID := uuid.New().String()
	projectName := fmt.Sprintf("%s %s", testPrefix, uniqueID[:8])

	var project *projects_models.Project
	if allowedIPs != nil {
		config := &projects_testing.ProjectConfigurationDTO{
			IsApiKeyRequired:   false,
			IsFilterByDomain:   false,
			AllowedDomains:     nil,
			IsFilterByIP:       true,
			AllowedIPs:         allowedIPs,
			LogsPerSecondLimit: 1000,
			MaxLogSizeKB:       64,
		}
		project = projects_testing.CreateTestProjectWithConfiguration(projectName, user, router, config)
	} else {
		project = projects_testing.CreateBasicTestProject(projectName, user, router)
	}

	return &IPTestData{
		Router:   router,
		User:     user,
		Project:  project,
		UniqueID: uniqueID,
	}
}

// submitTestLogsWithIP sends from a direct TCP peer at clientIP, which alone
// decides the IP filter when no proxy is trusted.
func submitTestLogsWithIP(
	t *testing.T,
	router *gin.Engine,
	projectID uuid.UUID,
	apiKeyToken, uniqueID, clientIP string,
) *logs_receiving.SubmitLogsResponseDTO {
	logItems := CreateValidLogItems(1, uniqueID)
	request := &logs_receiving.SubmitLogsRequestDTO{
		Logs: logItems,
	}

	headers := make(map[string]string)
	if apiKeyToken != "" {
		headers["X-API-Key"] = apiKeyToken
	}

	resp := test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method:         "POST",
		URL:            fmt.Sprintf("/api/v1/logs/receiving/%s", projectID.String()),
		Body:           request,
		Headers:        headers,
		RemoteAddr:     clientIP + ":54321",
		ExpectedStatus: http.StatusAccepted,
	})

	var response logs_receiving.SubmitLogsResponseDTO
	if err := json.Unmarshal(resp.Body, &response); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	return &response
}

func submitTestLogsWithIPExpectingError(
	t *testing.T,
	router *gin.Engine,
	projectID uuid.UUID,
	apiKeyToken, uniqueID, clientIP string,
	expectedStatus int,
) *test_utils.TestResponse {
	logItems := CreateValidLogItems(1, uniqueID)
	request := &logs_receiving.SubmitLogsRequestDTO{
		Logs: logItems,
	}

	headers := make(map[string]string)
	if apiKeyToken != "" {
		headers["X-API-Key"] = apiKeyToken
	}

	return test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method:         "POST",
		URL:            fmt.Sprintf("/api/v1/logs/receiving/%s", projectID.String()),
		Body:           request,
		Headers:        headers,
		RemoteAddr:     clientIP + ":54321",
		ExpectedStatus: expectedStatus,
	})
}

// submitTestLogsWithSpoofedHeader sends headerName: headerValue from
// realRemoteIP with no trusted proxy; expectedStatus shows which one won.
func submitTestLogsWithSpoofedHeader(
	t *testing.T,
	router *gin.Engine,
	projectID uuid.UUID,
	uniqueID, headerName, headerValue, realRemoteIP string,
	expectedStatus int,
) *test_utils.TestResponse {
	logItems := CreateValidLogItems(1, uniqueID)
	request := &logs_receiving.SubmitLogsRequestDTO{
		Logs: logItems,
	}

	return test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method:         "POST",
		URL:            fmt.Sprintf("/api/v1/logs/receiving/%s", projectID.String()),
		Body:           request,
		Headers:        map[string]string{headerName: headerValue},
		RemoteAddr:     realRemoteIP + ":54321",
		ExpectedStatus: expectedStatus,
	})
}
