package system_healthcheck

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type HealthcheckController struct {
	healthcheckService *HealthcheckService
}

func (c *HealthcheckController) RegisterRoutes(router *gin.RouterGroup) {
	router.GET("/system/health", c.CheckHealth)
	router.GET("/system/ready", c.CheckReady)
	router.GET("/system/live", c.CheckLive)
}

// CheckHealth
// @Summary Check system health
// @Description Check if the system is healthy by testing database connection
// @Tags system/health
// @Produce json
// @Success 200 {object} HealthcheckResponse
// @Failure 503 {object} HealthcheckResponse
// @Router /system/health [get]
func (c *HealthcheckController) CheckHealth(ctx *gin.Context) {
	err := c.healthcheckService.IsHealthy()

	if err == nil {
		ctx.JSON(
			http.StatusOK,
			HealthcheckResponse{
				Status: "Application is healthy, internal DB working fine and disk usage is below 95%. You can connect downdetector to this endpoint",
			},
		)
		return
	}

	ctx.JSON(http.StatusServiceUnavailable, HealthcheckResponse{Status: err.Error()})
}

// CheckReady
// @Summary Check readiness to receive traffic
// @Description Kubernetes readiness probe: 503 as soon as the process receives SIGTERM, so the load balancer stops sending it requests while it drains; otherwise the same database and disk checks as /system/health
// @Tags system/health
// @Produce json
// @Success 200 {object} HealthcheckResponse
// @Failure 503 {object} HealthcheckResponse
// @Router /system/ready [get]
func (c *HealthcheckController) CheckReady(ctx *gin.Context) {
	if err := c.healthcheckService.IsReady(); err != nil {
		ctx.JSON(http.StatusServiceUnavailable, HealthcheckResponse{Status: err.Error()})
		return
	}
	ctx.JSON(http.StatusOK, HealthcheckResponse{Status: "ready"})
}

// CheckLive
// @Summary Check the process is alive
// @Description Kubernetes liveness probe: 200 while the HTTP server answers. It checks no dependency, so a database outage does not restart every replica.
// @Tags system/health
// @Produce json
// @Success 200 {object} HealthcheckResponse
// @Router /system/live [get]
func (c *HealthcheckController) CheckLive(ctx *gin.Context) {
	ctx.JSON(http.StatusOK, HealthcheckResponse{Status: "alive"})
}
