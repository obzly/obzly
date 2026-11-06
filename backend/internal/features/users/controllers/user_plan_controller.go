package users_controllers

import (
	"net/http"

	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	users_middleware "obzly/internal/features/users/middleware"
	users_services "obzly/internal/features/users/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type UserPlanController struct {
	userPlanService *users_services.UserPlanService
}

func (c *UserPlanController) RegisterRoutes(router *gin.RouterGroup) {
	router.GET("/plans", c.GetPlans)
	router.POST("/plans", users_middleware.RequireRole(users_enums.UserRoleAdmin), c.CreatePlan)
	router.PUT("/plans/:id", users_middleware.RequireRole(users_enums.UserRoleAdmin), c.UpdatePlan)
	router.DELETE("/plans/:id", users_middleware.RequireRole(users_enums.UserRoleAdmin), c.DeletePlan)
}

// GetPlans
// @Summary Get all user plans
// @Description Get list of all user plans
// @Tags user-plans
// @Produce json
// @Security BearerAuth
// @Success 200 {array} users_models.UserPlan
// @Failure 401 {object} map[string]string "Unauthorized"
// @Failure 500 {object} map[string]string "Internal server error"
// @Router /plans [get]
func (c *UserPlanController) GetPlans(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	plans, err := c.userPlanService.GetPlans(user)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get plans"})
		return
	}

	ctx.JSON(http.StatusOK, plans)
}

// CreatePlan
// @Summary Create user plan
// @Description Create a new user plan (admin only)
// @Tags user-plans
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body users_dto.CreatePlanRequestDTO true "Plan creation data"
// @Success 200 {object} users_models.UserPlan
// @Failure 400 {object} map[string]string "Bad request"
// @Failure 401 {object} map[string]string "Unauthorized"
// @Failure 403 {object} map[string]string "Forbidden"
// @Router /plans [post]
func (c *UserPlanController) CreatePlan(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	var request users_dto.CreatePlanRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	plan, err := c.userPlanService.CreatePlan(&request, user)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, plan)
}

// UpdatePlan
// @Summary Update user plan
// @Description Update an existing user plan (admin only)
// @Tags user-plans
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Plan ID"
// @Param request body users_dto.UpdatePlanRequestDTO true "Plan update data"
// @Success 200 {object} users_models.UserPlan
// @Failure 400 {object} map[string]string "Bad request"
// @Failure 401 {object} map[string]string "Unauthorized"
// @Failure 403 {object} map[string]string "Forbidden"
// @Failure 404 {object} map[string]string "Not found"
// @Router /plans/{id} [put]
func (c *UserPlanController) UpdatePlan(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	planIDStr := ctx.Param("id")
	planID, err := uuid.Parse(planIDStr)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid plan ID"})
		return
	}

	var request users_dto.UpdatePlanRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	plan, err := c.userPlanService.UpdatePlan(planID, &request, user)
	if err != nil {
		if err.Error() == "plan not found" {
			ctx.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, plan)
}

// DeletePlan
// @Summary Delete user plan
// @Description Delete a user plan (admin only, fails if users are using it)
// @Tags user-plans
// @Security BearerAuth
// @Param id path string true "Plan ID"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string "Bad request"
// @Failure 401 {object} map[string]string "Unauthorized"
// @Failure 403 {object} map[string]string "Forbidden"
// @Failure 404 {object} map[string]string "Not found"
// @Router /plans/{id} [delete]
func (c *UserPlanController) DeletePlan(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	planIDStr := ctx.Param("id")
	planID, err := uuid.Parse(planIDStr)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid plan ID"})
		return
	}

	if err := c.userPlanService.DeletePlan(planID, user); err != nil {
		if err.Error() == "plan not found" {
			ctx.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "Plan deleted successfully"})
}
