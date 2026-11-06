package datamigration

import (
	"net/http"

	users_middleware "obzly/internal/features/users/middleware"

	"github.com/gin-gonic/gin"
)

type Controller struct{}

func GetController() *Controller {
	return &Controller{}
}

func (c *Controller) RegisterRoutes(router *gin.RouterGroup) {
	router.GET("/system/data-migration", c.GetStatus)
}

// GetStatus
// @Summary Progress of the attribute-name data migration (admin)
// @Tags system
// @Produce json
// @Security BearerAuth
// @Success 200 {object} map[string]any
// @Failure 403 {object} map[string]string
// @Router /system/data-migration [get]
func (c *Controller) GetStatus(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok || !user.CanUpdateSettings() {
		ctx.JSON(http.StatusForbidden, gin.H{"error": "insufficient permissions"})
		return
	}
	statuses, err := GetWorker().Status()
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read migration status"})
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"streams": statuses})
}
