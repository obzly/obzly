package control_plane_controllers

import (
	"errors"
	"net/http"

	control_plane_dto "obzly/internal/features/control_plane/dto"
	control_plane_models "obzly/internal/features/control_plane/models"
	control_plane_services "obzly/internal/features/control_plane/services"
	users_middleware "obzly/internal/features/users/middleware"
	users_repositories "obzly/internal/features/users/repositories"
	"obzly/internal/util/logger"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// GrantController manages site_grants (who can reach which Site, at which role).
// Central-ADMIN-only; enforced in GrantService so direct service calls can't skip it.
type GrantController struct {
	grantService   *control_plane_services.GrantService
	userRepository *users_repositories.UserRepository
}

func (c *GrantController) RegisterRoutes(protected *gin.RouterGroup) {
	protected.GET("/control-plane/admin/sites/:id/grants", c.ListForSite)
	protected.POST("/control-plane/admin/grants", c.CreateGrant)
	protected.DELETE("/control-plane/admin/grants/:id", c.DeleteGrant)
}

func toGrantDTO(grant *control_plane_models.SiteGrant, email string) control_plane_dto.GrantResponseDTO {
	return control_plane_dto.GrantResponseDTO{
		ID: grant.ID, UserID: grant.UserID, UserEmail: email, SiteID: grant.SiteID,
		ProjectID: grant.ProjectID, Role: grant.Role, CreatedAt: grant.CreatedAt,
		SSOManaged: grant.SSOConnectorID != nil,
	}
}

// ListForSite
// @Summary List grants for a Site (admin)
// @Tags control-plane
// @Produce json
// @Param id path string true "Site ID"
// @Success 200
// @Failure 403
// @Router /control-plane/admin/sites/{id}/grants [get]
func (c *GrantController) ListForSite(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
		return
	}

	grants, err := c.grantService.ListForSite(user, ctx.Param("id"))
	if err != nil {
		writeGrantError(ctx, err)
		return
	}

	response := make([]control_plane_dto.GrantResponseDTO, 0, len(grants))
	for _, grant := range grants {
		email := ""
		if granted, err := c.userRepository.GetUserByID(grant.UserID); err == nil && granted != nil {
			email = granted.Email
		}
		response = append(response, toGrantDTO(grant, email))
	}
	ctx.JSON(http.StatusOK, response)
}

// CreateGrant
// @Summary Grant a user access to a Site (admin)
// @Tags control-plane
// @Accept json
// @Produce json
// @Param request body control_plane_dto.CreateGrantRequestDTO true "Grant"
// @Success 200
// @Failure 400
// @Failure 403
// @Router /control-plane/admin/grants [post]
func (c *GrantController) CreateGrant(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
		return
	}

	var request control_plane_dto.CreateGrantRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid request format"})
		return
	}

	grant, err := c.grantService.CreateGrant(user, request)
	if err != nil {
		writeGrantError(ctx, err)
		return
	}

	email := ""
	if granted, err := c.userRepository.GetUserByID(grant.UserID); err == nil && granted != nil {
		email = granted.Email
	}
	ctx.JSON(http.StatusOK, toGrantDTO(grant, email))
}

// DeleteGrant
// @Summary Revoke a Site grant (admin)
// @Tags control-plane
// @Produce json
// @Param id path string true "Grant ID"
// @Success 200
// @Failure 403
// @Router /control-plane/admin/grants/{id} [delete]
func (c *GrantController) DeleteGrant(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
		return
	}

	grantID, err := uuid.Parse(ctx.Param("id"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid grant id"})
		return
	}

	if _, err := c.grantService.DeleteGrant(user, grantID); err != nil {
		writeGrantError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"success": true})
}

// writeGrantError returns sentinel messages as-is; anything else is logged and returned generically.
func writeGrantError(ctx *gin.Context, err error) {
	switch {
	case errors.Is(err, control_plane_services.ErrGrantForbidden),
		errors.Is(err, control_plane_services.ErrGrantSiteAdminRootOnly):
		ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	case errors.Is(err, control_plane_services.ErrGrantInvalidRole):
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	default:
		logger.GetLogger().Error("grant management request failed", "error", err)
		ctx.JSON(
			http.StatusBadRequest,
			gin.H{"error": "request failed - check that the user and site exist and the grant isn't a duplicate"},
		)
	}
}
