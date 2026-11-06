package telemetry_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"obzly/internal/telemetry"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func Test_GinMiddleware_LabelsByRouteTemplateNotRawPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(telemetry.GinMiddleware())
	router.GET("/telemetry-test/projects/:projectId", func(ctx *gin.Context) { ctx.Status(http.StatusTeapot) })

	for _, id := range []string{"a", "b", "c"} {
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/telemetry-test/projects/"+id, nil))
	}
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/telemetry-test/nope/xyz", nil))

	require.Equal(t, 3, histogramCount(t, "GET", "/telemetry-test/projects/:projectId", "418"))
	require.Positive(t, histogramCount(t, "GET", "unmatched", "404"))
	require.Zero(t, histogramCount(t, "GET", "/telemetry-test/projects/a", "418"),
		"raw paths must never become label values")
}

func Test_Serve_ExposesRegistryAtMetrics(t *testing.T) {
	telemetry.BuildInfo.WithLabelValues("test-version", "all").Set(1)

	listener, err := telemetry.Listen("127.0.0.1:0")
	require.NoError(t, err)
	srv := telemetry.Serve(listener, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer telemetry.Shutdown(srv)

	resp, err := http.Get("http://" + listener.Addr().String() + "/metrics")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), `obzly_build_info{mode="all",version="test-version"} 1`)
	require.Contains(t, string(body), "go_goroutines")
}

func histogramCount(t *testing.T, method, route, status string) int {
	t.Helper()
	families, err := telemetry.Registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != "obzly_http_request_duration_seconds" {
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := map[string]string{}
			for _, pair := range metric.GetLabel() {
				labels[pair.GetName()] = pair.GetValue()
			}
			if labels["method"] == method && labels["route"] == route && labels["status"] == status {
				return int(metric.GetHistogram().GetSampleCount())
			}
		}
	}
	return 0
}
