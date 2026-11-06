package metrics_querying_tests

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	metrics_core "obzly/internal/features/metrics/core"
	projects_testing "obzly/internal/features/projects/testing"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_GetProjectStats_ReturnsSeededTotals(t *testing.T) {
	router := createTestRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Metric Stats", owner.Token, router)

	samples := []*metrics_core.MetricSample{
		{
			ID:         uuid.New(),
			ProjectID:  project.ID,
			MetricName: "http_requests_total",
			Value:      1,
			Timestamp:  time.Now().UTC(),
			Type:       metrics_core.MetricTypeCounter,
		},
		{
			ID:         uuid.New(),
			ProjectID:  project.ID,
			MetricName: "cpu_usage",
			Value:      1,
			Timestamp:  time.Now().UTC(),
			Type:       metrics_core.MetricTypeGauge,
		},
		{
			ID:         uuid.New(),
			ProjectID:  project.ID,
			MetricName: "mem_usage",
			Value:      1,
			Timestamp:  time.Now().UTC(),
			Type:       metrics_core.MetricTypeGauge,
		},
	}
	require.NoError(t, metrics_core.GetMetricCoreRepository().StoreSamplesBatch(
		map[uuid.UUID][]*metrics_core.MetricSample{project.ID: samples},
	))
	require.NoError(t, metrics_core.GetMetricCoreRepository().ForceFlush())

	var response metrics_core.MetricsStatsDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t, router,
		fmt.Sprintf("/api/v1/metrics/query/stats/%s", project.ID.String()),
		"Bearer "+owner.Token, http.StatusOK, &response,
	)

	assert.Equal(t, int64(3), response.TotalSamples)
	assert.Greater(t, response.TotalSizeMB, 0.0)
	assert.False(t, response.OldestSampleTime.IsZero())
	assert.False(t, response.NewestSampleTime.IsZero())
}

func Test_GetProjectStats_NonMember_ReturnsForbidden(t *testing.T) {
	router := createTestRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Metric Stats Forbidden", owner.Token, router)
	outsider := users_testing.CreateTestUser(users_enums.UserRoleMember)

	test_utils.MakeGetRequest(
		t, router,
		fmt.Sprintf("/api/v1/metrics/query/stats/%s", project.ID.String()),
		"Bearer "+outsider.Token, http.StatusForbidden,
	)
}
