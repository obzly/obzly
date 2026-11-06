package metrics_querying_tests

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	metrics_core "obzly/internal/features/metrics/core"
	metrics_querying "obzly/internal/features/metrics/querying"
	projects_controllers "obzly/internal/features/projects/controllers"
	projects_testing "obzly/internal/features/projects/testing"
	users_enums "obzly/internal/features/users/enums"
	users_middleware "obzly/internal/features/users/middleware"
	users_services "obzly/internal/features/users/services"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	v1 := router.Group("/api/v1")
	protected := v1.Group("").Use(users_middleware.AuthMiddleware(users_services.GetUserService()))
	if routerGroup, ok := protected.(*gin.RouterGroup); ok {
		projects_controllers.GetProjectController().RegisterRoutes(routerGroup)
		projects_controllers.GetMembershipController().RegisterRoutes(routerGroup)
		metrics_querying.GetMetricQueryController().RegisterRoutes(routerGroup)
	}

	return router
}

func Test_ListMetricNames_ReturnsSeededNames(t *testing.T) {
	router := createTestRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Metric Names", owner.Token, router)

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
	}
	require.NoError(t, metrics_core.GetMetricCoreRepository().StoreSamplesBatch(
		map[uuid.UUID][]*metrics_core.MetricSample{project.ID: samples},
	))
	require.NoError(t, metrics_core.GetMetricCoreRepository().ForceFlush())

	var response metrics_core.ListMetricNamesResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t, router,
		fmt.Sprintf("/api/v1/metrics/query/names/%s", project.ID.String()),
		"Bearer "+owner.Token, http.StatusOK, &response,
	)

	names := make([]string, len(response.Metrics))
	for i, m := range response.Metrics {
		names[i] = m.Name
	}
	assert.ElementsMatch(t, []string{"http_requests_total", "cpu_usage"}, names)
}

func Test_ListMetricNames_NonMember_ReturnsForbidden(t *testing.T) {
	router := createTestRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Metric Names Forbidden", owner.Token, router)
	outsider := users_testing.CreateTestUser(users_enums.UserRoleMember)

	test_utils.MakeGetRequest(
		t, router,
		fmt.Sprintf("/api/v1/metrics/query/names/%s", project.ID.String()),
		"Bearer "+outsider.Token, http.StatusForbidden,
	)
}

func Test_ListLabelValues_AcceptsDottedNames(t *testing.T) {
	router := createTestRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Label Values", owner.Token, router)

	samples := []*metrics_core.MetricSample{
		{
			ID:         uuid.New(),
			ProjectID:  project.ID,
			MetricName: "k8s_pod_cpu_usage",
			Labels:     map[string]string{"k8s_pod_name": "api-1"},
			Value:      1,
			Timestamp:  time.Now().UTC(),
			Type:       metrics_core.MetricTypeGauge,
		},
		{
			ID:         uuid.New(),
			ProjectID:  project.ID,
			MetricName: "k8s_node_cpu_usage",
			Labels:     map[string]string{"k8s_pod_name": "other"},
			Value:      1,
			Timestamp:  time.Now().UTC(),
			Type:       metrics_core.MetricTypeGauge,
		},
	}
	require.NoError(t, metrics_core.GetMetricCoreRepository().StoreSamplesBatch(
		map[uuid.UUID][]*metrics_core.MetricSample{project.ID: samples},
	))
	require.NoError(t, metrics_core.GetMetricCoreRepository().ForceFlush())

	for _, matcher := range []metrics_core.MatcherDTO{
		{Name: "__name__", Type: metrics_core.MatchEqual, Value: "k8s.pod.cpu.usage"},
		{Name: "__name__", Type: metrics_core.MatchRegexp, Value: `k8s\.pod\..*`},
	} {
		var response metrics_core.ListLabelValuesResponseDTO
		test_utils.MakePostRequestAndUnmarshal(
			t, router,
			fmt.Sprintf("/api/v1/metrics/query/label-values/%s", project.ID.String()),
			"Bearer "+owner.Token,
			metrics_core.ListLabelValuesRequestDTO{
				LabelName: "resource.k8s.pod.name",
				Matchers:  []metrics_core.MatcherDTO{matcher},
			},
			http.StatusOK, &response,
		)
		assert.Equal(t, []string{"api-1"}, response.Values, matcher.Value)
	}
}
