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
