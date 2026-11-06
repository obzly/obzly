package servicemap

import (
	"net/http"
	"time"

	users_middleware "obzly/internal/features/users/middleware"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// ServiceMapController serves "/service-map": it merges two signals, so it
// has no single-signal route prefix.
type ServiceMapController struct {
	service *ServiceMapService
}

func (c *ServiceMapController) RegisterRoutes(router *gin.RouterGroup) {
	router.GET("/service-map/:projectId", c.GetServiceMap)
}

// GetServiceMap
// @Summary Get the merged service map
// @Description Merges span-derived service-call edges with flow-derived service edges into one graph
// @Tags service-map
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID"
// @Param from query string false "Range start, RFC3339"
// @Param to query string false "Range end, RFC3339"
// @Success 200 {object} servicemap.ServiceMapResponseDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /service-map/{projectId} [get]
func (c *ServiceMapController) GetServiceMap(ctx *gin.Context) {
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

	from, to := parseTimeRangeQuery(ctx)

	response, err := c.service.GetServiceMap(projectID, from, to, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, response)
}

// parseTimeRangeQuery returns plain times: the two signal packages each
// define their own TimeRangeDTO.
func parseTimeRangeQuery(ctx *gin.Context) (*time.Time, *time.Time) {
	var from, to *time.Time
	if fromStr := ctx.Query("from"); fromStr != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, fromStr); err == nil {
			from = &parsed
		}
	}
	if toStr := ctx.Query("to"); toStr != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, toStr); err == nil {
			to = &parsed
		}
	}
	return from, to
}

func (c *ServiceMapController) handleError(ctx *gin.Context, err error) {
	switch err.Error() {
	case "insufficient permissions to query project service map":
		ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	default:
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	}
}
