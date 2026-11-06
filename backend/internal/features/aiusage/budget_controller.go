package aiusage

import (
	"errors"
	"net/http"

	"obzly/internal/authz"
	users_middleware "obzly/internal/features/users/middleware"
	users_models "obzly/internal/features/users/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// BudgetController serves AI budgets and model prices: any member reads,
// project owners and admins change them.
type BudgetController struct {
	service *AIUsageService
}

func (c *BudgetController) RegisterRoutes(router *gin.RouterGroup) {
	router.GET("/projects/:id/ai-budgets", authz.Require(authz.ProjectRead), c.ListBudgets)
	router.GET("/projects/:id/ai-budgets/status", authz.Require(authz.ProjectRead), c.GetBudgetStatus)
	router.POST("/projects/:id/ai-budgets", authz.Require(authz.ProjectManage), c.CreateBudget)
	router.PUT("/projects/:id/ai-budgets/:budgetId", authz.Require(authz.ProjectManage), c.UpdateBudget)
	router.DELETE("/projects/:id/ai-budgets/:budgetId", authz.Require(authz.ProjectManage), c.DeleteBudget)
	router.GET("/projects/:id/ai-model-prices", authz.Require(authz.ProjectRead), c.ListModelPrices)
	router.POST("/projects/:id/ai-model-prices", authz.Require(authz.ProjectManage), c.CreateModelPrice)
	router.POST("/projects/:id/ai-model-prices/fill-common", authz.Require(authz.ProjectManage), c.FillCommonPrices)
	router.PUT("/projects/:id/ai-model-prices/:priceId", authz.Require(authz.ProjectManage), c.UpdateModelPrice)
	router.DELETE("/projects/:id/ai-model-prices/:priceId", authz.Require(authz.ProjectManage), c.DeleteModelPrice)
}

// ListBudgets
// @Summary List AI budgets
// @Description A project's AI spending budgets (any member)
// @Tags ai-usage
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Success 200 {array} aiusage.Budget
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /projects/{id}/ai-budgets [get]
func (c *BudgetController) ListBudgets(ctx *gin.Context) {
	projectID, _, ok := projectAndUser(ctx)
	if !ok {
		return
	}
	budgets, err := c.service.ListBudgets(projectID)
	respondBudget(ctx, budgets, err)
}

// GetBudgetStatus
// @Summary AI budget status
// @Description Each budget's spend this period, forecast at the current rate and top five users or models (any member)
// @Tags ai-usage
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Success 200 {array} aiusage.BudgetStatusDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /projects/{id}/ai-budgets/status [get]
func (c *BudgetController) GetBudgetStatus(ctx *gin.Context) {
	projectID, _, ok := projectAndUser(ctx)
	if !ok {
		return
	}
	statuses, err := c.service.BudgetStatuses(ctx.Request.Context(), projectID)
	respondBudget(ctx, statuses, err)
}

// CreateBudget
// @Summary Create an AI budget
// @Description Project owners and admins. Keeps a read-only alert rule (source ai_cost).
// @Tags ai-usage
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Param request body aiusage.BudgetRequest true "Budget"
// @Success 200 {object} aiusage.Budget
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /projects/{id}/ai-budgets [post]
func (c *BudgetController) CreateBudget(ctx *gin.Context) {
	projectID, user, ok := projectAndUser(ctx)
	if !ok {
		return
	}
	var request BudgetRequest
	if !bindJSON(ctx, &request) {
		return
	}
	budget, err := c.service.CreateBudget(projectID, request, user.ID)
	respondBudget(ctx, budget, err)
}

// UpdateBudget
// @Summary Update an AI budget
// @Description Project owners and admins
// @Tags ai-usage
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Param budgetId path string true "Budget ID"
// @Param request body aiusage.BudgetRequest true "Budget"
// @Success 200 {object} aiusage.Budget
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /projects/{id}/ai-budgets/{budgetId} [put]
func (c *BudgetController) UpdateBudget(ctx *gin.Context) {
	projectID, user, ok := projectAndUser(ctx)
	if !ok {
		return
	}
	budgetID, ok := pathUUID(ctx, "budgetId")
	if !ok {
		return
	}
	var request BudgetRequest
	if !bindJSON(ctx, &request) {
		return
	}
	budget, err := c.service.UpdateBudget(projectID, budgetID, request, user.ID)
	respondBudget(ctx, budget, err)
}

// DeleteBudget
// @Summary Delete an AI budget
// @Description Project owners and admins; its alert rule goes too
// @Tags ai-usage
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Param budgetId path string true "Budget ID"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /projects/{id}/ai-budgets/{budgetId} [delete]
func (c *BudgetController) DeleteBudget(ctx *gin.Context) {
	projectID, user, ok := projectAndUser(ctx)
	if !ok {
		return
	}
	budgetID, ok := pathUUID(ctx, "budgetId")
	if !ok {
		return
	}
	respondBudget(ctx, gin.H{"message": "Budget deleted"}, c.service.DeleteBudget(projectID, budgetID, user.ID))
}

// ListModelPrices
// @Summary List AI model prices
// @Description USD per million tokens for LLM apps' cost estimates, with the calls each price applied to in the last 24 hours and the models seen without a price (any member)
// @Tags ai-usage
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Success 200 {object} aiusage.ModelPricesResponseDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /projects/{id}/ai-model-prices [get]
func (c *BudgetController) ListModelPrices(ctx *gin.Context) {
	projectID, _, ok := projectAndUser(ctx)
	if !ok {
		return
	}
	prices, err := c.service.ListModelPrices(ctx.Request.Context(), projectID)
	respondBudget(ctx, prices, err)
}

// CreateModelPrice
// @Summary Add an AI model price
// @Description Project owners and admins. The model is an exact name or a prefix ending in *.
// @Tags ai-usage
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Param request body aiusage.ModelPriceRequest true "Price"
// @Success 200 {object} aiusage.ModelPrice
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /projects/{id}/ai-model-prices [post]
func (c *BudgetController) CreateModelPrice(ctx *gin.Context) {
	projectID, user, ok := projectAndUser(ctx)
	if !ok {
		return
	}
	var request ModelPriceRequest
	if !bindJSON(ctx, &request) {
		return
	}
	price, err := c.service.CreateModelPrice(projectID, request, user.ID)
	respondBudget(ctx, price, err)
}

// FillCommonPrices
// @Summary Add common model prices
// @Description Project owners and admins. Adds a short, dated starting list of list prices for models without a price; review each one.
// @Tags ai-usage
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Success 200 {object} aiusage.FillCommonPricesResponseDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /projects/{id}/ai-model-prices/fill-common [post]
func (c *BudgetController) FillCommonPrices(ctx *gin.Context) {
	projectID, user, ok := projectAndUser(ctx)
	if !ok {
		return
	}
	response, err := c.service.FillCommonPrices(projectID, user.ID)
	respondBudget(ctx, response, err)
}

// UpdateModelPrice
// @Summary Update an AI model price
// @Description Project owners and admins
// @Tags ai-usage
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Param priceId path string true "Price ID"
// @Param request body aiusage.ModelPriceRequest true "Price"
// @Success 200 {object} aiusage.ModelPrice
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /projects/{id}/ai-model-prices/{priceId} [put]
func (c *BudgetController) UpdateModelPrice(ctx *gin.Context) {
	projectID, user, ok := projectAndUser(ctx)
	if !ok {
		return
	}
	priceID, ok := pathUUID(ctx, "priceId")
	if !ok {
		return
	}
	var request ModelPriceRequest
	if !bindJSON(ctx, &request) {
		return
	}
	price, err := c.service.UpdateModelPrice(projectID, priceID, request, user.ID)
	respondBudget(ctx, price, err)
}

// DeleteModelPrice
// @Summary Delete an AI model price
// @Description Project owners and admins
// @Tags ai-usage
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Param priceId path string true "Price ID"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /projects/{id}/ai-model-prices/{priceId} [delete]
func (c *BudgetController) DeleteModelPrice(ctx *gin.Context) {
	projectID, user, ok := projectAndUser(ctx)
	if !ok {
		return
	}
	priceID, ok := pathUUID(ctx, "priceId")
	if !ok {
		return
	}
	respondBudget(ctx, gin.H{"message": "Price deleted"}, c.service.DeleteModelPrice(projectID, priceID, user.ID))
}

func projectAndUser(ctx *gin.Context) (uuid.UUID, *users_models.User, bool) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return uuid.Nil, nil, false
	}
	projectID, ok := pathUUID(ctx, "id")
	return projectID, user, ok
}

func pathUUID(ctx *gin.Context, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(ctx.Param(name))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid " + name})
		return uuid.Nil, false
	}
	return id, true
}

func bindJSON(ctx *gin.Context, target any) bool {
	if err := ctx.ShouldBindJSON(target); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return false
	}
	return true
}

func respondBudget(ctx *gin.Context, body any, err error) {
	switch {
	case err == nil:
		ctx.JSON(http.StatusOK, body)
	case errors.Is(err, ErrInvalidRequest):
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, ErrNotFound):
		ctx.JSON(http.StatusNotFound, gin.H{"error": "Not found"})
	default:
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process the request"})
	}
}
