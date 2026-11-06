package control_plane_controllers

import (
	"errors"
	"net/http"

	control_plane_services "obzly/internal/features/control_plane/services"
	users_middleware "obzly/internal/features/users/middleware"

	"github.com/gin-gonic/gin"
)

type SigningKeyController struct {
	signingKeyService *control_plane_services.SigningKeyService
}

func (c *SigningKeyController) RegisterRoutes(protected *gin.RouterGroup) {
	protected.POST("/control-plane/admin/signing-keys/rotate", c.Rotate)
}

// Rotate
// @Summary Rotate the control plane's assertion-signing key (root admin)
// @Tags control-plane
// @Produce json
// @Success 200
// @Failure 403
// @Router /control-plane/admin/signing-keys/rotate [post]
func (c *SigningKeyController) Rotate(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
		return
	}

	if err := c.signingKeyService.Rotate(user); err != nil {
		if errors.Is(err, control_plane_services.ErrSigningKeyForbidden) {
			ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "failed to rotate signing key"})
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"success": true})
}
