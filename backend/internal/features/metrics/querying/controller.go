package metrics_querying

import (
	"errors"
	"net/http"

	metrics_core "obzly/internal/features/metrics/core"
	users_middleware "obzly/internal/features/users/middleware"
	"obzly/internal/util/logger"
	"obzly/internal/util/querylimit"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type MetricQueryController struct {
	metricQueryService *MetricQueryService
}

func (c *MetricQueryController) RegisterRoutes(router *gin.RouterGroup) {
	router.GET("/metrics/query/names/:projectId", c.ListMetricNames)
	router.POST("/metrics/query/label-values/:projectId", c.ListLabelValues)
	router.GET("/metrics/query/stats/:projectId", c.GetProjectStats)
	router.POST("/metrics/query/promql/:projectId", c.PromQLQuery)
}

// GetProjectStats
// @Summary Get project metric statistics
// @Description Get statistics about samples for a project including total count, size, and time range
// @Tags metrics-query
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID"
// @Success 200 {object} metrics_core.MetricsStatsDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /metrics/query/stats/{projectId} [get]
func (c *MetricQueryController) GetProjectStats(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	projectID, err := uuid.Parse(ctx.Param("projectId"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid project ID"})
		return
	}

	response, err := c.metricQueryService.GetProjectStats(projectID, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, response)
}

// ListMetricNames
// @Summary List available metric names
// @Description Lists every distinct metric name recorded for a project - used by the dashboard panel editor to suggest names while writing a PromQL query
// @Tags metrics-query
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID"
// @Success 200 {object} metrics_core.ListMetricNamesResponseDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /metrics/query/names/{projectId} [get]
func (c *MetricQueryController) ListMetricNames(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	projectID, err := uuid.Parse(ctx.Param("projectId"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid project ID"})
		return
	}

	response, err := c.metricQueryService.ListMetricNames(ctx.Request.Context(), projectID, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, response)
}

// ListLabelValues
// @Summary List distinct values of a metric label
// @Description Lists every distinct value recorded for a label name (e.g. "k8s_namespace_name") within a project - used by the dashboard filter bar
// @Tags metrics-query
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID"
// @Param request body metrics_core.ListLabelValuesRequestDTO true "Label name to look up"
// @Success 200 {object} metrics_core.ListLabelValuesResponseDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /metrics/query/label-values/{projectId} [post]
func (c *MetricQueryController) ListLabelValues(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	projectID, err := uuid.Parse(ctx.Param("projectId"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid project ID"})
		return
	}

	var request metrics_core.ListLabelValuesRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	response, err := c.metricQueryService.ListLabelValues(ctx.Request.Context(), projectID, &request, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, response)
}

// PromQLQuery
// @Summary Run a PromQL range or instant query
// @Description Series are capped at 100; truncated is set when more matched.
// @Tags metrics
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID"
// @Param request body PromQLQueryRequestDTO true "PromQL query and time range"
// @Success 200 {object} PromQLQueryResponseDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 429 {object} map[string]string
// @Router /metrics/query/promql/{projectId} [post]
func (c *MetricQueryController) PromQLQuery(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	projectID, err := uuid.Parse(ctx.Param("projectId"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid project ID"})
		return
	}

	var request PromQLQueryRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "A PromQL query is required"})
		return
	}

	response, err := c.metricQueryService.PromQLQuery(ctx.Request.Context(), projectID, &request, user)
	if err != nil {
		if errors.Is(err, ErrInvalidPromQL) {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, response)
}

func (c *MetricQueryController) handleError(ctx *gin.Context, err error) {
	if errors.Is(err, querylimit.ErrTooManyQueries) {
		ctx.JSON(http.StatusTooManyRequests, gin.H{"error": err.Error()})
		return
	}
	switch err.Error() {
	case "insufficient permissions to access project metrics":
		ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	default:
		// Other errors may wrap internal details such as segment file
		// paths: log them, send the client a generic message.
		logger.GetLogger().Error("metrics query failed", "error", err)
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "failed to execute query"})
	}
}
