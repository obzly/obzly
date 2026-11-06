package control_plane_controllers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	control_plane_dto "obzly/internal/features/control_plane/dto"
	control_plane_models "obzly/internal/features/control_plane/models"
	control_plane_services "obzly/internal/features/control_plane/services"
	users_middleware "obzly/internal/features/users/middleware"
	"obzly/internal/util/logger"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// SiteController is the Site registry's HTTP surface. ListSites (any user) never
// includes ApiURL; the central-ADMIN /admin/* routes do.
type SiteController struct {
	siteService  *control_plane_services.SiteService
	grantService *control_plane_services.GrantService
}

func (c *SiteController) RegisterRoutes(protected *gin.RouterGroup) {
	protected.GET("/control-plane/sites", c.ListSites)
	protected.GET("/control-plane/sites/status", c.GetReachableSiteStatuses)

	protected.GET("/control-plane/admin/sites", c.ListSitesAdmin)
	protected.POST("/control-plane/admin/sites", c.CreateSite)
	protected.PUT("/control-plane/admin/sites/:id", c.UpdateSite)
	protected.DELETE("/control-plane/admin/sites/:id", c.DeleteSite)
	protected.POST("/control-plane/admin/sites/:id/check", c.CheckSite)
	protected.GET("/control-plane/admin/sites/status", c.GetSiteStatuses)
}

// ListSites
// @Summary List the Sites the caller can reach
// @Tags control-plane
// @Produce json
// @Success 200
// @Router /control-plane/sites [get]
func (c *SiteController) ListSites(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
		return
	}

	reachable, err := c.reachableSites(user.ID)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list sites"})
		return
	}

	response := make([]control_plane_dto.SiteResponseDTO, 0, len(reachable))
	for _, r := range reachable {
		response = append(response, control_plane_dto.SiteResponseDTO{
			ID: r.site.ID, Name: r.site.Name, Enabled: r.site.Enabled, SiteRole: r.siteRole,
			DisplayName: r.site.DisplayName, Environment: r.site.Environment,
		})
	}
	ctx.JSON(http.StatusOK, response)
}

// GetReachableSiteStatuses
// @Summary Traffic-light status of the Sites the caller can reach
// @Description Only the Site ID and status (up, degraded, down) - the reason, version and latency are admin-only.
// @Tags control-plane
// @Produce json
// @Success 200
// @Router /control-plane/sites/status [get]
func (c *SiteController) GetReachableSiteStatuses(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
		return
	}

	reachable, err := c.reachableSites(user.ID)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list sites"})
		return
	}

	sites := make([]*control_plane_models.Site, len(reachable))
	for i, r := range reachable {
		sites[i] = r.site
	}
	statuses := control_plane_services.CheckSiteStatuses(ctx.Request.Context(), sites)
	response := make([]gin.H, len(statuses))
	for i, st := range statuses {
		response[i] = gin.H{"siteId": st.SiteID, "status": st.Status}
	}
	ctx.JSON(http.StatusOK, gin.H{"statuses": response})
}

type reachableSite struct {
	site *control_plane_models.Site
	// The Site-wide role (ADMIN / MEMBER / VIEWER), or "" for
	// project-scoped access only.
	siteRole string
}

// reachableSites is every enabled Site the user has any grant on.
func (c *SiteController) reachableSites(userID uuid.UUID) ([]reachableSite, error) {
	sites, err := c.siteService.ListSites()
	if err != nil {
		return nil, err
	}
	grants, err := c.grantService.ResolveEffectiveGrants(userID)
	if err != nil {
		return nil, err
	}

	reachable := make([]reachableSite, 0, len(sites))
	for _, site := range sites {
		grant := grants[site.ID]
		if !site.Enabled || !grant.HasAnyAccess() {
			continue
		}
		reachable = append(reachable, reachableSite{site: site, siteRole: grant.SiteRole})
	}
	return reachable, nil
}

func toAdminDTO(site *control_plane_models.Site) control_plane_dto.SiteAdminResponseDTO {
	return control_plane_dto.SiteAdminResponseDTO{
		ID: site.ID, Name: site.Name, ApiURL: site.ApiURL, Enabled: site.Enabled,
		CreatedAt: site.CreatedAt, UpdatedAt: site.UpdatedAt,
		DisplayName: site.DisplayName, Environment: site.Environment,
	}
}

// ListSitesAdmin
// @Summary List every registered Site (admin)
// @Tags control-plane
// @Produce json
// @Success 200
// @Failure 403
// @Router /control-plane/admin/sites [get]
func (c *SiteController) ListSitesAdmin(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok || !user.CanUpdateSettings() {
		ctx.JSON(http.StatusForbidden, gin.H{"error": "insufficient permissions"})
		return
	}

	sites, err := c.siteService.ListSites()
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list sites"})
		return
	}
	response := make([]control_plane_dto.SiteAdminResponseDTO, 0, len(sites))
	for _, site := range sites {
		response = append(response, toAdminDTO(site))
	}
	ctx.JSON(http.StatusOK, response)
}

// CreateSite
// @Summary Register a new Site (admin)
// @Tags control-plane
// @Accept json
// @Produce json
// @Param request body control_plane_dto.CreateSiteRequestDTO true "Site"
// @Success 200
// @Failure 400
// @Failure 403
// @Router /control-plane/admin/sites [post]
func (c *SiteController) CreateSite(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
		return
	}

	var request control_plane_dto.CreateSiteRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid request format"})
		return
	}

	site, err := c.siteService.CreateSite(user, request)
	if err != nil {
		writeSiteError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, toAdminDTO(site))
}

