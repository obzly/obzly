package ingestion

import (
	"errors"
	"net/http"

	users_middleware "obzly/internal/features/users/middleware"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type Controller struct {
	service *Service
}

func (c *Controller) RegisterRoutes(router *gin.RouterGroup) {
	router.GET("/projects/:id/ingestion-status", c.GetIngestionStatus)
}

// GetIngestionStatus
// @Summary List the sources sending to a project
// @Description Each source (identified by obzly_source, else Kubernetes workload, else service and host) with its signals, last seen, recent rate and health. Sources seen in the last 7 days; refreshed about every 30 seconds.
// @Tags ingestion
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID (UUID format)"
// @Success 200 {object} ingestion.IngestionStatusDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /projects/{id}/ingestion-status [get]
func (c *Controller) GetIngestionStatus(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	projectID, err := uuid.Parse(ctx.Param("id"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid project ID format"})
		return
	}
	status, err := c.service.GetStatus(ctx.Request.Context(), user, projectID)
	switch {
	case errors.Is(err, ErrForbidden):
		ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	case err != nil:
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get ingestion status"})
	default:
		ctx.JSON(http.StatusOK, status)
	}
}
