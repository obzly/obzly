package logs_querying_tests

import (
	"net/http"
	"testing"

	logs_core "obzly/internal/features/logs/core"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_GetSystemDashboard_WhenAdmin_ReturnsMetrics(t *testing.T) {
	router := CreateLogQueryTestRouter()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	var response logs_core.SystemDashboardDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t, router, "/api/v1/logs/query/system-dashboard", "Bearer "+admin.Token, http.StatusOK, &response,
	)

	assert.GreaterOrEqual(t, response.TotalLogs, int64(0))
	assert.Equal(t, 15, response.IngestionWindowMinutes)
	assert.GreaterOrEqual(t, response.CPUPercent, 0.0)
	require.Positive(t, response.MemoryTotalBytes)
	assert.Positive(t, response.DiskTotalBytes)
}

func Test_GetSystemDashboard_WhenMember_ReturnsForbidden(t *testing.T) {
	router := CreateLogQueryTestRouter()
	member := users_testing.CreateTestUser(users_enums.UserRoleMember)

	test_utils.MakeGetRequest(
		t, router, "/api/v1/logs/query/system-dashboard", "Bearer "+member.Token, http.StatusForbidden,
	)
}

func Test_GetSystemDashboard_WithoutAuth_ReturnsUnauthorized(t *testing.T) {
	router := CreateLogQueryTestRouter()

	test_utils.MakeGetRequest(t, router, "/api/v1/logs/query/system-dashboard", "", http.StatusUnauthorized)
}
