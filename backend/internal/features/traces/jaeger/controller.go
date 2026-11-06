package traces_jaeger

import (
	"net/http"
	"strconv"
	"time"

	spans_core "obzly/internal/features/spans/core"
	"obzly/internal/query"
	"obzly/internal/util/logger"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// JaegerController exposes the Jaeger HTTP query API at
// /api/v1/jaeger/:projectId/api/... (per project: see the package doc).
type JaegerController struct {
	service *JaegerService
}

func (c *JaegerController) RegisterRoutes(router gin.IRouter) {
	jaegerRoutes := router.Group("/api/v1/jaeger/:projectId/api")

	jaegerRoutes.GET("/services", c.ListServices)
	jaegerRoutes.GET("/services/:service/operations", c.ListOperations)
	// Pre-2.x Jaeger clients use /operations?service=X.
	jaegerRoutes.GET("/operations", c.ListOperationsLegacy)
	jaegerRoutes.GET("/traces", c.SearchTraces)
	jaegerRoutes.GET("/traces/:traceId", c.GetTrace)
}

func (c *JaegerController) ListServices(ctx *gin.Context) {
	projectID, ok := parseProjectID(ctx)
	if !ok {
		return
	}

	services, err := c.service.ListServices(projectID, apiKeyFromRequest(ctx))
	if err != nil {
		writeError(ctx, err)
		return
	}

	writeData(ctx, services)
}

func (c *JaegerController) ListOperations(ctx *gin.Context) {
	projectID, ok := parseProjectID(ctx)
	if !ok {
		return
	}

	operations, err := c.service.ListOperations(projectID, apiKeyFromRequest(ctx), ctx.Param("service"))
	if err != nil {
		writeError(ctx, err)
		return
	}

	writeData(ctx, operations)
}

func (c *JaegerController) ListOperationsLegacy(ctx *gin.Context) {
	projectID, ok := parseProjectID(ctx)
	if !ok {
		return
	}

	operations, err := c.service.ListOperations(projectID, apiKeyFromRequest(ctx), ctx.Query("service"))
	if err != nil {
		writeError(ctx, err)
		return
	}

	writeData(ctx, operations)
}

func (c *JaegerController) SearchTraces(ctx *gin.Context) {
	projectID, ok := parseProjectID(ctx)
	if !ok {
		return
	}

	request := &spans_core.ListTracesRequestDTO{
		ServiceName:   ctx.Query("service"),
		OperationName: ctx.Query("operation"),
		Limit:         intQuery(ctx, "limit", 20),
	}

	if from, to, ok := microsecondRangeFromQuery(ctx); ok {
		request.TimeRange = &spans_core.TimeRangeDTO{From: &from, To: &to}
	}
	if minDuration, ok := durationMsQuery(ctx, "minDuration"); ok {
		request.MinDurationMs = &minDuration
	}
	if maxDuration, ok := durationMsQuery(ctx, "maxDuration"); ok {
		request.MaxDurationMs = &maxDuration
	}
	// "tags" filtering is unsupported and ignored rather than rejected, so
	// a client with a tag filter still gets (unfiltered) results.

	traces, err := c.service.SearchTraces(ctx.Request.Context(), projectID, apiKeyFromRequest(ctx), request)
	if err != nil {
		writeError(ctx, err)
		return
	}

	writeData(ctx, traces)
}

func (c *JaegerController) GetTrace(ctx *gin.Context) {
	projectID, ok := parseProjectID(ctx)
	if !ok {
		return
	}

	trace, err := c.service.GetTrace(projectID, apiKeyFromRequest(ctx), ctx.Param("traceId"))
	if err != nil {
		writeError(ctx, err)
		return
	}

	writeData(ctx, []Trace{*trace})
}

func parseProjectID(ctx *gin.Context) (uuid.UUID, bool) {
	projectID, err := uuid.Parse(ctx.Param("projectId"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, Response{Errors: []string{"invalid project ID"}})
		return uuid.Nil, false
	}
	return projectID, true
}

// apiKeyFromRequest accepts the X-API-Key header or an "?api_key=" query
// param: some Jaeger clients can only add query params, not headers.
func apiKeyFromRequest(ctx *gin.Context) string {
	if apiKey := ctx.GetHeader("X-API-Key"); apiKey != "" {
		return apiKey
	}
	return ctx.Query("api_key")
}

func writeData(ctx *gin.Context, data any) {
	ctx.JSON(http.StatusOK, Response{Data: data})
}

func writeError(ctx *gin.Context, err error) {
	if message, refused := query.BudgetRefusal(err); refused {
		ctx.JSON(http.StatusUnprocessableEntity, Response{Errors: []string{message}})
		return
	}
	switch err {
	case errUnauthenticated:
		ctx.JSON(http.StatusUnauthorized, Response{Errors: []string{err.Error()}})
	case errTraceNotFound:
		ctx.JSON(http.StatusNotFound, Response{Errors: []string{err.Error()}})
	default:
		// Internal errors can carry file paths: log them, never return them.
		logger.GetLogger().Error("jaeger query failed", "error", err)
		ctx.JSON(http.StatusInternalServerError, Response{Errors: []string{"internal server error"}})
	}
}

func intQuery(ctx *gin.Context, name string, fallback int) int {
	raw := ctx.Query(name)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

// microsecondRangeFromQuery reads Jaeger's "start"/"end" query params
// (microseconds since the Unix epoch).
func microsecondRangeFromQuery(ctx *gin.Context) (from, to time.Time, ok bool) {
	startStr := ctx.Query("start")
	endStr := ctx.Query("end")
	if startStr == "" && endStr == "" {
		return time.Time{}, time.Time{}, false
	}

	// An unparsable start/end must not become the zero time (year 1): that
	// searched all history and broke the storage layer's timestamps.
	if startStr != "" {
		micros, err := strconv.ParseInt(startStr, 10, 64)
		if err != nil {
			return time.Time{}, time.Time{}, false
		}
		from = time.UnixMicro(micros).UTC()
	}
	if endStr != "" {
		micros, err := strconv.ParseInt(endStr, 10, 64)
		if err != nil {
			return time.Time{}, time.Time{}, false
		}
		to = time.UnixMicro(micros).UTC()
	}
	return from, to, true
}

// durationMsQuery reads a Jaeger duration query param ("100ms", "1s") as
// milliseconds.
func durationMsQuery(ctx *gin.Context, name string) (float64, bool) {
	raw := ctx.Query(name)
	if raw == "" {
		return 0, false
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return 0, false
	}
	return float64(parsed.Microseconds()) / 1000.0, true
}