// UpdateSite
// @Summary Update a Site (admin)
// @Tags control-plane
// @Accept json
// @Produce json
// @Param id path string true "Site ID"
// @Param request body control_plane_dto.UpdateSiteRequestDTO true "Site"
// @Success 200
// @Failure 400
// @Failure 403
// @Failure 404
// @Router /control-plane/admin/sites/{id} [put]
func (c *SiteController) UpdateSite(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
		return
	}

	var request control_plane_dto.UpdateSiteRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid request format"})
		return
	}

	site, err := c.siteService.UpdateSite(user, ctx.Param("id"), request)
	if err != nil {
		writeSiteError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, toAdminDTO(site))
}

// DeleteSite
// @Summary Remove a Site (admin)
// @Tags control-plane
// @Produce json
// @Param id path string true "Site ID"
// @Success 200
// @Failure 403
// @Router /control-plane/admin/sites/{id} [delete]
func (c *SiteController) DeleteSite(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
		return
	}
	if err := c.siteService.DeleteSite(user, ctx.Param("id")); err != nil {
		writeSiteError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"success": true})
}

// GetSiteStatuses
// @Summary Traffic-light status of every Site (admin)
// @Description Server-side checks of each Site's /api/v1/site/info and /api/v1/system/health: up, degraded, down or disabled. Results are cached for 15 seconds.
// @Tags control-plane
// @Produce json
// @Success 200
// @Failure 403
// @Router /control-plane/admin/sites/status [get]
func (c *SiteController) GetSiteStatuses(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok || !user.CanUpdateSettings() {
		ctx.JSON(http.StatusForbidden, gin.H{"error": "insufficient permissions"})
		return
	}

	sites, err := c.siteService.ListSites()
	if err != nil {
		logger.GetLogger().Error("failed to list sites for status", "error", err)
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load sites"})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{
		"statuses": control_plane_services.CheckSiteStatuses(ctx.Request.Context(), sites),
	})
}

// CheckSite
// @Summary Check connectivity to a Site (admin)
// @Description Server-side call to the Site's own GET /api/v1/site/info - the api_url never reaches the browser
// @Tags control-plane
// @Produce json
// @Param id path string true "Site ID"
// @Success 200
// @Failure 403
// @Failure 404
// @Router /control-plane/admin/sites/{id}/check [post]
func (c *SiteController) CheckSite(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok || !user.CanUpdateSettings() {
		ctx.JSON(http.StatusForbidden, gin.H{"error": "insufficient permissions"})
		return
	}

	site, err := c.siteService.GetSite(ctx.Param("id"))
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load site"})
		return
	}
	if site == nil {
		ctx.JSON(http.StatusNotFound, gin.H{"error": "site not found"})
		return
	}

	ctx.JSON(http.StatusOK, checkSiteInfo(ctx.Request.Context(), site))
}

func checkSiteInfo(ctx context.Context, site *control_plane_models.Site) control_plane_dto.SiteCheckResponseDTO {
	// Same egress check as the token exchange, so this can't be used to probe internal
	// addresses.
	if err := control_plane_services.CheckEgressAllowed(site.ApiURL); err != nil {
		return control_plane_dto.SiteCheckResponseDTO{Error: "site address is not allowed"}
	}

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(
		reqCtx,
		http.MethodGet,
		strings.TrimRight(site.ApiURL, "/")+"/api/v1/site/info",
		nil,
	)
	if err != nil {
		return control_plane_dto.SiteCheckResponseDTO{Error: "invalid site url"}
	}

	resp, err := control_plane_services.CheckHTTPClient.Do(req)
	if err != nil {
		return control_plane_dto.SiteCheckResponseDTO{Error: "site unreachable"}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return control_plane_dto.SiteCheckResponseDTO{Reachable: true, Error: "unexpected response from site"}
	}

	info, err := control_plane_services.DecodeSiteInfo(resp.Body)
	if err != nil {
		return control_plane_dto.SiteCheckResponseDTO{Reachable: true, Error: "could not parse site response"}
	}

	return control_plane_dto.SiteCheckResponseDTO{
		Reachable:      true,
		SiteIDMatches:  info.SiteID == site.ID,
		ReportedSiteID: info.SiteID,
		Version:        info.Version,
	}
}

// writeSiteError maps sentinel errors to statuses; other errors may contain DB details
// and are logged, not returned.
func writeSiteError(ctx *gin.Context, err error) {
	switch {
	case errors.Is(err, control_plane_services.ErrSiteForbidden):
		ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	case errors.Is(err, control_plane_services.ErrSiteNotFound):
		ctx.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, control_plane_services.ErrSiteIDExists),
		errors.Is(err, control_plane_services.ErrSiteInvalidID),
		errors.Is(err, control_plane_services.ErrSiteInvalidAPURL),
		errors.Is(err, control_plane_services.ErrSiteInvalidDisplayName),
		errors.Is(err, control_plane_services.ErrSiteInvalidEnvironment):
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	default:
		logger.GetLogger().Error("site management request failed", "error", err)
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "request failed"})
	}
}
