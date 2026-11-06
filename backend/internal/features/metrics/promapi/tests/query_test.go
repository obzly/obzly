package metrics_promapi_tests

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	api_keys "obzly/internal/features/api_keys"
	metrics_core "obzly/internal/features/metrics/core"
	metrics_promapi "obzly/internal/features/metrics/promapi"
	projects_controllers "obzly/internal/features/projects/controllers"
	projects_testing "obzly/internal/features/projects/testing"
	users_enums "obzly/internal/features/users/enums"
	users_middleware "obzly/internal/features/users/middleware"
	users_services "obzly/internal/features/users/services"
	users_testing "obzly/internal/features/users/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	metrics_promapi.GetController().RegisterRoutes(router)

	v1 := router.Group("/api/v1")
	protected := v1.Group("").Use(users_middleware.AuthMiddleware(users_services.GetUserService()))
	if routerGroup, ok := protected.(*gin.RouterGroup); ok {
		projects_controllers.GetProjectController().RegisterRoutes(routerGroup)
		projects_controllers.GetMembershipController().RegisterRoutes(routerGroup)
		api_keys.GetApiKeyController().RegisterRoutes(routerGroup)
	}

	return router
}

func setUpFixtureWithData(t *testing.T, name string) (router *gin.Engine, apiKey string, projectID uuid.UUID) {
	t.Helper()

	router = createTestRouter()
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project := projects_testing.CreateTestProject(name, user, router)
	key := api_keys.CreateTestApiKey("Promapi Test Key", project.ID, user.Token, router)

	base := time.Now().UTC().Add(-10 * time.Minute)
	var samples []*metrics_core.MetricSample
	for i := 0; i < 5; i++ {
		ts := base.Add(time.Duration(i) * time.Minute)
		samples = append(samples,
			&metrics_core.MetricSample{
				ID: uuid.New(), ProjectID: project.ID, MetricName: "http_requests_total",
				Labels: map[string]string{"service": "api"}, Value: float64(10 * (i + 1)),
				Timestamp: ts, Type: metrics_core.MetricTypeCounter,
			},
		)
	}
	require.NoError(t, metrics_core.GetMetricCoreRepository().StoreSamplesBatch(
		map[uuid.UUID][]*metrics_core.MetricSample{project.ID: samples},
	))
	require.NoError(t, metrics_core.GetMetricCoreRepository().ForceFlush())

	return router, key.Token, project.ID
}

type apiEnvelope struct {
	Status string          `json:"status"`
	Data   json.RawMessage `json:"data"`
	Error  string          `json:"error"`
}

func Test_InstantQuery_ReturnsVectorResult(t *testing.T) {
	router, apiKey, projectID := setUpFixtureWithData(t, "Promapi Instant Query")

	url := fmt.Sprintf("/api/v1/metrics/%s/api/v1/query?query=%s", projectID, "sum(http_requests_total)")
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	req.Header.Set("X-API-Key", apiKey)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	var envelope apiEnvelope
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	assert.Equal(t, "success", envelope.Status)
	assert.Contains(t, string(envelope.Data), `"resultType":"vector"`)
}

func Test_RangeQuery_ReturnsMatrixResult(t *testing.T) {
	router, apiKey, projectID := setUpFixtureWithData(t, "Promapi Range Query")

	now := time.Now().UTC()
	start := now.Add(-15 * time.Minute).Format(time.RFC3339)
	end := now.Format(time.RFC3339)

	url := fmt.Sprintf(
		"/api/v1/metrics/%s/api/v1/query_range?query=%s&start=%s&end=%s&step=60",
		projectID, "http_requests_total", start, end,
	)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	req.Header.Set("X-API-Key", apiKey)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	var envelope apiEnvelope
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	assert.Equal(t, "success", envelope.Status)
	assert.Contains(t, string(envelope.Data), `"resultType":"matrix"`)
}

// Test_RangeQuery_ExcessiveResolution_RejectedBeforeExecuting: a query over
// MaxRangeQueryPoints is rejected up front; the engine's limits miss it.
func Test_RangeQuery_ExcessiveResolution_RejectedBeforeExecuting(t *testing.T) {
	router, apiKey, projectID := setUpFixtureWithData(t, "Promapi Range Query Resolution Cap")

	now := time.Now().UTC()
	// 30 days at a 1 s step is ~2.6 million points.
	start := now.Add(-30 * 24 * time.Hour).Format(time.RFC3339)
	end := now.Format(time.RFC3339)

	url := fmt.Sprintf(
		"/api/v1/metrics/%s/api/v1/query_range?query=%s&start=%s&end=%s&step=1",
		projectID, "http_requests_total", start, end,
	)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	req.Header.Set("X-API-Key", apiKey)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusUnprocessableEntity, recorder.Code, recorder.Body.String())

	var envelope apiEnvelope
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	assert.Equal(t, "error", envelope.Status)
	assert.Contains(t, envelope.Error, "resolution")
}

func Test_LabelValues_ReturnsServiceValues(t *testing.T) {
	router, apiKey, projectID := setUpFixtureWithData(t, "Promapi Label Values")

	url := fmt.Sprintf("/api/v1/metrics/%s/api/v1/label/service/values", projectID)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	req.Header.Set("X-API-Key", apiKey)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	var envelope apiEnvelope
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	assert.Equal(t, "success", envelope.Status)

	var values []string
	require.NoError(t, json.Unmarshal(envelope.Data, &values))
	assert.Contains(t, values, "api")
}

func Test_Query_WithoutApiKey_Unauthorized(t *testing.T) {
	router, _, projectID := setUpFixtureWithData(t, "Promapi No Key")

	url := fmt.Sprintf("/api/v1/metrics/%s/api/v1/query?query=up", projectID)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
}

func Test_Query_WithInvalidSyntax_BadRequest(t *testing.T) {
	router, apiKey, projectID := setUpFixtureWithData(t, "Promapi Bad Syntax")

	url := fmt.Sprintf("/api/v1/metrics/%s/api/v1/query?query=%s", projectID, "sum(")
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	req.Header.Set("X-API-Key", apiKey)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	assert.Equal(t, http.StatusUnprocessableEntity, recorder.Code)
}
