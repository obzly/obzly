package system_upgradestatus

import (
	"net/http"
	"os"
	"testing"

	logs_testsupport "obzly/internal/features/logs/testsupport"
	metrics_testsupport "obzly/internal/features/metrics/testsupport"
	spans_testsupport "obzly/internal/features/spans/testsupport"
	users_enums "obzly/internal/features/users/enums"
	users_middleware "obzly/internal/features/users/middleware"
	users_services "obzly/internal/features/users/services"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	logs_testsupport.SetupRepository()
	metrics_testsupport.SetupRepository()
	spans_testsupport.SetupRepository()
	os.Exit(m.Run())
}

func createTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	protected := router.Group("/api/v1")
	protected.Use(users_middleware.AuthMiddleware(users_services.GetUserService()))
	GetController().RegisterRoutes(protected)
	return router
}

func Test_GetUpgradeStatus_WhenAdmin_ReportsMigratedSchemaAndBacklog(t *testing.T) {
	router := createTestRouter()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	var response Response
	test_utils.MakeGetRequestAndUnmarshal(
		t, router, "/api/v1/system/upgrade-status", "Bearer "+admin.Token, http.StatusOK, &response,
	)

	// The test database is migrated by goose before the suite runs, so the
	// schema must match the migrations directory this binary sees.
	assert.Equal(t, SchemaUpToDate, response.Schema.State)
	assert.Positive(t, response.Schema.CurrentVersion)
	assert.Equal(t, response.Schema.LatestAvailable, response.Schema.CurrentVersion)
	assert.NotEmpty(t, response.Schema.Recent)

	require.NotNil(t, response.Rewrites)
	assert.Len(t, response.Rewrites.Streams, 3)
	assert.Len(t, response.Segments, 3)
	for _, backlog := range response.Segments {
		assert.GreaterOrEqual(t, backlog.Total, backlog.SmallSegments)
		if !response.ObjectStorageEnabled {
			assert.Nil(t, backlog.PendingUpload, "pending upload is meaningless without object storage")
		}
	}
}

func Test_GetUpgradeStatus_WhenMember_ReturnsForbidden(t *testing.T) {
	router := createTestRouter()
	member := users_testing.CreateTestUser(users_enums.UserRoleMember)

	test_utils.MakeGetRequest(t, router, "/api/v1/system/upgrade-status", "Bearer "+member.Token, http.StatusForbidden)
}

func Test_GetUpgradeStatus_WithoutAuth_ReturnsUnauthorized(t *testing.T) {
	router := createTestRouter()

	test_utils.MakeGetRequest(t, router, "/api/v1/system/upgrade-status", "", http.StatusUnauthorized)
}
