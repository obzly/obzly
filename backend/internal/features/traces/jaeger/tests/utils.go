package traces_jaeger_tests

import (
	"testing"
	"time"

	api_keys "obzly/internal/features/api_keys"
	projects_controllers "obzly/internal/features/projects/controllers"
	projects_models "obzly/internal/features/projects/models"
	projects_testing "obzly/internal/features/projects/testing"
	spans_core "obzly/internal/features/spans/core"
	traces_jaeger "obzly/internal/features/traces/jaeger"
	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	users_middleware "obzly/internal/features/users/middleware"
	users_services "obzly/internal/features/users/services"
	users_testing "obzly/internal/features/users/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// CreateJaegerTestRouter mounts the Jaeger controller without Bearer auth
// (it checks API keys itself), next to the controllers fixtures need.
func CreateJaegerTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	traces_jaeger.GetJaegerController().RegisterRoutes(router)

	v1 := router.Group("/api/v1")
	protected := v1.Group("").Use(users_middleware.AuthMiddleware(users_services.GetUserService()))
	if routerGroup, ok := protected.(*gin.RouterGroup); ok {
		projects_controllers.GetProjectController().RegisterRoutes(routerGroup)
		projects_controllers.GetMembershipController().RegisterRoutes(routerGroup)
		api_keys.GetApiKeyController().RegisterRoutes(routerGroup)
	}

	return router
}

type TestFixture struct {
	Router  *gin.Engine
	User    *users_dto.SignInResponseDTO
	Project *projects_models.Project
	ApiKey  *api_keys.ApiKey
	TraceID string
}

func SetUpFixtureWithTrace(t *testing.T, testName string) *TestFixture {
	t.Helper()

	router := CreateJaegerTestRouter()
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project := projects_testing.CreateTestProject(testName, user, router)
	apiKey := api_keys.CreateTestApiKey("Jaeger Test Key", project.ID, user.Token, router)

	traceID := uuid.NewString()
	base := time.Now().UTC().Truncate(time.Millisecond)
	spanList := []*spans_core.Span{
		{
			ID: uuid.New(), ProjectID: project.ID, TraceID: traceID, SpanID: "s1",
			Name: "GET /checkout", ServiceName: "gateway",
			StartTime: base, EndTime: base.Add(100 * time.Millisecond),
			StatusCode: spans_core.StatusCodeOK,
			Attributes: map[string]any{"http.method": "GET"},
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

	return &TestFixture{Router: router, User: user, Project: project, ApiKey: apiKey, TraceID: traceID}
}
