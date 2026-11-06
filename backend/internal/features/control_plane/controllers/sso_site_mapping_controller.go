package control_plane_controllers

import (
	"errors"
	"net/http"

	control_plane_dto "obzly/internal/features/control_plane/dto"
	control_plane_services "obzly/internal/features/control_plane/services"
	users_middleware "obzly/internal/features/users/middleware"
	"obzly/internal/util/logger"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// SSOSiteMappingController manages SSO group -> Site mappings. Central-ADMIN only,
// enforced in the service.
type SSOSiteMappingController struct {
	service *control_plane_services.SSOSiteMappingService
}

func (c *SSOSiteMappingController) RegisterRoutes(protected *gin.RouterGroup) {
	routes := protected.Group("/control-plane/admin/sso/connectors/:connectorId/site-group-mappings")
	routes.GET("", c.List)
	routes.POST("", c.Create)
	routes.DELETE("/:mappingId", c.Delete)
}

// List
// @Summary List an SSO connector's group -> Site mappings (admin)
// @Tags control-plane
// @Produce json
// @Param connectorId path string true "SSO connector ID"
// @Success 200
// @Failure 403
// @Router /control-plane/admin/sso/connectors/{connectorId}/site-group-mappings [get]
func (c *SSOSiteMappingController) List(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
		return
	}
	connectorID, err := uuid.Parse(ctx.Param("connectorId"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid connector id"})
		return
	}
	mappings, err := c.service.List(user, connectorID)
	if err != nil {
		writeSSOSiteMappingError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"mappings": mappings})
}

// Create
// @Summary Map an SSO group to Site access (admin)
// @Tags control-plane
// @Accept json
// @Produce json
// @Param connectorId path string true "SSO connector ID"
// @Param request body control_plane_dto.CreateSSOSiteGroupMappingRequestDTO true "Mapping"
// @Success 200
// @Failure 400
// @Failure 403
// @Router /control-plane/admin/sso/connectors/{connectorId}/site-group-mappings [post]
func (c *SSOSiteMappingController) Create(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
		return
	}
	connectorID, err := uuid.Parse(ctx.Param("connectorId"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid connector id"})
		return
	}
	var request control_plane_dto.CreateSSOSiteGroupMappingRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid request format"})
		return
	}
	mapping, err := c.service.Create(
		user, connectorID, request.GroupName, request.SiteID, request.ProjectID, request.Role,
	)
	if err != nil {
		writeSSOSiteMappingError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, mapping)
}

// Delete
// @Summary Remove an SSO group -> Site mapping (admin)
// @Tags control-plane
// @Produce json
// @Param connectorId path string true "SSO connector ID"
// @Param mappingId path string true "Mapping ID"
// @Success 200
// @Failure 403
// @Failure 404
// @Router /control-plane/admin/sso/connectors/{connectorId}/site-group-mappings/{mappingId} [delete]
func (c *SSOSiteMappingController) Delete(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
		return
	}
	connectorID, err1 := uuid.Parse(ctx.Param("connectorId"))
	mappingID, err2 := uuid.Parse(ctx.Param("mappingId"))
	if err1 != nil || err2 != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if err := c.service.Delete(user, connectorID, mappingID); err != nil {
		writeSSOSiteMappingError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "mapping deleted"})
}

func writeSSOSiteMappingError(ctx *gin.Context, err error) {
	switch {
	case errors.Is(err, control_plane_services.ErrGrantForbidden),
		errors.Is(err, control_plane_services.ErrGrantSiteAdminRootOnly):
		ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	case errors.Is(err, control_plane_services.ErrSSOSiteMappingNotFound),
		errors.Is(err, control_plane_services.ErrSSOConnectorNotFound):
		ctx.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, control_plane_services.ErrSSOSiteMappingRole),
		errors.Is(err, control_plane_services.ErrSSOSiteMappingExists),
		errors.Is(err, control_plane_services.ErrSSOSiteMappingSite):
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	default:
		logger.GetLogger().Error("SSO site group mapping request failed", "error", err)
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	}
}
