package system_healthcheck_test

import (
	"net/http"
	"os"
	"syscall"
	"testing"
	"time"

	"obzly/internal/config"
	system_healthcheck "obzly/internal/features/system/healthcheck"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// One test, because the shutdown signal it sends can't be undone for the
// rest of this test binary.
func Test_Probes_WhenSIGTERMReceived_ReadyFailsAndLiveKeepsPassing(t *testing.T) {
	router := newRouter()

	test_utils.MakeGetRequest(t, router, "/api/v1/system/live", "", http.StatusOK)
	test_utils.MakeGetRequest(t, router, "/api/v1/system/ready", "", http.StatusOK)

	config.StartListeningForShutdownSignal()
	require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGTERM))
	require.Eventually(t, config.IsShouldShutdown, 5*time.Second, 10*time.Millisecond)

	var response system_healthcheck.HealthcheckResponse
	test_utils.MakeGetRequestAndUnmarshal(
		t, router, "/api/v1/system/ready", "", http.StatusServiceUnavailable, &response,
	)
	require.Equal(t, "shutting down", response.Status)
	test_utils.MakeGetRequest(t, router, "/api/v1/system/live", "", http.StatusOK)
	test_utils.MakeGetRequest(t, router, "/api/v1/system/health", "", http.StatusOK)
}

func newRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	system_healthcheck.GetHealthcheckController().RegisterRoutes(router.Group("/api/v1"))
	return router
}
