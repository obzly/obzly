package traces_querying

import (
	"errors"
	"net/http"
	"time"

	spans_core "obzly/internal/features/spans/core"
	users_middleware "obzly/internal/features/users/middleware"
	"obzly/internal/query"
	"obzly/internal/util/logger"
	"obzly/internal/util/querylimit"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type TraceQueryController struct {
	traceQueryService *TraceQueryService
}

func (c *TraceQueryController) RegisterRoutes(router *gin.RouterGroup) {
	queryRoutes := router.Group("/traces/query")

	queryRoutes.POST("/list/:projectId", c.ListTraces)
	queryRoutes.GET("/get/:projectId/:traceId", c.GetTrace)
	queryRoutes.GET("/services/:projectId", c.ListServices)
	queryRoutes.GET("/operations/:projectId", c.ListOperations)
	queryRoutes.GET("/stats/:projectId", c.GetProjectStats)
}

// GetProjectStats
// @Summary Get project trace/span statistics
// @Description Get statistics about spans for a project including total count, size, and time range
// @Tags traces-query
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID"
// @Success 200 {object} spans_core.SpansStatsDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /traces/query/stats/{projectId} [get]
func (c *TraceQueryController) GetProjectStats(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	projectID, ok := parseProjectID(ctx)
	if !ok {
		return
	}

	response, err := c.traceQueryService.GetProjectStats(projectID, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, response)
}

// ListTraces
// @Summary Search traces
// @Description Search for traces matching service/operation/duration/error filters within a time range
// @Tags traces-query
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID"
// @Param request body spans_core.ListTracesRequestDTO true "Search filters"
// @Success 200 {object} spans_core.ListTracesResponseDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 429 {object} map[string]string
// @Router /traces/query/list/{projectId} [post]
func (c *TraceQueryController) ListTraces(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	projectID, ok := parseProjectID(ctx)
	if !ok {
		return
	}

	var request spans_core.ListTracesRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	response, err := c.traceQueryService.ListTraces(ctx.Request.Context(), projectID, &request, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, response)
}

// GetTrace
// @Summary Get one trace's spans
// @Tags traces-query
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID"
// @Param traceId path string true "Trace ID (hex)"
// @Param from query string false "Range start, RFC3339"
// @Param to query string false "Range end, RFC3339"
// @Success 200 {object} spans_core.GetTraceResponseDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 429 {object} map[string]string
// @Router /traces/query/get/{projectId}/{traceId} [get]
func (c *TraceQueryController) GetTrace(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	projectID, ok := parseProjectID(ctx)
	if !ok {
		return
	}
	traceID := ctx.Param("traceId")

	response, err := c.traceQueryService.GetTrace(projectID, traceID, timeRangeFromQuery(ctx), user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, response)
}

// ListServices
// @Summary List services seen in a project's traces
// @Tags traces-query
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID"
// @Param from query string false "Range start, RFC3339"
// @Param to query string false "Range end, RFC3339"
// @Success 200 {object} spans_core.ListServicesResponseDTO
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /traces/query/services/{projectId} [get]
func (c *TraceQueryController) ListServices(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	projectID, ok := parseProjectID(ctx)
	if !ok {
		return
	}

	response, err := c.traceQueryService.ListServices(projectID, timeRangeFromQuery(ctx), user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, response)
}

// ListOperations
// @Summary List operation names for one service
// @Tags traces-query
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID"
// @Param service query string true "Service name"
// @Param from query string false "Range start, RFC3339"
// @Param to query string false "Range end, RFC3339"
// @Success 200 {object} spans_core.ListOperationsResponseDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /traces/query/operations/{projectId} [get]
func (c *TraceQueryController) ListOperations(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	projectID, ok := parseProjectID(ctx)
	if !ok {
		return
	}

	serviceName := ctx.Query("service")
	if serviceName == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "service query parameter is required"})
		return
	}

	response, err := c.traceQueryService.ListOperations(projectID, serviceName, timeRangeFromQuery(ctx), user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, response)
}

func parseProjectID(ctx *gin.Context) (uuid.UUID, bool) {
	projectID, err := uuid.Parse(ctx.Param("projectId"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid project ID"})
		return uuid.Nil, false
	}
	return projectID, true
}

// timeRangeFromQuery reads optional RFC3339 "from"/"to" query params; nil
// when neither is set, meaning unbounded.
func timeRangeFromQuery(ctx *gin.Context) *spans_core.TimeRangeDTO {
	fromStr := ctx.Query("from")
	toStr := ctx.Query("to")
	if fromStr == "" && toStr == "" {
		return nil
	}

	timeRange := &spans_core.TimeRangeDTO{}
	if fromStr != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, fromStr); err == nil {
			timeRange.From = &parsed
		}
	}
	if toStr != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, toStr); err == nil {
			timeRange.To = &parsed
		}
	}
	return timeRange
}

func (c *TraceQueryController) handleError(ctx *gin.Context, err error) {
	if message, refused := query.BudgetRefusal(err); refused {
		ctx.JSON(http.StatusUnprocessableEntity, gin.H{"error": message, "code": "QUERY_TOO_LARGE"})
		return
	}
	if errors.Is(err, querylimit.ErrTooManyQueries) {
		ctx.JSON(http.StatusTooManyRequests, gin.H{"error": err.Error()})
		return
	}
	switch err.Error() {
	case "insufficient permissions to query project traces":
		ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	default:
		// Internal errors can carry file paths: log them, never return them.
		logger.GetLogger().Error("trace query failed", "error", err)
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "failed to execute query"})
	}
}
