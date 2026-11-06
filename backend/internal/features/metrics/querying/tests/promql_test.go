package metrics_querying_tests

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	metrics_core "obzly/internal/features/metrics/core"
	metrics_querying "obzly/internal/features/metrics/querying"
	projects_testing "obzly/internal/features/projects/testing"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_PromQLQuery_Instant_ReturnsSeededValue(t *testing.T) {
	router := createTestRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("PromQL", owner.Token, router)

	now := time.Now().UTC()
	require.NoError(t, metrics_core.GetMetricCoreRepository().StoreSamplesBatch(
		map[uuid.UUID][]*metrics_core.MetricSample{project.ID: {{
			ID: uuid.New(), ProjectID: project.ID, MetricName: "queue_depth",
			Value: 42, Timestamp: now.Add(-30 * time.Second), Type: metrics_core.MetricTypeGauge,
		}}},
	))
	require.NoError(t, metrics_core.GetMetricCoreRepository().ForceFlush())

	var response metrics_querying.PromQLQueryResponseDTO
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/metrics/query/promql/"+project.ID.String(),
		"Bearer "+owner.Token,
		metrics_querying.PromQLQueryRequestDTO{Query: "queue_depth", Instant: true, End: &now},
		http.StatusOK, &response)

	assert.Equal(t, "vector", response.ResultType)
	require.Len(t, response.Series, 1)
	assert.Equal(t, 42.0, response.Series[0].Points[0].Value)
}

func Test_PromQLQuery_InvalidExpression_ReturnsParseError(t *testing.T) {
	router := createTestRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("PromQL bad", owner.Token, router)

	resp := test_utils.MakePostRequest(t, router, "/api/v1/metrics/query/promql/"+project.ID.String(),
		"Bearer "+owner.Token, metrics_querying.PromQLQueryRequestDTO{Query: "rate(foo[5m"}, http.StatusBadRequest)
	assert.Contains(t, string(resp.Body), "invalid PromQL")
}

func Test_PromQLQuery_NonMember_ReturnsForbidden(t *testing.T) {
	router := createTestRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("PromQL private", owner.Token, router)
	outsider := users_testing.CreateTestUser(users_enums.UserRoleMember)

	test_utils.MakePostRequest(t, router, "/api/v1/metrics/query/promql/"+project.ID.String(),
		"Bearer "+outsider.Token, metrics_querying.PromQLQueryRequestDTO{Query: "up"}, http.StatusForbidden)
}

func Test_PromQLQuery_WithMaxSeries_ReturnsMoreThanDefaultLimit(t *testing.T) {
	router := createTestRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("PromQL many", owner.Token, router)

	now := time.Now().UTC()
	samples := make([]*metrics_core.MetricSample, 0, 150)
	for i := range 150 {
		samples = append(samples, &metrics_core.MetricSample{
			ID: uuid.New(), ProjectID: project.ID, MetricName: "check_up",
			Labels: map[string]string{"check_id": fmt.Sprintf("c%03d", i)},
			Value:  1, Timestamp: now.Add(-30 * time.Second), Type: metrics_core.MetricTypeGauge,
		})
	}
	require.NoError(t, metrics_core.GetMetricCoreRepository().StoreSamplesBatch(
		map[uuid.UUID][]*metrics_core.MetricSample{project.ID: samples},
	))
	require.NoError(t, metrics_core.GetMetricCoreRepository().ForceFlush())

	url := "/api/v1/metrics/query/promql/" + project.ID.String()
	var limited metrics_querying.PromQLQueryResponseDTO
	test_utils.MakePostRequestAndUnmarshal(t, router, url, "Bearer "+owner.Token,
		metrics_querying.PromQLQueryRequestDTO{Query: "check_up", Instant: true, End: &now},
		http.StatusOK, &limited)
	assert.Len(t, limited.Series, 100)
	assert.True(t, limited.Truncated)

	var all metrics_querying.PromQLQueryResponseDTO
	test_utils.MakePostRequestAndUnmarshal(t, router, url, "Bearer "+owner.Token,
		metrics_querying.PromQLQueryRequestDTO{Query: "check_up", Instant: true, End: &now, MaxSeries: 1000},
		http.StatusOK, &all)
	assert.Len(t, all.Series, 150)
	assert.False(t, all.Truncated)
}
