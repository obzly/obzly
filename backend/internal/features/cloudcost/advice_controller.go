package cloudcost

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"obzly/internal/authz"
	"obzly/internal/features/api_keys"
	"obzly/internal/features/otlp"
	"obzly/internal/synthetics/spec"
)

// ReceiveAdvice
// @Summary Receive cost recommendations from a probe
// @Description obzly-probe sends a connection's weekly recommendations, commitments and purchase options in chunks (gzip JSON, X-API-Key of the destination project); each section that came back ok replaces the connection's data once every chunk has arrived
// @Tags cloud-cost
// @Accept json
// @Produce json
// @Param X-API-Key header string true "The destination project's ingest key"
// @Success 200 {object} map[string]any
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Router /v1/cloud/advice [post]
func (c *Controller) ReceiveAdvice(ctx *gin.Context) {
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
	var batch spec.AdviceBatch
	if err := json.Unmarshal(body, &batch); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid advice batch JSON"})
		return
	}
	applied, err := c.service.ReceiveAdvice(ctx.Request.Context(), resolved.ProjectID, batch)
	switch {
	case errors.Is(err, ErrInvalidAdvice):
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case err != nil:
		c.service.log.Error("Storing an advice batch failed", "error", err)
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "couldn't store the advice batch"})
	default:
		ctx.JSON(http.StatusOK, gin.H{"applied": applied})
	}
}

// ListRecommendations
// @Summary Cost recommendations
// @Description Rightsizing, idle, modernization and storage recommendations with their status, expected and realized savings, and how each connection's last refresh went
// @Tags cloud-cost
// @Produce json
// @Security BearerAuth
// @Param projectId query string false "Only this project"
// @Success 200 {object} cloudcost.RecommendationsDTO
// @Failure 403 {object} map[string]string
// @Router /cloud-cost/recommendations [get]
func (c *Controller) ListRecommendations(ctx *gin.Context) {
	projects, ok := c.projects(ctx, ctx.Query("projectId"))
	if !ok {
		return
	}
	result, err := c.service.Recommendations(ctx.Request.Context(), projects)
	respond(ctx, result, err)
}

// UpdateRecommendation
// @Summary Plan, finish, dismiss or reopen a cost recommendation
// @Description Project members. Dismissing needs a reason; plans and dismissals go into the audit log
// @Tags cloud-cost
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body cloudcost.RecommendationUpdate true "The recommendation's project, key and new status"
// @Success 200 {object} cloudcost.RecommendationDTO
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /cloud-cost/recommendations [patch]
func (c *Controller) UpdateRecommendation(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	var request RecommendationUpdate
	if !bind(ctx, &request) {
		return
	}
	if decision, err := authz.Authorize(ctx, user, authz.ProjectWrite, authz.Project(request.ProjectID)); err != nil ||
		!decision.Allowed {
		respond(ctx, nil, ErrForbidden)
		return
	}
	item, err := c.service.UpdateRecommendation(ctx.Request.Context(), user, request)
	respond(ctx, item, err)
}

// GetResourceRecommendations
// @Summary One resource's open cost recommendations
// @Description For the asset drawer: open and planned recommendations for the resource named by its cloud ID
// @Tags cloud-cost
// @Produce json
// @Security BearerAuth
// @Param key query string true "The resource's ARN or Azure resource ID"
// @Param projectId query string false "Only this project"
// @Success 200 {array} cloudcost.RecommendationDTO
// @Failure 403 {object} map[string]string
// @Router /cloud-cost/resource-recommendations [get]
func (c *Controller) GetResourceRecommendations(ctx *gin.Context) {
	key := ctx.Query("key")
	if key == "" || len(key) > 2048 {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "key is required"})
		return
	}
	projects, ok := c.projects(ctx, ctx.Query("projectId"))
	if !ok {
		return
	}
	items, err := c.service.ResourceRecommendations(ctx.Request.Context(), projects, key)
	respond(ctx, items, err)
}

// ListCommitments
// @Summary Existing commitments
// @Description Savings Plans and reservations with their use over 30 days, flagged when unused or ending within 60 days, and coverage per type
// @Tags cloud-cost
// @Produce json
// @Security BearerAuth
// @Param projectId query string false "Only this project"
// @Success 200 {object} cloudcost.CommitmentsDTO
// @Failure 403 {object} map[string]string
// @Router /cloud-cost/commitments [get]
func (c *Controller) ListCommitments(ctx *gin.Context) {
	projects, ok := c.projects(ctx, ctx.Query("projectId"))
	if !ok {
		return
	}
	result, err := c.service.Commitments(ctx.Request.Context(), projects)
	respond(ctx, result, err)
}

// GetPlanner
// @Summary Commitment planner
// @Description The providers' purchase recommendations per connection and type: 1 or 3 years by payment, for one look-back; rightsizingFirst (default true) scales each plan down by the open rightsizing savings on the usage it covers
// @Tags cloud-cost
// @Produce json
// @Security BearerAuth
// @Param projectId query string false "Only this project"
// @Param lookbackDays query int false "7, 30 (default) or 60"
// @Param rightsizingFirst query bool false "Default true"
// @Success 200 {object} cloudcost.PlannerDTO
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /cloud-cost/planner [get]
func (c *Controller) GetPlanner(ctx *gin.Context) {
	projects, ok := c.projects(ctx, ctx.Query("projectId"))
	if !ok {
		return
	}
	lookback, err := strconv.Atoi(ctx.DefaultQuery("lookbackDays", "30"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "lookbackDays must be 7, 30 or 60"})
		return
	}
	first := ctx.DefaultQuery("rightsizingFirst", "true") != "false"
	result, err := c.service.Planner(ctx.Request.Context(), projects, lookback, first)
	respond(ctx, result, err)
}
