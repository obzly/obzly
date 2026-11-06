// Package sso_controllers exposes admin CRUD over SSO connectors. The public
// list and callback routes live on UserController, which owns account find-or-create.
package sso_controllers

import (
	"errors"
	"net/http"

	users_enums "obzly/internal/features/users/enums"
	mfa_services "obzly/internal/features/users/mfa/services"
	user_middleware "obzly/internal/features/users/middleware"
	sso_dto "obzly/internal/features/users/sso/dto"
	sso_services "obzly/internal/features/users/sso/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type ConnectorController struct {
	connectorService *sso_services.ConnectorService
}

func (c *ConnectorController) RegisterRoutes(router *gin.RouterGroup) {
	routes := router.Group("/sso/connectors", user_middleware.RequireRole(users_enums.UserRoleAdmin))

	routes.GET("", c.ListConnectors)
	routes.POST("", c.CreateConnector)
	routes.PUT("/:id", c.UpdateConnector)
	routes.DELETE("/:id", c.DeleteConnector)
	routes.GET("/:id/group-mappings", c.ListGroupMappings)
	routes.POST("/:id/group-mappings", c.CreateGroupMapping)
	routes.DELETE("/:id/group-mappings/:mappingId", c.DeleteGroupMapping)
}

// ListConnectors
// @Summary List SSO connectors
// @Description List every configured OIDC SSO connector (admin only) - client secrets are never included
// @Tags sso
// @Produce json
// @Security BearerAuth
// @Success 200 {object} sso_dto.ListConnectorsResponseDTO
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /sso/connectors [get]
func (c *ConnectorController) ListConnectors(ctx *gin.Context) {
	user, ok := user_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	response, err := c.connectorService.ListConnectors(user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, response)
}

// CreateConnector
// @Summary Create an SSO connector
// @Description Configure a new OIDC SSO connector (admin only) - validates the issuer via live OIDC discovery
// @Tags sso
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body sso_dto.CreateConnectorRequestDTO true "Connector configuration"
// @Success 200 {object} sso_dto.ConnectorResponseDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /sso/connectors [post]
func (c *ConnectorController) CreateConnector(ctx *gin.Context) {
	user, ok := user_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	var request sso_dto.CreateConnectorRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	response, err := c.connectorService.CreateConnector(&request, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, response)
}

// UpdateConnector
// @Summary Update an SSO connector
// @Description Update an existing OIDC SSO connector (admin only) - leave clientSecret empty to keep the existing one
// @Tags sso
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Connector ID"
// @Param request body sso_dto.UpdateConnectorRequestDTO true "Connector configuration"
// @Success 200 {object} sso_dto.ConnectorResponseDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /sso/connectors/{id} [put]
func (c *ConnectorController) UpdateConnector(ctx *gin.Context) {
	user, ok := user_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	id, err := uuid.Parse(ctx.Param("id"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid connector ID"})
		return
	}

	var request sso_dto.UpdateConnectorRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	response, err := c.connectorService.UpdateConnector(id, &request, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, response)
}

// DeleteConnector
// @Summary Delete an SSO connector
// @Description Delete an OIDC SSO connector (admin only) - users already linked to it keep their accounts, just lose this sign-in method
// @Tags sso
// @Produce json
// @Security BearerAuth
// @Param id path string true "Connector ID"
// @Param request body object false "{\"mfaCode\": \"123456\"} - required when the caller has MFA enabled"
// @Success 200 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 422 {object} map[string]string
// @Router /sso/connectors/{id} [delete]
func (c *ConnectorController) DeleteConnector(ctx *gin.Context) {
	user, ok := user_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	id, err := uuid.Parse(ctx.Param("id"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid connector ID"})
		return
	}

	// Optional body: users with MFA enabled must send {"mfaCode": "123456"}.
	var request struct {
		MFACode *string `json:"mfaCode"`
	}
	_ = ctx.ShouldBindJSON(&request)

	if err := c.connectorService.DeleteConnector(id, user, request.MFACode); err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "Connector deleted successfully"})
}

func (c *ConnectorController) handleError(ctx *gin.Context, err error) {
	switch {
	case errors.Is(err, sso_services.ErrInsufficientPermissions),
		errors.Is(err, sso_services.ErrGlobalAdminRootOnly),
		errors.Is(err, sso_services.ErrConnectorRootOnly):
		ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	case errors.Is(err, sso_services.ErrMFACodeRequired), errors.Is(err, sso_services.ErrMFACodeInvalid):
		ctx.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
	case errors.Is(err, mfa_services.ErrTooManyAttempts):
		ctx.JSON(http.StatusTooManyRequests, gin.H{"error": err.Error()})
	case errors.Is(err, sso_services.ErrConnectorNotFound), errors.Is(err, sso_services.ErrGroupMappingNotFound):
		ctx.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	default:
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	}
}

// ListGroupMappings
// @Summary List a connector's group mappings
// @Description Which IdP groups grant which project role (admin only)
// @Tags sso
// @Produce json
// @Security BearerAuth
// @Param id path string true "Connector ID"
// @Success 200 {object} sso_dto.ListGroupMappingsResponseDTO
// @Router /sso/connectors/{id}/group-mappings [get]
func (c *ConnectorController) ListGroupMappings(ctx *gin.Context) {
	user, ok := user_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	connectorID, err := uuid.Parse(ctx.Param("id"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid connector ID"})
		return
	}

	response, err := c.connectorService.ListGroupMappings(connectorID, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, response)
}

// CreateGroupMapping
// @Summary Map an IdP group to a project role
// @Description Members of the group get the role in the project on their next sign-in (admin only). OWNER is not assignable.
// @Tags sso
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Connector ID"
// @Param request body sso_dto.CreateGroupMappingRequestDTO true "Mapping"
// @Success 200 {object} sso_dto.GroupMappingResponseDTO
// @Router /sso/connectors/{id}/group-mappings [post]
func (c *ConnectorController) CreateGroupMapping(ctx *gin.Context) {
	user, ok := user_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	connectorID, err := uuid.Parse(ctx.Param("id"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid connector ID"})
		return
	}

	var request sso_dto.CreateGroupMappingRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	response, err := c.connectorService.CreateGroupMapping(connectorID, &request, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, response)
}

// DeleteGroupMapping
// @Summary Delete a group mapping
// @Description Memberships it created are removed the next time each affected user signs in (admin only)
// @Tags sso
// @Produce json
// @Security BearerAuth
// @Param id path string true "Connector ID"
// @Param mappingId path string true "Mapping ID"
// @Success 200 {object} map[string]string
// @Router /sso/connectors/{id}/group-mappings/{mappingId} [delete]
func (c *ConnectorController) DeleteGroupMapping(ctx *gin.Context) {
	user, ok := user_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	connectorID, err := uuid.Parse(ctx.Param("id"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid connector ID"})
		return
	}
	mappingID, err := uuid.Parse(ctx.Param("mappingId"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid mapping ID"})
		return
	}

	if err := c.connectorService.DeleteGroupMapping(connectorID, mappingID, user); err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "Group mapping deleted successfully"})
}
