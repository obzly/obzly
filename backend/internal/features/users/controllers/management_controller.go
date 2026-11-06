package users_controllers

import (
	"errors"
	"net/http"

	user_dto "obzly/internal/features/users/dto"
	user_enums "obzly/internal/features/users/enums"
	user_middleware "obzly/internal/features/users/middleware"
	users_services "obzly/internal/features/users/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type ManagementController struct {
	managementService *users_services.UserManagementService
}

func (c *ManagementController) RegisterRoutes(router *gin.RouterGroup) {
	router.GET("/users", user_middleware.RequireRole(user_enums.UserRoleAdmin), c.GetUsers)
	router.GET("/users/:id", c.GetUserProfile)
	router.GET("/users/count-by-plan/:planId", user_middleware.RequireRole(user_enums.UserRoleAdmin), c.CountByPlan)
	router.POST("/users/:id/deactivate", user_middleware.RequireRole(user_enums.UserRoleAdmin), c.DeactivateUser)
	router.POST("/users/:id/activate", user_middleware.RequireRole(user_enums.UserRoleAdmin), c.ActivateUser)
	router.DELETE("/users/:id", user_middleware.RequireRole(user_enums.UserRoleAdmin), c.DeleteUser)
	router.PUT("/users/:id/role", user_middleware.RequireRole(user_enums.UserRoleAdmin), c.ChangeUserRole)
	router.POST("/users/:id/reset-mfa", user_middleware.RequireRole(user_enums.UserRoleAdmin), c.ResetUserMFA)
	router.POST("/users/:id/set-password", user_middleware.RequireRole(user_enums.UserRoleAdmin), c.SetUserPassword)
}

// ListUsers
// @Summary List users
// @Description Get list of users (admin only)
// @Tags user-management
// @Produce json
// @Security BearerAuth
// @Param limit query int false "Number of items per page" default(20)
// @Param offset query int false "Page offset" default(0)
// @Param beforeDate query string false "Filter users created before this date (RFC3339 format)" format(date-time)
// @Param query query string false "Search by email or name (case-insensitive)"
// @Success 200 {object} users_dto.ListUsersResponseDTO
// @Failure 401 {object} map[string]string "Unauthorized"
// @Failure 403 {object} map[string]string "Forbidden"
// @Router /users [get]
func (c *ManagementController) GetUsers(ctx *gin.Context) {
	user, ok := user_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	request := &user_dto.ListUsersRequestDTO{}
	if err := ctx.ShouldBindQuery(request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid query parameters"})
		return
	}

	if request.Limit <= 0 || request.Limit > 100 {
		request.Limit = 20
	}
	if request.Offset < 0 {
		request.Offset = 0
	}

	users, total, err := c.managementService.GetUsers(
		user,
		request.Limit,
		request.Offset,
		request.BeforeDate,
		request.Query,
	)
	if err != nil {
		ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	userProfiles := make([]user_dto.UserProfileResponseDTO, len(users))
	for i, u := range users {
		userProfiles[i] = user_dto.UserProfileResponseDTO{
			ID:        u.ID,
			Email:     u.Email,
			Name:      u.Name,
			Role:      u.Role,
			IsActive:  u.IsActiveUser(),
			CreatedAt: u.CreatedAt,

			LastLoginAt:  u.LastLoginAt,
			LastActiveAt: u.LastActiveAt,
			MFAEnabled:   u.MFAEnabled,
		}
	}

	response := user_dto.ListUsersResponseDTO{
		Users: userProfiles,
		Total: total,
	}

	ctx.JSON(http.StatusOK, response)
}

// GetUserProfile
// @Summary Get user profile
// @Description Get user profile information (users can view own profile, admins can view any)
// @Tags user-management
// @Produce json
// @Security BearerAuth
// @Param id path string true "User ID"
// @Success 200 {object} users_dto.UserProfileResponseDTO
// @Failure 400 {object} map[string]string "Bad request"
// @Failure 401 {object} map[string]string "Unauthorized"
// @Failure 403 {object} map[string]string "Forbidden"
// @Router /users/{id} [get]
func (c *ManagementController) GetUserProfile(ctx *gin.Context) {
	currentUser, ok := user_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	userIDStr := ctx.Param("id")
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID"})
		return
	}

	user, err := c.managementService.GetUserProfile(userID, currentUser)
	if err != nil {
		ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	profile := user_dto.UserProfileResponseDTO{
		ID:        user.ID,
		Email:     user.Email,
		Name:      user.Name,
		Role:      user.Role,
		IsActive:  user.IsActiveUser(),
		CreatedAt: user.CreatedAt,

		LastLoginAt:  user.LastLoginAt,
		LastActiveAt: user.LastActiveAt,
		MFAEnabled:   user.MFAEnabled,
	}

	ctx.JSON(http.StatusOK, profile)
}

// DeactivateUser
// @Summary Deactivate user
// @Description Deactivate a user account (admin only)
// @Tags user-management
// @Security BearerAuth
// @Param id path string true "User ID"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string "Bad request"
// @Failure 401 {object} map[string]string "Unauthorized"
// @Failure 403 {object} map[string]string "Forbidden"
// @Router /users/{id}/deactivate [post]
func (c *ManagementController) DeactivateUser(ctx *gin.Context) {
	currentUser, ok := user_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	userIDStr := ctx.Param("id")
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID"})
		return
	}

	if err := c.managementService.DeactivateUser(userID, currentUser); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "User deactivated successfully"})
}

