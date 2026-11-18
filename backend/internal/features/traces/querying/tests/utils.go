package traces_querying_tests

import (
	"testing"
	"time"

	projects_controllers "obzly/internal/features/projects/controllers"
	projects_models "obzly/internal/features/projects/models"
	projects_testing "obzly/internal/features/projects/testing"
	spans_core "obzly/internal/features/spans/core"
	traces_querying "obzly/internal/features/traces/querying"
	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	users_middleware "obzly/internal/features/users/middleware"
	users_services "obzly/internal/features/users/services"
	users_testing "obzly/internal/features/users/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func CreateTraceQueryTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	v1 := router.Group("/api/v1")
	protected := v1.Group("").Use(users_middleware.AuthMiddleware(users_services.GetUserService()))
	if routerGroup, ok := protected.(*gin.RouterGroup); ok {
		projects_controllers.GetProjectController().RegisterRoutes(routerGroup)
		projects_controllers.GetMembershipController().RegisterRoutes(routerGroup)
		traces_querying.GetTraceQueryController().RegisterRoutes(routerGroup)
	}

	return router
}

func SetUpProjectWithTrace(
	t *testing.T,
	router *gin.Engine,
	testName string,
) (owner *users_dto.SignInResponseDTO, project *projects_models.Project, traceID string) {
	t.Helper()

	owner = users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ = projects_testing.CreateTestProjectWithToken(testName, owner.Token, router)

	traceID = uuid.NewString()
	base := time.Now().UTC().Truncate(time.Millisecond)
	spanList := []*spans_core.Span{
		{
			ID: uuid.New(), ProjectID: project.ID, TraceID: traceID, SpanID: "s1",
			Name: "GET /checkout", ServiceName: "gateway",
			StartTime: base, EndTime: base.Add(100 * time.Millisecond),
			StatusCode: spans_core.StatusCodeOK,
		},
		{
			ID: uuid.New(), ProjectID: project.ID, TraceID: traceID, SpanID: "s2", ParentSpanID: "s1",
			Name: "POST /charge", ServiceName: "payments",
			StartTime: base.Add(10 * time.Millisecond), EndTime: base.Add(60 * time.Millisecond),
			StatusCode: spans_core.StatusCodeError, StatusMessage: "declined",
		},
	}

	entries := map[uuid.UUID][]*spans_core.Span{project.ID: spanList}
	require.NoError(t, spans_core.GetSpanCoreRepository().StoreSpansBatch(entries))
	require.NoError(t, spans_core.GetSpanCoreRepository().ForceFlush())

	return owner, project, traceID
}

// SetUpProjectWithSummarySpans seeds, relative to now: 4 traces in the last
// 10 minutes (gateway root SERVER spans of 10/20/30/40 ms, each calling
// payments; trace 1's payments span and trace 3's root fail), 2 traces in
// the 10 minutes before (50 ms roots), and one trace 30 minutes ago.
func SetUpProjectWithSummarySpans(
	t *testing.T,
	router *gin.Engine,
	testName string,
	now time.Time,
) (owner *users_dto.SignInResponseDTO, project *projects_models.Project) {
	t.Helper()

	owner = users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ = projects_testing.CreateTestProjectWithToken(testName, owner.Token, router)

	var spanList []*spans_core.Span
	addTrace := func(at time.Time, rootMs int, rootStatus, childStatus spans_core.StatusCode) {
		traceID := uuid.NewString()
		spanList = append(spanList,
			&spans_core.Span{
				ID: uuid.New(), ProjectID: project.ID, TraceID: traceID, SpanID: "root",
				Name: "GET /checkout", ServiceName: "gateway", Kind: spans_core.SpanKindServer,
				StartTime: at, EndTime: at.Add(time.Duration(rootMs) * time.Millisecond), StatusCode: rootStatus,
			},
			&spans_core.Span{
				ID: uuid.New(), ProjectID: project.ID, TraceID: traceID, SpanID: "child", ParentSpanID: "root",
				Name: "POST /charge", ServiceName: "payments", Kind: spans_core.SpanKindServer,
				StartTime: at.Add(time.Millisecond), EndTime: at.Add(6 * time.Millisecond), StatusCode: childStatus,
			},
		)
	}

	ok, failed := spans_core.StatusCodeOK, spans_core.StatusCodeError
	addTrace(now.Add(-9*time.Minute), 10, ok, ok)
	addTrace(now.Add(-8*time.Minute), 20, ok, failed)
	addTrace(now.Add(-7*time.Minute), 30, ok, ok)
	addTrace(now.Add(-6*time.Minute), 40, failed, ok)
	addTrace(now.Add(-15*time.Minute), 50, ok, ok)
	addTrace(now.Add(-14*time.Minute), 50, ok, ok)
	addTrace(now.Add(-30*time.Minute), 70, failed, failed)

	require.NoError(t, spans_core.GetSpanCoreRepository().StoreSpansBatch(
		map[uuid.UUID][]*spans_core.Span{project.ID: spanList},
	))
	require.NoError(t, spans_core.GetSpanCoreRepository().ForceFlush())

	return owner, project
}

// SetUpProjectWithManyRootSpans seeds count root spans in the last 5 minutes.
func SetUpProjectWithManyRootSpans(
	t *testing.T,
	router *gin.Engine,
	testName string,
	now time.Time,
	count int,
) (owner *users_dto.SignInResponseDTO, project *projects_models.Project) {
	t.Helper()

	owner = users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ = projects_testing.CreateTestProjectWithToken(testName, owner.Token, router)

	spanList := make([]*spans_core.Span, 0, count)
	for i := range count {
		at := now.Add(-5 * time.Minute).Add(time.Duration(i) * 10 * time.Millisecond)
		spanList = append(spanList, &spans_core.Span{
			ID: uuid.New(), ProjectID: project.ID, TraceID: uuid.NewString(), SpanID: "root",
			Name: "GET /", ServiceName: "gateway", Kind: spans_core.SpanKindServer,
			StartTime: at, EndTime: at.Add(time.Millisecond), StatusCode: spans_core.StatusCodeOK,
		})
	}
	require.NoError(t, spans_core.GetSpanCoreRepository().StoreSpansBatch(
		map[uuid.UUID][]*spans_core.Span{project.ID: spanList},
	))
	require.NoError(t, spans_core.GetSpanCoreRepository().ForceFlush())

	return owner, project
}
