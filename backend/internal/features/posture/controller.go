package posture

import (
	"encoding/json"
	"errors"
	"net/http"

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

// RegisterReceiver mounts the probe's posture receiver at the root, next to OTLP: it
// authenticates with the destination project's API key and is never proxied.
func (c *Controller) RegisterReceiver(router gin.IRouter) {
	router.POST(report.PosturePath, c.Receive)
}

func (c *Controller) RegisterRoutes(router *gin.RouterGroup) {
	router.GET("/posture/summary", c.GetSummary)
	router.POST("/posture/findings", c.ListFindings)
	router.GET("/posture/findings/:id", c.GetFinding)
	router.PATCH("/posture/findings/:id", c.UpdateFinding)
	router.POST("/posture/facets", c.GetFacets)
	router.POST("/posture/trends", c.GetTrends)
	router.GET("/posture/coverage", c.GetCoverage)
	router.GET("/posture/resource", c.GetResource)
	router.GET("/posture/actions", c.ListActions)
	router.PATCH("/posture/actions", c.UpdateAction)
	router.GET("/projects/:id/posture-alerts", authz.Require(authz.ProjectRead), c.GetAlertSettings)
	router.PUT("/projects/:id/posture-alerts", authz.Require(authz.ProjectWrite), c.UpdateAlertSettings)
}

// Receive
// @Summary Receive a posture batch from a probe
// @Description obzly-probe sends scope runs and findings in chunks (gzip JSON, X-API-Key of the destination project); a batch applies once every chunk has arrived
// @Tags posture
// @Accept json
// @Produce json
// @Param X-API-Key header string true "The destination project's ingest key"
// @Success 200 {object} map[string]any
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Router /v1/cloud/posture [post]
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
	var batch spec.PostureBatch
	if err := json.Unmarshal(body, &batch); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid posture batch JSON"})
		return
	}
	applied, err := c.service.Receive(ctx.Request.Context(), resolved.ProjectID, batch)
	switch {
	case errors.Is(err, ErrInvalidBatch):
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case err != nil:
		c.service.log.Error("Storing a posture batch failed", "error", err)
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "couldn't store the posture batch"})
	default:
		ctx.JSON(http.StatusOK, gin.H{"applied": applied})
	}
}

// GetSummary
// @Summary Cloud posture overview
// @Description Each standard's score and source, open findings by severity, coverage counts
// @Tags posture
// @Produce json
// @Security BearerAuth
// @Param projectId query string false "Only this project"
// @Success 200 {object} posture.SummaryDTO
// @Failure 403 {object} map[string]string
// @Router /posture/summary [get]
func (c *Controller) GetSummary(ctx *gin.Context) {
	projects, ok := c.projects(ctx, ctx.Query("projectId"))
	if !ok {
		return
	}
	summary, err := c.service.Summary(ctx.Request.Context(), projects)
	respond(ctx, summary, err)
}

// ListFindings
// @Summary Posture findings
// @Description Findings matching the filters (open and unsuppressed by default), a page at a time
// @Tags posture
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param projectId query string false "Only this project"
// @Param request body posture.FindingsRequest true "Filters, sort and page"
// @Success 200 {object} posture.FindingsResponse
// @Failure 400 {object} map[string]string
// @Router /posture/findings [post]
func (c *Controller) ListFindings(ctx *gin.Context) {
	var request FindingsRequest
	if !bind(ctx, &request) {
		return
	}
	projects, ok := c.projects(ctx, ctx.Query("projectId"))
	if !ok {
		return
	}
	response, err := c.service.Findings(ctx.Request.Context(), projects, request)
	respond(ctx, response, err)
}

// GetFinding
// @Summary One posture finding
// @Description With its history and, for a baseline check, remediation steps
// @Tags posture
// @Produce json
// @Security BearerAuth
// @Param id path string true "Finding ID"
// @Success 200 {object} posture.FindingDetail
// @Failure 404 {object} map[string]string
// @Router /posture/findings/{id} [get]
func (c *Controller) GetFinding(ctx *gin.Context) {
	id, ok := pathUUID(ctx, "id")
	if !ok {
		return
	}
	projects, ok := c.projects(ctx, "")
	if !ok {
		return
	}
	detail, err := c.service.Finding(ctx.Request.Context(), projects, id)
	respond(ctx, detail, err)
}

// UpdateFinding
// @Summary Suppress or unsuppress a posture finding
// @Description Suppressing needs a reason and can end at a time; both are kept in the finding's history and the audit log (project members who can change the project)
// @Tags posture
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Finding ID"
// @Param request body posture.FindingUpdate true "Suppression"
// @Success 200 {object} posture.Finding
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /posture/findings/{id} [patch]
func (c *Controller) UpdateFinding(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	id, ok := pathUUID(ctx, "id")
	if !ok {
		return
	}
	var request FindingUpdate
	if !bind(ctx, &request) {
		return
	}
	projectID, err := c.service.findingProject(ctx.Request.Context(), id)
	if err != nil {
		respond(ctx, nil, err)
		return
	}
	if decision, err := authz.Authorize(ctx, user, authz.ProjectWrite, authz.Project(projectID)); err != nil ||
		!decision.Allowed {
		respond(ctx, nil, ErrForbidden)
		return
	}
	finding, err := c.service.UpdateFinding(ctx.Request.Context(), user, id, request)
	respond(ctx, finding, err)
}