// DeleteUser
// @Summary Delete user
// @Description Permanently delete a user account (admin only). Not your own account or the root admin; an admin account only by the root admin; not while the user owns a project.
// @Tags user-management
// @Produce json
// @Security BearerAuth
// @Param id path string true "User ID"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string "Bad request"
// @Failure 401 {object} map[string]string "Unauthorized"
// @Failure 403 {object} map[string]string "Forbidden"
// @Failure 404 {object} map[string]string "User not found"
// @Failure 409 {object} map[string]string "User owns projects"
// @Router /users/{id} [delete]
func (c *ManagementController) DeleteUser(ctx *gin.Context) {
	currentUser, ok := user_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	userID, err := uuid.Parse(ctx.Param("id"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID"})
		return
	}

	err = c.managementService.DeleteUser(userID, currentUser)
	switch {
	case err == nil:
		ctx.JSON(http.StatusOK, gin.H{"message": "User deleted successfully"})
	case errors.Is(err, users_services.ErrDeleteUsersForbidden):
		ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	case errors.Is(err, users_services.ErrUserNotFound):
		ctx.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, users_services.ErrUserOwnsProjects):
		ctx.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	default:
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	}
}

// ResetUserMFA
// @Summary Reset a user's MFA (admin only)
// @Description Clears a user's MFA so they can sign in and set it up again - the safety valve for a lost authenticator device
// @Tags user-management
// @Produce json
// @Security BearerAuth
// @Param id path string true "User ID"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /users/{id}/reset-mfa [post]
func (c *ManagementController) ResetUserMFA(ctx *gin.Context) {
	currentUser, ok := user_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	userIDStr := ctx.Param("id")
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID"})
		return
	}

	if err := c.managementService.ResetUserMFA(userID, currentUser); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "MFA reset successfully"})
}

// SetUserPassword
// @Summary Set a user's password (admin only)
// @Description Directly sets (or resets) another user's password - the escape hatch for the invite
// @Description flow when no SMTP is configured, since InviteUser alone never reaches the invitee.
// @Description If the target is still in INVITED status, this also activates their account.
// @Tags user-management
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "User ID"
// @Param request body users_dto.SetUserPasswordRequestDTO true "New password"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string "Bad request"
// @Failure 401 {object} map[string]string "Unauthorized"
// @Failure 403 {object} map[string]string "Forbidden"
// @Router /users/{id}/set-password [post]
func (c *ManagementController) SetUserPassword(ctx *gin.Context) {
	currentUser, ok := user_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	userIDStr := ctx.Param("id")
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID"})
		return
	}

	var request user_dto.SetUserPasswordRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	if err := c.managementService.SetUserPassword(userID, request.Password, currentUser); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "Password set successfully"})
}

// ActivateUser
// @Summary Activate user
// @Description Activate a user account (admin only)
// @Tags user-management
// @Security BearerAuth
// @Param id path string true "User ID"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string "Bad request"
// @Failure 401 {object} map[string]string "Unauthorized"
// @Failure 403 {object} map[string]string "Forbidden"
// @Router /users/{id}/activate [post]
func (c *ManagementController) ActivateUser(ctx *gin.Context) {
	currentUser, ok := user_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	userIDStr := ctx.Param("id")
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID"})
		return
	}

	if err := c.managementService.ActivateUser(userID, currentUser); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "User activated successfully"})
}

// ChangeUserRole
// @Summary Change user role
// @Description Change a user's role (admin only)
// @Tags user-management
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "User ID"
// @Param request body users_dto.ChangeUserRoleRequestDTO true "Role change data"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string "Bad request"
// @Failure 401 {object} map[string]string "Unauthorized"
// @Failure 403 {object} map[string]string "Forbidden"
// @Router /users/{id}/role [put]
func (c *ManagementController) ChangeUserRole(ctx *gin.Context) {
	currentUser, ok := user_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	userIDStr := ctx.Param("id")
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID"})
		return
	}

	var request user_dto.ChangeUserRoleRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	if err := c.managementService.ChangeUserRole(userID, request.Role, currentUser); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "User role changed successfully"})
}

// CountByPlan
// @Summary Get user count by plan
// @Description Get count of users for a specific plan (admin only)
// @Tags user-management
// @Produce json
// @Security BearerAuth
// @Param planId path string true "Plan ID"
// @Success 200 {object} users_dto.CountByPlanResponseDTO
// @Failure 400 {object} map[string]string "Bad Request"
// @Failure 401 {object} map[string]string "Unauthorized"
// @Failure 403 {object} map[string]string "Forbidden"
// @Router /users/count-by-plan/{planId} [get]
func (c *ManagementController) CountByPlan(ctx *gin.Context) {
	user, ok := user_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	planIDStr := ctx.Param("planId")
	planID, err := uuid.Parse(planIDStr)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid plan ID"})
		return
	}

	count, err := c.managementService.CountByPlan(planID, user)
	if err != nil {
		ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	response := user_dto.CountByPlanResponseDTO{
		Count: count,
	}

	ctx.JSON(http.StatusOK, response)
}
