package cloudcost

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"obzly/internal/authz"
	"obzly/internal/features/api_keys"
	"obzly/internal/features/otlp"
	users_middleware "obzly/internal/features/users/middleware"
	users_models "obzly/internal/features/users/models"
	"obzly/internal/synthetics/report"
	"obzly/internal/synthetics/spec"
)

const maxRequestBytes = 1 << 20

type Controller struct {
	service *Service
}

// RegisterReceiver mounts the probe's cost receiver at the root, next to OTLP: it authenticates
// with the destination project's API key, like OTLP, and is never proxied.
func (c *Controller) RegisterReceiver(router gin.IRouter) {
	router.POST(report.CostPath, c.Receive)
	router.POST(report.AdvicePath, c.ReceiveAdvice)
}

func (c *Controller) RegisterRoutes(router *gin.RouterGroup) {
	router.GET("/cloud-cost/summary", c.GetSummary)
	router.GET("/cloud-cost/sources", c.ListSources)
	router.POST("/cloud-cost/explore", c.Explore)
	router.POST("/cloud-cost/facets", c.Facets)
	router.GET("/cloud-cost/resource", c.GetResource)
	router.GET("/cloud-cost/anomalies", c.ListAnomalies)
	router.PATCH("/cloud-cost/anomalies/:id", c.UpdateAnomaly)
	router.GET("/cloud-cost/recommendations", c.ListRecommendations)
	router.PATCH("/cloud-cost/recommendations", c.UpdateRecommendation)
	router.GET("/cloud-cost/resource-recommendations", c.GetResourceRecommendations)
	router.GET("/cloud-cost/commitments", c.ListCommitments)
	router.GET("/cloud-cost/planner", c.GetPlanner)
	router.GET("/projects/:id/cost-budgets", authz.Require(authz.ProjectRead), c.ListBudgets)
	router.POST("/projects/:id/cost-budgets", authz.Require(authz.ProjectWrite), c.CreateBudget)
	router.PUT("/projects/:id/cost-budgets/:budgetId", authz.Require(authz.ProjectWrite), c.UpdateBudget)
	router.DELETE("/projects/:id/cost-budgets/:budgetId", authz.Require(authz.ProjectWrite), c.DeleteBudget)
	router.GET("/projects/:id/cost-alerts", authz.Require(authz.ProjectRead), c.GetAlertSettings)
	router.PUT("/projects/:id/cost-alerts", authz.Require(authz.ProjectWrite), c.UpdateAlertSettings)
}

// Receive
// @Summary Receive a cost batch from a probe
// @Description obzly-probe sends daily cost rows in chunks (gzip JSON, X-API-Key of the destination project); a batch replaces its days once every chunk has arrived
// @Tags cloud-cost
// @Accept json
// @Produce json
// @Param X-API-Key header string true "The destination project's ingest key"
// @Success 200 {object} map[string]any
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Router /v1/cloud/cost [post]
func (c *Controller) Receive(ctx *gin.Context) {
	resolved, err := api_keys.GetApiKeyService().ResolveProjectFromApiKey(ctx.GetHeader("X-API-Key"))
	if err != nil || resolved == nil || !resolved.IsValid {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "invalid API key"})
		return
	}
	body, err := otlp.ReadBody(ctx.Request)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var batch spec.CostBatch
	if err := json.Unmarshal(body, &batch); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid cost batch JSON"})
		return
	}
	applied, err := c.service.Receive(ctx.Request.Context(), resolved.ProjectID, batch)
	switch {
	case errors.Is(err, ErrInvalidBatch):
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case err != nil:
		c.service.log.Error("Storing a cost batch failed", "error", err)
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "couldn't store the cost batch"})
	default:
		ctx.JSON(http.StatusOK, gin.H{"applied": applied})
	}
}

// GetSummary
// @Summary Cloud cost overview
// @Description Per currency: month to date, last month, forecast and the last 90 days; what each connection last delivered; open anomalies
// @Tags cloud-cost
// @Produce json
// @Security BearerAuth
// @Param projectId query string false "Only this project"
// @Success 200 {object} cloudcost.SummaryDTO
// @Failure 403 {object} map[string]string
// @Router /cloud-cost/summary [get]
func (c *Controller) GetSummary(ctx *gin.Context) {
	projects, ok := c.projects(ctx, ctx.Query("projectId"))
	if !ok {
		return
	}
	summary, err := c.service.Summary(ctx.Request.Context(), projects)
	respond(ctx, summary, err)
}

// ListSources
// @Summary What each connection's cost sources last delivered
// @Tags cloud-cost
// @Produce json
// @Security BearerAuth
// @Param projectId query string false "Only this project"
// @Success 200 {array} cloudcost.SourceStatus
// @Failure 403 {object} map[string]string
// @Router /cloud-cost/sources [get]
func (c *Controller) ListSources(ctx *gin.Context) {
	projects, ok := c.projects(ctx, ctx.Query("projectId"))
	if !ok {
		return
	}
	sources, err := c.service.Sources(ctx.Request.Context(), projects)
	respond(ctx, sources, err)
}

