package downdetect

import (
	"net/http"

	"obzly/internal/util/logger"

	"github.com/gin-gonic/gin"
)

type DowndetectController struct {
	service *DowndetectService
}

func (c *DowndetectController) RegisterRoutes(router *gin.RouterGroup) {
	router.GET("/downdetect/is-available", c.IsAvailable)
}

// @Summary Check API availability
// @Description Checks if the API service is available
// @Tags downdetect
// @Accept json
// @Produce json
// @Success 200
// @Failure 500
// @Router /downdetect/is-available [get]
func (c *DowndetectController) IsAvailable(ctx *gin.Context) {
	err := c.service.IsAvailable()
	if err != nil {
		// Log the real error server-side only; driver errors can carry DSN/host details
		// and this endpoint is unauthenticated.
		logger.GetLogger().Error("downdetect check failed", "error", err)
		ctx.JSON(
			http.StatusInternalServerError,
			gin.H{"error": "one or more backend services are not available"},
		)
		return
	}

	ctx.JSON(
		http.StatusOK,
		gin.H{"message": "API, DB (PostgreSQL), logs storage and cache (Valkey) are available"},
	)
}
