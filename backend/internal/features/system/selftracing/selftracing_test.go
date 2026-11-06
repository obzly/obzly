package system_selftracing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	spans_core "obzly/internal/features/spans/core"
	"obzly/internal/telemetry"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace/noop"
)

// A sampled W3C parent a client could send to try to force tracing.
const sampledTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

func Test_GinRequest_WhenSampled_StoresRootServerSpanWithoutQueryString(t *testing.T) {
	store, projectID := startCapturing(t, 1)
	router := newRouter()

	serve(router, "/items/42?api_key=secret-value", sampledTraceparent)
	serve(router, "/fail", "")
	spans := store.flush(t)

	require.Len(t, spans, 2)
	ok, failed := spans[0], spans[1]
	if ok.Name != "GET /items/:id" {
		ok, failed = failed, ok
	}
	require.Equal(t, "GET /items/:id", ok.Name)
	require.Equal(t, projectID, ok.ProjectID)
	require.Equal(t, ServiceName, ok.ServiceName)
	require.Equal(t, spans_core.SpanKindServer, ok.Kind)
	require.Empty(t, ok.ParentSpanID, "a caller's traceparent is never adopted")
	require.NotEqual(t, "4bf92f3577b34da6a3ce929d0e0e4736", ok.TraceID)
	require.Equal(t, "/items/:id", ok.Attributes["http_route"])
	require.EqualValues(t, 200, ok.Attributes["http_response_status_code"])
	require.Equal(t, spans_core.StatusCodeUnset, ok.StatusCode)
	require.NotContains(t, spanText(ok), "secret-value", "no query string is recorded")
	require.Equal(t, ServiceName, ok.ResourceAttributes["service_name"])

	require.Equal(t, "GET /fail", failed.Name)
	require.Equal(t, spans_core.StatusCodeError, failed.StatusCode)
}

func Test_GinRequest_AtRateZero_SampledTraceparentCannotForceTracing(t *testing.T) {
	store, _ := startCapturing(t, 0)
	router := newRouter()

	for i := 0; i < 20; i++ {
		serve(router, "/items/1", sampledTraceparent)
	}

	require.Empty(t, store.flush(t))
}

type capturingStore struct {
	mu      sync.Mutex
	spans   []*spans_core.Span
	tracing *Tracing
}

func (s *capturingStore) flush(t *testing.T) []*spans_core.Span {
	t.Helper()
	require.NoError(t, s.tracing.provider.ForceFlush(context.Background()))
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spans
}

func startCapturing(t *testing.T, rate float64) (*capturingStore, uuid.UUID) {
	t.Helper()
	store := &capturingStore{}
	projectID := uuid.New()
	store.tracing = startWithStore(
		Options{ProjectID: projectID, SampleRate: rate},
		func(spans []*spans_core.Span) error {
			store.mu.Lock()
			defer store.mu.Unlock()
			store.spans = append(store.spans, spans...)
			return nil
		},
	)
	t.Cleanup(func() {
		_ = store.tracing.Shutdown(context.Background())
		otel.SetTracerProvider(noop.NewTracerProvider())
	})
	return store, projectID
}

func newRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(telemetry.GinMiddleware())
	router.GET("/items/:id", func(ctx *gin.Context) { ctx.Status(http.StatusOK) })
	router.GET("/fail", func(ctx *gin.Context) { ctx.Status(http.StatusInternalServerError) })
	return router
}

func serve(router *gin.Engine, target, traceparent string) {
	request := httptest.NewRequest(http.MethodGet, target, nil)
	if traceparent != "" {
		request.Header.Set("traceparent", traceparent)
	}
	router.ServeHTTP(httptest.NewRecorder(), request)
}

func spanText(span *spans_core.Span) string {
	text := span.Name
	for key, value := range span.Attributes {
		text += " " + key + "=" + toString(value)
	}
	return text
}

func toString(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}
