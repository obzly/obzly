package system_upgradestatus

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
	router.GET("/system/upgrade-status", users_middleware.RequireRole(users_enums.UserRoleAdmin), c.GetStatus)
}

// GetStatus
// @Summary Whether it's safe to deploy another version (admin)
// @Description Database schema version vs the migrations this build ships, progress of background segment rewrites, and segments still waiting for upload or compaction - with a verdict on whether deploying the next version now would cut that work short. A control plane reports only its schema.
// @Tags system
// @Produce json
// @Security BearerAuth
// @Success 200 {object} Response
// @Failure 403 {object} map[string]string
// @Router /system/upgrade-status [get]
func (c *Controller) GetStatus(ctx *gin.Context) {
	response, err := c.service.GetStatus()
	if err != nil {
		logger.GetLogger().Error("failed to build upgrade status", "error", err)
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read upgrade status"})
		return
	}
	ctx.JSON(http.StatusOK, response)
}
