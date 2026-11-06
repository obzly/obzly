package organizations

import (
	"errors"
	"net/http"

	"obzly/internal/features/orgplan"
	users_middleware "obzly/internal/features/users/middleware"
	"obzly/internal/util/logger"

	"github.com/gin-gonic/gin"
)

// RegisterSitePlanRoutes mounts the plan document Sites fetch (control
// plane): public like the JWKS - it is signed for one Site, and a plan's
// limits aren't secret.
func (c *Controller) RegisterSitePlanRoutes(router *gin.Engine) {
	router.GET("/api/v1/site-plans/:siteId", c.GetSitePlan)
}

// GetPlan
// @Summary Get an organization's plan
// @Description Members; every limit 0 (unlimited) without a plan
// @Tags organizations
// @Produce json
// @Security BearerAuth
// @Param orgId path string true "orgId"
// @Success 200 {object} orgplan.OrgPlan
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /orgs/{orgId}/plan [get]
func (c *Controller) GetPlan(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	orgID, ok := pathID(ctx, "orgId")
	if !ok {
		return
	}
	plan, err := c.service.GetPlan(orgID, user)
	respond(ctx, plan, err)
}

// SetPlan
// @Summary Set an organization's plan
// @Description Global admins. Its Sites apply per-project limits (the stricter of a project's own and the plan's) and a project count per Site within minutes; the control plane enforces Sites and members
// @Tags organizations
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param orgId path string true "orgId"
// @Param request body orgplan.Limits true "Limits (0 is unlimited)"
// @Success 200 {object} orgplan.OrgPlan
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /orgs/{orgId}/plan [put]
func (c *Controller) SetPlan(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	orgID, ok := pathID(ctx, "orgId")
	if !ok {
		return
	}
	var limits orgplan.Limits
	if !bind(ctx, &limits) {
		return
	}
	plan, err := c.service.SetPlan(orgID, limits, user)
	respond(ctx, plan, err)
}

// GetSitePlan
// @Summary A Site's organization plan
// @Description The signed plan document (orgplan.Document) a Site fetches from its control plane
// @Tags organizations
// @Produce plain
// @Param siteId path string true "siteId"
// @Success 200 {string} string "ES256 JWT"
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /site-plans/{siteId} [get]
func (c *Controller) GetSitePlan(ctx *gin.Context) {
	document, err := c.service.SitePlanDocument(ctx.Param("siteId"))
	switch {
	case errors.Is(err, ErrNotFound):
		ctx.JSON(http.StatusNotFound, gin.H{"error": "Unknown Site"})
	case err != nil:
		logger.GetLogger().Error("Failed to sign a Site plan", "site", ctx.Param("siteId"), "error", err)
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to sign the plan"})
	default:
		ctx.Header("Cache-Control", "no-store")
		ctx.Data(http.StatusOK, "application/jwt", []byte(document))
	}
}
