package dashboards_controllers_tests

import (
	"fmt"
	"testing"
	"time"

	dashboards_controllers "obzly/internal/features/dashboards/controllers"
	logs_core "obzly/internal/features/logs/core"
	logs_querying_tests "obzly/internal/features/logs/querying/tests"
	logs_receiving "obzly/internal/features/logs/receiving"
	metrics_core "obzly/internal/features/metrics/core"
	projects_controllers "obzly/internal/features/projects/controllers"
	projects_models "obzly/internal/features/projects/models"
	projects_testing "obzly/internal/features/projects/testing"
	spans_core "obzly/internal/features/spans/core"
	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	users_middleware "obzly/internal/features/users/middleware"
	users_services "obzly/internal/features/users/services"
	users_testing "obzly/internal/features/users/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// CreateDashboardTestRouter includes the log receiving routes so tests can
// seed real log data.
func CreateDashboardTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	v1 := router.Group("/api/v1")
	logs_receiving.GetReceivingController().RegisterRoutes(v1)

	protected := v1.Group("").Use(users_middleware.AuthMiddleware(users_services.GetUserService()))
	if routerGroup, ok := protected.(*gin.RouterGroup); ok {
		projects_controllers.GetProjectController().RegisterRoutes(routerGroup)
		projects_controllers.GetMembershipController().RegisterRoutes(routerGroup)
		dashboards_controllers.GetDashboardController().RegisterRoutes(routerGroup)
	}

	return router
}

// SetUpProjectWithLogs creates a user and project, seeds logs over HTTP and
// flushes them so panel queries see them without polling.
func SetUpProjectWithLogs(
	t *testing.T,
	router *gin.Engine,
	testName string,
) (owner *users_dto.SignInResponseDTO, project *projects_models.Project, uniqueID string) {
	t.Helper()

	owner = users_testing.CreateTestUser(users_enums.UserRoleMember)
	uniqueID = uuid.New().String()
	projectName := fmt.Sprintf("%s %s", testName, uniqueID[:8])
	project, _ = projects_testing.CreateTestProjectWithToken(projectName, owner.Token, router)

	infoMessages := []string{"user logged in", "request handled", "cache warmed"}
	errorMessages := []string{"connection refused", "timeout exceeded"}

	infoItems := logs_querying_tests.CreateLogItemsWithMessages(
		uniqueID, infoMessages, logs_core.LogLevelInfo, nil,
	)
	errorItems := logs_querying_tests.CreateLogItemsWithMessages(
		uniqueID, errorMessages, logs_core.LogLevelError, nil,
	)

	logs_querying_tests.SubmitLogsAndProcess(t, router, project.ID, infoItems)
	logs_querying_tests.SubmitLogsAndProcess(t, router, project.ID, errorItems)

	require.NoError(t, logs_core.GetLogCoreRepository().ForceFlush())

	return owner, project, uniqueID
}

// SeedSpans stores two services' spans (one erroring) directly, bypassing
// OTLP ingestion.
func SeedSpans(t *testing.T, projectID uuid.UUID) {
	t.Helper()

	base := time.Now().UTC().Truncate(time.Millisecond)
	spanList := []*spans_core.Span{
		{
			ID: uuid.New(), ProjectID: projectID, TraceID: uuid.NewString(), SpanID: "s1",
			Name: "GET /checkout", ServiceName: "gateway",
			StartTime: base, EndTime: base.Add(50 * time.Millisecond),
			StatusCode: spans_core.StatusCodeOK,
		},
		{
			ID: uuid.New(), ProjectID: projectID, TraceID: uuid.NewString(), SpanID: "s2",
			Name: "POST /charge", ServiceName: "payments",
			StartTime: base, EndTime: base.Add(200 * time.Millisecond),
			StatusCode: spans_core.StatusCodeError,
		},
	}

	entries := map[uuid.UUID][]*spans_core.Span{projectID: spanList}
	require.NoError(t, spans_core.GetSpanCoreRepository().StoreSpansBatch(entries))
	require.NoError(t, spans_core.GetSpanCoreRepository().ForceFlush())
}

// SeedMetrics stores one counter and one gauge directly, bypassing ingestion.
func SeedMetrics(t *testing.T, projectID uuid.UUID) {
	t.Helper()

	base := time.Now().UTC().Add(-5 * time.Minute)
	var samples []*metrics_core.MetricSample
	for i := 0; i < 3; i++ {
		ts := base.Add(time.Duration(i) * time.Minute)
		samples = append(samples,
			&metrics_core.MetricSample{
				ID: uuid.New(), ProjectID: projectID, MetricName: "http_requests_total",
				Labels: map[string]string{"service": "api"}, Value: float64(10 * (i + 1)),
				Timestamp: ts, Type: metrics_core.MetricTypeCounter,
			},
			&metrics_core.MetricSample{
				ID: uuid.New(), ProjectID: projectID, MetricName: "cpu_usage",
				Labels: map[string]string{"service": "api"}, Value: 0.5,
				Timestamp: ts, Type: metrics_core.MetricTypeGauge,
			},
		)
	}

	entries := map[uuid.UUID][]*metrics_core.MetricSample{projectID: samples}
	require.NoError(t, metrics_core.GetMetricCoreRepository().StoreSamplesBatch(entries))
	require.NoError(t, metrics_core.GetMetricCoreRepository().ForceFlush())
}
