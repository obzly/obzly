package webanalytics

import (
	"errors"
	"net/http"
	"strings"
	"time"

	users_middleware "obzly/internal/features/users/middleware"
	users_models "obzly/internal/features/users/models"
	"obzly/internal/util/logger"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type Controller struct {
	service *Service
}

func (c *Controller) RegisterRoutes(router *gin.RouterGroup) {
	router.GET("/projects/:id/web-analytics/report", c.GetReport)
	router.GET("/projects/:id/web-analytics/settings", c.GetSettings)
	router.PUT("/projects/:id/web-analytics/settings", c.UpdateSettings)
}

// GetReport
// @Summary Web analytics report from a project's access logs
// @Description Requests, visitors, status codes and top lists for a range: closed hours from hourly rollups (built a few minutes after each hour) merged with the current hour read from raw logs. Ranges under 2 hours are read from raw logs. The first report of a project starts its rollups (the last 7 days are backfilled).
// @Tags web-analytics
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID (UUID format)"
// @Param from query string false "Range start (RFC 3339, default 24 hours before to)"
// @Param to query string false "Range end (RFC 3339, default now)"
// @Param sources query string false "Comma-separated source keys (default all)"
// @Param host query string false "Only this virtual host"
// @Success 200 {object} webanalytics.ReportDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /projects/{id}/web-analytics/report [get]
func (c *Controller) GetReport(ctx *gin.Context) {
	user, projectID, ok := userAndProject(ctx)
	if !ok {
		return
	}
	params := ReportParams{Host: strings.TrimSpace(ctx.Query("host"))}
	for _, part := range []struct {
		name   string
		target *time.Time
	}{{"from", &params.From}, {"to", &params.To}} {
		if raw := ctx.Query(part.name); raw != "" {
			parsed, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid " + part.name + ": use RFC 3339"})
				return
			}
			*part.target = parsed
		}
	}
	if raw := ctx.Query("sources"); raw != "" {
		params.Sources = strings.Split(raw, ",")
	}
	report, err := c.service.Report(ctx.Request.Context(), user, projectID, params)
	respond(ctx, report, err, "Failed to build the Web analytics report")
}

// GetSettings
// @Summary Web analytics settings, sources and rollup status
// @Tags web-analytics
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID (UUID format)"
// @Success 200 {object} webanalytics.SettingsDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /projects/{id}/web-analytics/settings [get]
func (c *Controller) GetSettings(ctx *gin.Context) {
	user, projectID, ok := userAndProject(ctx)
	if !ok {
		return
	}
	settings, err := c.service.Settings(ctx.Request.Context(), user, projectID)
	respond(ctx, settings, err, "Failed to load Web analytics settings")
}

// UpdateSettings
// @Summary Change Web analytics settings and source names
// @Description Project admins only. Visitor IPs (truncate, hash or full), crawlers and static extensions apply to hours summarised afterwards. Sources can be renamed or merged into another. Every change is audit-logged.
// @Tags web-analytics
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID (UUID format)"
// @Param request body webanalytics.UpdateSettingsDTO true "Settings to change"
// @Success 200 {object} webanalytics.SettingsDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /projects/{id}/web-analytics/settings [put]
func (c *Controller) UpdateSettings(ctx *gin.Context) {
	user, projectID, ok := userAndProject(ctx)
	if !ok {
		return
	}
	var request UpdateSettingsDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}
	settings, err := c.service.UpdateSettings(ctx.Request.Context(), user, projectID, &request)
	respond(ctx, settings, err, "Failed to save Web analytics settings")
}

func userAndProject(ctx *gin.Context) (*users_models.User, uuid.UUID, bool) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return nil, uuid.Nil, false
	}
	projectID, err := uuid.Parse(ctx.Param("id"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid project ID format"})
		return nil, uuid.Nil, false
	}
	return user, projectID, true
}

func respond(ctx *gin.Context, body any, err error, failure string) {
	var validation *ValidationError
	switch {
	case err == nil:
		ctx.JSON(http.StatusOK, body)
	case errors.Is(err, ErrForbidden), errors.Is(err, ErrManageForbidden):
		ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	case errors.Is(err, ErrInvalidRange):
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.As(err, &validation):
		ctx.JSON(http.StatusBadRequest, gin.H{"error": validation.Message})
	default:
		logger.GetLogger().Error(failure, "error", err)
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": failure})
	}
}
