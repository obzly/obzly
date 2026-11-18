package system_upgradestatus

import (
	"net/http"

	users_enums "obzly/internal/features/users/enums"
	users_middleware "obzly/internal/features/users/middleware"
	"obzly/internal/util/logger"

	"github.com/gin-gonic/gin"
)

type Controller struct {
	service  *Service
	releases *ReleaseChecker
}

func (c *Controller) RegisterRoutes(router *gin.RouterGroup) {
	router.GET("/system/upgrade-status", users_middleware.RequireRole(users_enums.UserRoleAdmin), c.GetStatus)
	router.GET("/system/latest-release", users_middleware.RequireRole(users_enums.UserRoleAdmin),
		c.GetLatestRelease)
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

// GetLatestRelease
// @Summary The latest Obzly release (admin)
// @Description The newest release on GitHub (version, date, link), asked server-side with an anonymous request and cached for 6 hours. status is known, unknown (the check failed) or disabled (UPDATE_CHECK=false); neither of the last two means up to date.
// @Tags system
// @Produce json
// @Security BearerAuth
// @Param refresh query bool false "Ask GitHub again if the answer is over a minute old"
// @Success 200 {object} LatestReleaseDTO
// @Failure 403 {object} map[string]string
// @Router /system/latest-release [get]
func (c *Controller) GetLatestRelease(ctx *gin.Context) {
	ctx.JSON(http.StatusOK, c.releases.Latest(ctx.Request.Context(), ctx.Query("refresh") == "true"))
}