// GetFacets
// @Summary The values each posture filter can take with the others applied
// @Tags posture
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param projectId query string false "Only this project"
// @Param request body posture.Filters true "Filters"
// @Success 200 {object} posture.FacetsResponse
// @Router /posture/facets [post]
func (c *Controller) GetFacets(ctx *gin.Context) {
	var filters Filters
	if !bind(ctx, &filters) {
		return
	}
	projects, ok := c.projects(ctx, ctx.Query("projectId"))
	if !ok {
		return
	}
	facets, err := c.service.Facets(ctx.Request.Context(), projects, filters)
	respond(ctx, facets, err)
}

// GetTrends
// @Summary Posture over time
// @Description Per day, month, quarter or year: findings open at its end by severity, opened and resolved within it, and each standard's score
// @Tags posture
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param projectId query string false "Only this project"
// @Param request body posture.TrendsRequest true "Range, granularity and filters"
// @Success 200 {object} posture.TrendsResponse
// @Failure 400 {object} map[string]string
// @Router /posture/trends [post]
func (c *Controller) GetTrends(ctx *gin.Context) {
	var request TrendsRequest
	if !bind(ctx, &request) {
		return
	}
	projects, ok := c.projects(ctx, ctx.Query("projectId"))
	if !ok {
		return
	}
	trends, err := c.service.Trends(ctx.Request.Context(), projects, request)
	respond(ctx, trends, err)
}

// GetCoverage
// @Summary Posture coverage per scope
// @Description For each connection, account and region: native, fallback to the Obzly baseline, not covered or stale, with the reason
// @Tags posture
// @Produce json
// @Security BearerAuth
// @Param projectId query string false "Only this project"
// @Success 200 {array} posture.CoverageRow
// @Router /posture/coverage [get]
func (c *Controller) GetCoverage(ctx *gin.Context) {
	projects, ok := c.projects(ctx, ctx.Query("projectId"))
	if !ok {
		return
	}
	rows, err := c.service.Coverage(ctx.Request.Context(), projects)
	respond(ctx, rows, err)
}

// GetResource
// @Summary One resource's open posture findings
// @Description By its cloud ID (ARN or Azure resource ID), for the Infrastructure asset drawer
// @Tags posture
// @Produce json
// @Security BearerAuth
// @Param key query string true "Cloud resource ID"
// @Param projectId query string false "Only this project"
// @Success 200 {array} posture.Finding
// @Router /posture/resource [get]
func (c *Controller) GetResource(ctx *gin.Context) {
	key := ctx.Query("key")
	if key == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "key is the resource's cloud ID"})
		return
	}
	projects, ok := c.projects(ctx, ctx.Query("projectId"))
	if !ok {
		return
	}
	findings, err := c.service.Resource(ctx.Request.Context(), projects, key)
	respond(ctx, findings, err)
}

// GetAlertSettings
// @Summary Where a project's new posture findings are sent
// @Tags posture
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Success 200 {object} posture.AlertSettingsDTO
// @Router /projects/{id}/posture-alerts [get]
func (c *Controller) GetAlertSettings(ctx *gin.Context) {
	projectID, ok := pathUUID(ctx, "id")
	if !ok {
		return
	}
	settings, err := c.service.AlertSettings(ctx.Request.Context(), projectID)
	respond(ctx, settings, err)
}

// UpdateAlertSettings
// @Summary Set where a project's new posture findings are sent
// @Description Channels and the least severity that alerts; no channels turns the alert off
// @Tags posture
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Param request body posture.AlertSettingsDTO true "Settings"
// @Success 200 {object} posture.AlertSettingsDTO
// @Failure 400 {object} map[string]string
// @Router /projects/{id}/posture-alerts [put]
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

// ListActions
// @Summary Posture actions
// @Description Ranked changes that each fix a group of findings, guardrails that stop findings coming back and coverage fixes, with the standards' scores now, with the plan done and with every action done
// @Tags posture
// @Produce json
// @Security BearerAuth
// @Param projectId query string false "Only this project"
// @Success 200 {object} posture.ActionsDTO
// @Failure 403 {object} map[string]string
// @Router /posture/actions [get]
func (c *Controller) ListActions(ctx *gin.Context) {
	projects, ok := c.projects(ctx, ctx.Query("projectId"))
	if !ok {
		return
	}
	result, err := c.service.Actions(ctx.Request.Context(), projects)
	respond(ctx, result, err)
}

// UpdateAction
// @Summary Plan or dismiss a posture action
// @Description Project members. Dismissing needs a reason; both go into the audit log
// @Tags posture
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body posture.ActionUpdate true "The action's project, key and change"
// @Success 200 {object} posture.Action
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /posture/actions [patch]
func (c *Controller) UpdateAction(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	var request ActionUpdate
	if !bind(ctx, &request) {
		return
	}
	if decision, err := authz.Authorize(ctx, user, authz.ProjectWrite, authz.Project(request.ProjectID)); err != nil ||
		!decision.Allowed {
		respond(ctx, nil, ErrForbidden)
		return
	}
	action, err := c.service.UpdateAction(ctx.Request.Context(), user, request)
	respond(ctx, action, err)
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
