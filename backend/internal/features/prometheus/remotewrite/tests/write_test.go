package prometheus_remotewrite_tests

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	api_keys "obzly/internal/features/api_keys"
	metrics_core "obzly/internal/features/metrics/core"
	projects_controllers "obzly/internal/features/projects/controllers"
	projects_testing "obzly/internal/features/projects/testing"
	prometheus_remotewrite "obzly/internal/features/prometheus/remotewrite"
	users_enums "obzly/internal/features/users/enums"
	users_middleware "obzly/internal/features/users/middleware"
	users_services "obzly/internal/features/users/services"
	users_testing "obzly/internal/features/users/testing"

	"github.com/gin-gonic/gin"
	"github.com/gogo/protobuf/proto"
	"github.com/golang/snappy"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/prometheus/prometheus/prompb"
)

func createTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	prometheus_remotewrite.GetHTTPController().RegisterRoutes(router)

	v1 := router.Group("/api/v1")
	protected := v1.Group("").Use(users_middleware.AuthMiddleware(users_services.GetUserService()))
	if routerGroup, ok := protected.(*gin.RouterGroup); ok {
		projects_controllers.GetProjectController().RegisterRoutes(routerGroup)
		projects_controllers.GetMembershipController().RegisterRoutes(routerGroup)
		api_keys.GetApiKeyController().RegisterRoutes(routerGroup)
	}

	return router
}

func setUpFixture(name string) (router *gin.Engine, apiKey *api_keys.ApiKey, projectID uuid.UUID) {
	router = createTestRouter()
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project := projects_testing.CreateTestProject(name, user, router)
	key := api_keys.CreateTestApiKey("Remote Write Test Key", project.ID, user.Token, router)
	return router, key, project.ID
}

func postRemoteWrite(
	t *testing.T,
	router *gin.Engine,
	apiKey string,
	request *prompb.WriteRequest,
) *httptest.ResponseRecorder {
	t.Helper()

	payload, err := proto.Marshal(request)
	require.NoError(t, err)
	compressed := snappy.Encode(nil, payload)

	req, err := http.NewRequest(http.MethodPost, "/api/v1/write", bytes.NewReader(compressed))
	require.NoError(t, err)
	req.Header.Set("Content-Encoding", "snappy")
	req.Header.Set("Content-Type", "application/x-protobuf")
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder
}

func Test_RemoteWrite_WithValidApiKey_Accepted(t *testing.T) {
	router, apiKey, projectID := setUpFixture("Remote Write Valid")
	now := time.Now().UTC()

	request := &prompb.WriteRequest{
		Timeseries: []prompb.TimeSeries{
			{
				Labels: []prompb.Label{
					{Name: "__name__", Value: "node_cpu_seconds_total"},
					{Name: "mode", Value: "idle"},
				},
				Samples: []prompb.Sample{{Value: 99.5, Timestamp: now.UnixMilli()}},
			},
		},
	}

	recorder := postRemoteWrite(t, router, apiKey.Token, request)
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())

	require.NoError(t, metrics_core.GetMetricCoreRepository().ForceFlush())

	from := now.Add(-time.Hour)
	to := now.Add(time.Hour)
	response, err := metrics_core.GetMetricCoreRepository().
		SelectSeries(context.Background(), projectID, &metrics_core.SelectSeriesRequestDTO{
			Matchers: []metrics_core.MatcherDTO{
				{Name: "__name__", Type: metrics_core.MatchEqual, Value: "node_cpu_seconds_total"},
			},
			TimeRange: &metrics_core.TimeRangeDTO{From: &from, To: &to},
		})
	require.NoError(t, err)
	require.Len(t, response.Series, 1)
	assert.Equal(t, "idle", response.Series[0].Labels["mode"])
	require.Len(t, response.Series[0].Samples, 1)
	assert.InDelta(t, 99.5, response.Series[0].Samples[0].Value, 0.0001)
}

func Test_RemoteWrite_WithoutApiKey_Unauthorized(t *testing.T) {
	router, _, _ := setUpFixture("Remote Write No Key")

	request := &prompb.WriteRequest{
		Timeseries: []prompb.TimeSeries{
			{
				Labels:  []prompb.Label{{Name: "__name__", Value: "up"}},
				Samples: []prompb.Sample{{Value: 1, Timestamp: time.Now().UnixMilli()}},
			},
		},
	}

	recorder := postRemoteWrite(t, router, "", request)
	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
}

func Test_RemoteWrite_WithInvalidApiKey_Unauthorized(t *testing.T) {
	router, _, _ := setUpFixture("Remote Write Bad Key")

	request := &prompb.WriteRequest{
		Timeseries: []prompb.TimeSeries{
			{
				Labels:  []prompb.Label{{Name: "__name__", Value: "up"}},
				Samples: []prompb.Sample{{Value: 1, Timestamp: time.Now().UnixMilli()}},
			},
		},
	}

	recorder := postRemoteWrite(t, router, "lb_not-a-real-key", request)
	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
}

func Test_RemoteWrite_BearerAuthorizationHeader_Accepted(t *testing.T) {
	router, apiKey, _ := setUpFixture("Remote Write Bearer")

	payload, err := proto.Marshal(&prompb.WriteRequest{
		Timeseries: []prompb.TimeSeries{
			{
				Labels:  []prompb.Label{{Name: "__name__", Value: "up"}},
				Samples: []prompb.Sample{{Value: 1, Timestamp: time.Now().UnixMilli()}},
			},
		},
	})
	require.NoError(t, err)
	compressed := snappy.Encode(nil, payload)

	req, err := http.NewRequest(http.MethodPost, "/api/v1/write", bytes.NewReader(compressed))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+apiKey.Token)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	assert.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())
}
