package useraccess

import (
	"errors"
	"net/http"

	users_enums "obzly/internal/features/users/enums"
	users_middleware "obzly/internal/features/users/middleware"
	"obzly/internal/util/logger"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type Controller struct {
	service *Service
}

// RegisterRoutes mounts on the identity routes, next to the users management
// routes (same :id wildcard as GET /users/:id).
func (c *Controller) RegisterRoutes(router *gin.RouterGroup) {
	router.GET("/users/:id/detail", users_middleware.RequireRole(users_enums.UserRoleAdmin), c.GetUserDetail)
}

// GetUserDetail
// @Summary A user's sign-in methods and access (admin only)
// @Description For the Users page's side panel: the ways the account can sign in (password, SSO connectors, GitHub, Google), the organization whose SCIM created it, its organizations and teams, its control-plane Site grants (own, SSO-synced and through teams; project names live on the Site) and its roles on this instance's own projects. On a control plane, roles given on a Site itself aren't known (projectRolesOnSites).
// @Tags user-management
// @Produce json
// @Security BearerAuth
// @Param id path string true "User ID"
// @Success 200 {object} useraccess.UserDetail
// @Failure 400 {object} map[string]string "Invalid user ID"
// @Failure 401 {object} map[string]string "Unauthorized"
// @Failure 403 {object} map[string]string "Forbidden"
// @Failure 404 {object} map[string]string "User not found"
// @Failure 500 {object} map[string]string "Internal error"
// @Router /users/{id}/detail [get]
func (c *Controller) GetUserDetail(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	userID, err := uuid.Parse(ctx.Param("id"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID"})
		return
	}
	detail, err := c.service.GetUserDetail(userID, user)
	switch {
	case err == nil:
		ctx.JSON(http.StatusOK, detail)
	case errors.Is(err, ErrForbidden):
		ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	case errors.Is(err, ErrUserNotFound):
		ctx.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	default:
		logger.GetLogger().Error("failed to read user detail", "user", userID, "error", err)
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read the user"})
	}
}
