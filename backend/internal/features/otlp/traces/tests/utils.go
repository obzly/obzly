package otlp_traces_tests

import (
	"time"

	api_keys "obzly/internal/features/api_keys"
	otlp_traces "obzly/internal/features/otlp/traces"
	projects_controllers "obzly/internal/features/projects/controllers"
	projects_testing "obzly/internal/features/projects/testing"
	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	users_middleware "obzly/internal/features/users/middleware"
	users_services "obzly/internal/features/users/services"
	users_testing "obzly/internal/features/users/testing"

	"github.com/gin-gonic/gin"

	collectortracev1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
)

// CreateOtlpTestRouter mounts the OTLP HTTP receiver (auth by X-API-Key in the
// service) and the protected project/API key controllers for fixtures.
func CreateOtlpTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	otlp_traces.GetHTTPController().RegisterRoutes(router)

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
	Router    *gin.Engine
	User      *users_dto.SignInResponseDTO
	ApiKey    *api_keys.ApiKey
	ProjectID string
}

func SetUpFixture(name string) *TestFixture {
	router := CreateOtlpTestRouter()
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project := projects_testing.CreateTestProject(name, user, router)
	apiKey := api_keys.CreateTestApiKey("OTLP Traces Test Key", project.ID, user.Token, router)

	return &TestFixture{
		Router:    router,
		User:      user,
		ApiKey:    apiKey,
		ProjectID: project.ID.String(),
	}
}

// BuildExportRequest builds a minimal valid ExportTraceServiceRequest with one
// span.
func BuildExportRequest(spanName string) *collectortracev1.ExportTraceServiceRequest {
	now := time.Now().UTC()
	return &collectortracev1.ExportTraceServiceRequest{
		ResourceSpans: []*tracev1.ResourceSpans{
			{
				Resource: &resourcev1.Resource{
					Attributes: []*commonv1.KeyValue{
						{
							Key: "service.name",
							Value: &commonv1.AnyValue{
								Value: &commonv1.AnyValue_StringValue{StringValue: "test-service"},
							},
						},
					},
				},
				ScopeSpans: []*tracev1.ScopeSpans{
					{
						Spans: []*tracev1.Span{
							{
								TraceId:           []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
								SpanId:            []byte{1, 2, 3, 4, 5, 6, 7, 8},
								Name:              spanName,
								Kind:              tracev1.Span_SPAN_KIND_SERVER,
								StartTimeUnixNano: uint64(now.UnixNano()),
								EndTimeUnixNano:   uint64(now.Add(10 * time.Millisecond).UnixNano()),
							},
						},
					},
				},
			},
		},
	}
}
