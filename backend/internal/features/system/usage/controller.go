package system_usage

import (
	"net/http"

	users_enums "obzly/internal/features/users/enums"
	users_middleware "obzly/internal/features/users/middleware"
	"obzly/internal/util/logger"

	"github.com/gin-gonic/gin"
)

type Controller struct {
	service *Service
}

func (c *Controller) RegisterRoutes(router *gin.RouterGroup) {
	router.GET("/system/usage", users_middleware.RequireRole(users_enums.UserRoleAdmin), c.GetUsage)
}

// GetUsage
// @Summary Data received per hour and data kept, by signal (admin)
// @Description For Settings → Overview: uncompressed bytes and rows that arrived in each of the last 24 hours (by the hour of each segment's newest row, so approximate after compaction), and compressed and uncompressed bytes kept with the oldest row, for logs, metrics and traces. Cached for a minute.
// @Tags system
// @Produce json
// @Security BearerAuth
// @Success 200 {object} Response
// @Failure 403 {object} map[string]string
// @Router /system/usage [get]
func (c *Controller) GetUsage(ctx *gin.Context) {
	response, err := c.service.GetUsage()
	if err != nil {
		logger.GetLogger().Error("failed to read data usage", "error", err)
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read data usage"})
		return
	}
	ctx.JSON(http.StatusOK, response)
}