// Explore
// @Summary Cost by group and period
// @Description Sums effective or billed cost from..to by day, month, quarter or year, grouped by provider, account, region, service, resource, charge category or a tracked tag (tag:<key>); one block per currency, never added together
// @Tags cloud-cost
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body cloudcost.ExploreRequest true "Query"
// @Success 200 {object} cloudcost.ExploreResponse
// @Failure 400 {object} map[string]string
// @Router /cloud-cost/explore [post]
func (c *Controller) Explore(ctx *gin.Context) {
	var request ExploreRequest
	if !bind(ctx, &request) {
		return
	}
	projects, ok := c.projects(ctx, request.ProjectID)
	if !ok {
		return
	}
	response, err := c.service.Explore(ctx.Request.Context(), projects, request)
	respond(ctx, response, err)
}

// Facets
// @Summary The values each cost filter can take with the others applied
// @Tags cloud-cost
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body cloudcost.ExploreRequest true "Range and filters"
// @Success 200 {object} cloudcost.FacetsResponse
// @Failure 400 {object} map[string]string
// @Router /cloud-cost/facets [post]
func (c *Controller) Facets(ctx *gin.Context) {
	var request ExploreRequest
	if !bind(ctx, &request) {
		return
	}
	projects, ok := c.projects(ctx, request.ProjectID)
	if !ok {
		return
	}
	response, err := c.service.Facets(ctx.Request.Context(), projects, request)
	respond(ctx, response, err)
}

// GetResource
// @Summary One resource's cost
// @Description From the FOCUS exports, by day and charge category; key is the resource's cloud ID (ARN or Azure resource ID)
// @Tags cloud-cost
// @Produce json
// @Security BearerAuth
// @Param key query string true "Cloud resource ID"
// @Param days query int false "Days back (default 30)"
// @Param projectId query string false "Only this project"
// @Success 200 {object} cloudcost.ResourceCostDTO
// @Failure 400 {object} map[string]string
// @Router /cloud-cost/resource [get]
func (c *Controller) GetResource(ctx *gin.Context) {
	key := ctx.Query("key")
	if key == "" || len(key) > 1024 {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "key is the resource's cloud ID"})
		return
	}
	projects, ok := c.projects(ctx, ctx.Query("projectId"))
	if !ok {
		return
	}
	days, _ := strconv.Atoi(ctx.DefaultQuery("days", "30"))
	resource, err := c.service.Resource(ctx.Request.Context(), projects, key, days)
	respond(ctx, resource, err)
}

// ListAnomalies
// @Summary Cost anomalies
// @Tags cloud-cost
// @Produce json
// @Security BearerAuth
// @Param from query string false "First day (default 30 days ago)"
// @Param to query string false "Last day (default today)"
// @Param status query string false "open or acknowledged"
// @Param projectId query string false "Only this project"
// @Success 200 {array} cloudcost.AnomalyDTO
// @Failure 400 {object} map[string]string
// @Router /cloud-cost/anomalies [get]
func (c *Controller) ListAnomalies(ctx *gin.Context) {
	projects, ok := c.projects(ctx, ctx.Query("projectId"))
	if !ok {
		return
	}
	today := utcDay(c.service.now())
	from, to := today.AddDate(0, 0, -30), today
	var err error
	if value := ctx.Query("from"); value != "" {
		if from, err = parseDay(value); err != nil {
			respond(ctx, nil, err)
			return
		}
	}
	if value := ctx.Query("to"); value != "" {
		if to, err = parseDay(value); err != nil {
			respond(ctx, nil, err)
			return
		}
	}
	anomalies, err := c.service.ListAnomalies(ctx.Request.Context(), projects, from, to, ctx.Query("status"))
	respond(ctx, anomalies, err)
}

// AnomalyUpdate acknowledges or reopens an anomaly.
type AnomalyUpdate struct {
	Status string `json:"status"`
	Note   string `json:"note"`
}

// UpdateAnomaly
// @Summary Acknowledge or reopen a cost anomaly
// @Description Project members
// @Tags cloud-cost
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Anomaly ID"
// @Param request body cloudcost.AnomalyUpdate true "Status and note"
// @Success 200 {object} cloudcost.Anomaly
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /cloud-cost/anomalies/{id} [patch]
func (c *Controller) UpdateAnomaly(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	id, ok := pathUUID(ctx, "id")
	if !ok {
		return
	}
	var request AnomalyUpdate
	if !bind(ctx, &request) {
		return
	}
	projectID, err := c.service.anomalyProject(ctx.Request.Context(), id)
	if err != nil {
		respond(ctx, nil, err)
		return
	}
	if decision, err := authz.Authorize(ctx, user, authz.ProjectWrite, authz.Project(projectID)); err != nil ||
		!decision.Allowed {
		respond(ctx, nil, ErrForbidden)
		return
	}
	anomaly, err := c.service.UpdateAnomaly(ctx.Request.Context(), id, request.Status, request.Note)
	respond(ctx, anomaly, err)
}

