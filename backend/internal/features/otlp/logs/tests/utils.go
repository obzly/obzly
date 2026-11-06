package otlp_logs_tests

import (
	"time"

	api_keys "obzly/internal/features/api_keys"
	otlp_logs "obzly/internal/features/otlp/logs"
	projects_controllers "obzly/internal/features/projects/controllers"
	projects_testing "obzly/internal/features/projects/testing"
	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	users_middleware "obzly/internal/features/users/middleware"
	users_services "obzly/internal/features/users/services"
	users_testing "obzly/internal/features/users/testing"

	"github.com/gin-gonic/gin"

	collectorlogsv1 "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
)

// CreateOtlpTestRouter mounts the OTLP HTTP receiver (auth by X-API-Key in the
// service) and the protected project/API key controllers for fixtures.
func CreateOtlpTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	otlp_logs.GetHTTPController().RegisterRoutes(router)

	v1 := router.Group("/api/v1")
	protected := v1.Group("").Use(users_middleware.AuthMiddleware(users_services.GetUserService()))

	if routerGroup, ok := protected.(*gin.RouterGroup); ok {
		projects_controllers.GetProjectController().RegisterRoutes(routerGroup)
		projects_controllers.GetMembershipController().RegisterRoutes(routerGroup)
		api_keys.GetApiKeyController().RegisterRoutes(routerGroup)
	}

	return router
}

// TestFixture is a project and an active API key for it; OTLP always needs one.
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
	apiKey := api_keys.CreateTestApiKey("OTLP Test Key", project.ID, user.Token, router)

	return &TestFixture{
		Router:    router,
		User:      user,
		ApiKey:    apiKey,
		ProjectID: project.ID.String(),
	}
}

// BuildExportRequest builds a minimal valid ExportLogsServiceRequest with one
// log record.
func BuildExportRequest(message string) *collectorlogsv1.ExportLogsServiceRequest {
	return &collectorlogsv1.ExportLogsServiceRequest{
		ResourceLogs: []*logsv1.ResourceLogs{
			{
				ScopeLogs: []*logsv1.ScopeLogs{
					{
						LogRecords: []*logsv1.LogRecord{
							{
								TimeUnixNano:   uint64(time.Now().UnixNano()),
								SeverityNumber: logsv1.SeverityNumber_SEVERITY_NUMBER_INFO,
								Body: &commonv1.AnyValue{
									Value: &commonv1.AnyValue_StringValue{StringValue: message},
								},
							},
						},
					},
				},
			},
		},
	}
}