// ListBudgets
// @Summary Cost budgets
// @Description A project's monthly cost budgets with this month's spend and projection (any member)
// @Tags cloud-cost
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Success 200 {array} cloudcost.BudgetDTO
// @Failure 403 {object} map[string]string
// @Router /projects/{id}/cost-budgets [get]
func (c *Controller) ListBudgets(ctx *gin.Context) {
	projectID, ok := pathUUID(ctx, "id")
	if !ok {
		return
	}
	budgets, err := c.service.ListBudgets(ctx.Request.Context(), projectID)
	respond(ctx, budgets, err)
}

// CreateBudget
// @Summary Create a cost budget
// @Description Project members. Keeps a read-only alert rule (source cloud_cost)
// @Tags cloud-cost
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Param request body cloudcost.BudgetRequest true "Budget"
// @Success 200 {object} cloudcost.BudgetDTO
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /projects/{id}/cost-budgets [post]
func (c *Controller) CreateBudget(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	projectID, ok := pathUUID(ctx, "id")
	if !ok {
		return
	}
	var request BudgetRequest
	if !bind(ctx, &request) {
		return
	}
	budget, err := c.service.CreateBudget(ctx.Request.Context(), projectID, user.ID, request)
	respond(ctx, budget, err)
}

// UpdateBudget
// @Summary Update a cost budget
// @Description Project members
// @Tags cloud-cost
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Param budgetId path string true "Budget ID"
// @Param request body cloudcost.BudgetRequest true "Budget"
// @Success 200 {object} cloudcost.BudgetDTO
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /projects/{id}/cost-budgets/{budgetId} [put]
func (c *Controller) UpdateBudget(ctx *gin.Context) {
	projectID, ok := pathUUID(ctx, "id")
	if !ok {
		return
	}
	budgetID, ok := pathUUID(ctx, "budgetId")
	if !ok {
		return
	}
	var request BudgetRequest
	if !bind(ctx, &request) {
		return
	}
	budget, err := c.service.UpdateBudget(ctx.Request.Context(), projectID, budgetID, request)
	respond(ctx, budget, err)
}

// DeleteBudget
// @Summary Delete a cost budget
// @Description Project members; its alert rule goes too
// @Tags cloud-cost
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Param budgetId path string true "Budget ID"
// @Success 200 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /projects/{id}/cost-budgets/{budgetId} [delete]
func (c *Controller) DeleteBudget(ctx *gin.Context) {
	projectID, ok := pathUUID(ctx, "id")
	if !ok {
		return
	}
	budgetID, ok := pathUUID(ctx, "budgetId")
	if !ok {
		return
	}
	err := c.service.DeleteBudget(ctx.Request.Context(), projectID, budgetID)
	respond(ctx, gin.H{"message": "Budget deleted"}, err)
}

// GetAlertSettings
// @Summary Where a project's new cost anomalies are sent
// @Tags cloud-cost
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Success 200 {object} cloudcost.AlertSettingsDTO
// @Router /projects/{id}/cost-alerts [get]
func (c *Controller) GetAlertSettings(ctx *gin.Context) {
	projectID, ok := pathUUID(ctx, "id")
	if !ok {
		return
	}
	settings, err := c.service.GetAlertSettings(ctx.Request.Context(), projectID)
	respond(ctx, settings, err)
}

// UpdateAlertSettings
// @Summary Send a project's new cost anomalies to channels
// @Description Project members. Keeps a read-only alert rule (source cloud_cost); no channels removes it
// @Tags cloud-cost
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Param request body cloudcost.AlertSettingsDTO true "Channels"
// @Success 200 {object} cloudcost.AlertSettingsDTO
// @Router /projects/{id}/cost-alerts [put]
func (c *Controller) UpdateAlertSettings(ctx *gin.Context) {
	projectID, ok := pathUUID(ctx, "id")
	if !ok {
		return
	}
	var request AlertSettingsDTO
	if !bind(ctx, &request) {
		return
	}
	settings, err := c.service.UpdateAlertSettings(ctx.Request.Context(), projectID, request)
	respond(ctx, settings, err)
}

func (c *Controller) projects(ctx *gin.Context, projectID string) ([]uuid.UUID, bool) {
	user, ok := currentUser(ctx)
	if !ok {
		return nil, false
	}
	projects, err := c.service.projectsFor(user, projectID)
	if err != nil {
		respond(ctx, nil, err)
		return nil, false
	}
	return projects, true
}

func currentUser(ctx *gin.Context) (*users_models.User, bool) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
	}
	return user, ok
}

func pathUUID(ctx *gin.Context, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(ctx.Param(name))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid " + name})
		return uuid.Nil, false
	}
	return id, true
}

func bind(ctx *gin.Context, target any) bool {
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, maxRequestBytes)
	if err := ctx.ShouldBindJSON(target); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return false
	}
	return true
}

// respond maps service errors: permission 403, missing 404, anything else (validation) 400.
func respond(ctx *gin.Context, body any, err error) {
	switch {
	case err == nil:
		ctx.JSON(http.StatusOK, body)
	case errors.Is(err, ErrForbidden):
		ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	case errors.Is(err, ErrNotFound):
		ctx.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	default:
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	}
}
