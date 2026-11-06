package alerting

import (
	"errors"
	"net/http"
	"obzly/internal/authz"

	users_middleware "obzly/internal/features/users/middleware"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type Controller struct {
	service *Service
}

func (c *Controller) RegisterRoutes(router *gin.RouterGroup) {
	router.GET("/projects/:id/alert-rules", authz.Require(authz.ProjectRead), c.ListRules)
	router.POST("/projects/:id/alert-rules", authz.Require(authz.ProjectManage), c.CreateRule)
	router.PUT("/projects/:id/alert-rules/:ruleId", authz.Require(authz.ProjectManage), c.UpdateRule)
	router.DELETE("/projects/:id/alert-rules/:ruleId", authz.Require(authz.ProjectManage), c.DeleteRule)
	router.POST("/projects/:id/alert-rules/preview", authz.Require(authz.ProjectManage), c.PreviewRule)
	router.GET("/projects/:id/alerts", authz.Require(authz.ProjectRead), c.ListAlerts)
	router.GET("/projects/:id/notification-channels", authz.Require(authz.ProjectRead), c.ListChannels)
	router.POST("/projects/:id/notification-channels/:channelId/test", authz.Require(authz.ProjectOwn), c.TestChannel)
	router.GET("/projects/:id/silences", authz.Require(authz.ProjectRead), c.ListSilences)
	router.POST("/projects/:id/silences", authz.Require(authz.ProjectWrite), c.CreateSilence)
	router.DELETE("/projects/:id/silences/:silenceId", authz.Require(authz.ProjectWrite), c.DeleteSilence)
}

// ListRules
// @Summary List alert rules
// @Description A project's alert rules (any member)
// @Tags alerting
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Success 200 {array} alerting.AlertRule
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /projects/{id}/alert-rules [get]
func (c *Controller) ListRules(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	projectID, ok := pathID(ctx, "id")
	if !ok {
		return
	}
	rules, err := c.service.ListRules(projectID, user)
	respond(ctx, rules, err)
}

// CreateRule
// @Summary Create an alert rule
// @Description Project owners and admins
// @Tags alerting
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Param request body RuleRequest true "Request"
// @Success 200 {object} alerting.AlertRule
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /projects/{id}/alert-rules [post]
func (c *Controller) CreateRule(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	projectID, ok := pathID(ctx, "id")
	if !ok {
		return
	}
	var request RuleRequest
	if !bind(ctx, &request) {
		return
	}
	rule, err := c.service.CreateRule(projectID, request, user)
	respond(ctx, rule, err)
}

// UpdateRule
// @Summary Update an alert rule
// @Description Project owners and admins
// @Tags alerting
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Param ruleId path string true "ID"
// @Param request body RuleRequest true "Request"
// @Success 200 {object} alerting.AlertRule
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /projects/{id}/alert-rules/{ruleId} [put]
func (c *Controller) UpdateRule(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	projectID, ok := pathID(ctx, "id")
	if !ok {
		return
	}
	ruleID, ok := pathID(ctx, "ruleId")
	if !ok {
		return
	}
	var request RuleRequest
	if !bind(ctx, &request) {
		return
	}
	rule, err := c.service.UpdateRule(projectID, ruleID, request, user)
	respond(ctx, rule, err)
}

// DeleteRule
// @Summary Delete an alert rule
// @Description Project owners and admins
// @Tags alerting
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Param ruleId path string true "ID"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /projects/{id}/alert-rules/{ruleId} [delete]
func (c *Controller) DeleteRule(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	projectID, ok := pathID(ctx, "id")
	if !ok {
		return
	}
	ruleID, ok := pathID(ctx, "ruleId")
	if !ok {
		return
	}
	respond(ctx, gin.H{"message": "Alert rule deleted"}, c.service.DeleteRule(projectID, ruleID, user))
}

// PreviewRule
// @Summary Preview an alert rule
// @Description Runs the rule's query once and shows which series are past the threshold (project owners and admins: it runs a full query)
// @Tags alerting
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Param request body RuleRequest true "Request"
// @Success 200 {array} alerting.PreviewSeries
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /projects/{id}/alert-rules/preview [post]
func (c *Controller) PreviewRule(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	projectID, ok := pathID(ctx, "id")
	if !ok {
		return
	}
	var request RuleRequest
	if !bind(ctx, &request) {
		return
	}
	preview, err := c.service.PreviewRule(ctx.Request.Context(), projectID, request, user)
	respond(ctx, preview, err)
}

// ListAlerts
// @Summary List alerts
// @Description Pending, firing and recently resolved alerts (any member)
// @Tags alerting
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Success 200 {array} alerting.AlertView
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /projects/{id}/alerts [get]
func (c *Controller) ListAlerts(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	projectID, ok := pathID(ctx, "id")
	if !ok {
		return
	}
	alerts, err := c.service.ListAlerts(projectID, user)
	respond(ctx, alerts, err)
}

// ListChannels
// @Summary List notification channels
// @Description Channels this project may use (any member); where they send is shown to project owners and admins
// @Tags alerting
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Success 200 {array} alerting.NotificationChannel
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /projects/{id}/notification-channels [get]
func (c *Controller) ListChannels(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	projectID, ok := pathID(ctx, "id")
	if !ok {
		return
	}
	channels, err := c.service.ListChannels(projectID, user)
	respond(ctx, channels, err)
}

// TestChannel
// @Summary Send a test notification
// @Description The project owner or a global admin; only the receiver's status is reported
// @Tags alerting
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Param channelId path string true "ID"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /projects/{id}/notification-channels/{channelId}/test [post]
func (c *Controller) TestChannel(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	projectID, ok := pathID(ctx, "id")
	if !ok {
		return
	}
	channelID, ok := pathID(ctx, "channelId")
	if !ok {
		return
	}
	if err := c.service.TestChannel(
		ctx.Request.Context(),
		projectID,
		channelID,
		user,
	); err != nil && !errors.Is(err, ErrForbidden) &&
		!errors.Is(err, ErrNotFound) {
		ctx.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	} else if err != nil {
		respond(ctx, nil, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "Test notification sent"})
}

// ListSilences
// @Summary List silences
// @Description Current and upcoming silences (any member), including read-only ones from Synthetics maintenance windows (managedBy set)
// @Tags alerting
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Success 200 {array} alerting.Silence
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /projects/{id}/silences [get]
func (c *Controller) ListSilences(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	projectID, ok := pathID(ctx, "id")
	if !ok {
		return
	}
	silences, err := c.service.ListSilences(projectID, user)
	respond(ctx, silences, err)
}

// CreateSilence
// @Summary Create a silence
// @Description Members who can write to the project
// @Tags alerting
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Param request body SilenceRequest true "Request"
// @Success 200 {object} alerting.Silence
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /projects/{id}/silences [post]
func (c *Controller) CreateSilence(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	projectID, ok := pathID(ctx, "id")
	if !ok {
		return
	}
	var request SilenceRequest
	if !bind(ctx, &request) {
		return
	}
	silence, err := c.service.CreateSilence(projectID, request, user)
	respond(ctx, silence, err)
}

// DeleteSilence
// @Summary Delete a silence
// @Description Members who can write to the project; a maintenance window's silence (managedBy set) is refused with 409
// @Tags alerting
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Param silenceId path string true "ID"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Router /projects/{id}/silences/{silenceId} [delete]
func (c *Controller) DeleteSilence(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	projectID, ok := pathID(ctx, "id")
	if !ok {
		return
	}
	silenceID, ok := pathID(ctx, "silenceId")
	if !ok {
		return
	}
	respond(ctx, gin.H{"message": "Silence deleted"}, c.service.DeleteSilence(projectID, silenceID, user))
}

func pathID(ctx *gin.Context, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(ctx.Param(name))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid " + name})
		return uuid.Nil, false
	}
	return id, true
}

func bind(ctx *gin.Context, target any) bool {
	if err := ctx.ShouldBindJSON(target); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return false
	}
	return true
}

// respond maps the service's errors: permission 403, missing 404, changing
// something managed elsewhere 409, any other (validation) 400.
func respond(ctx *gin.Context, body any, err error) {
	switch {
	case err == nil:
		ctx.JSON(http.StatusOK, body)
	case errors.Is(err, ErrForbidden):
		ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	case errors.Is(err, ErrNotFound):
		ctx.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, ErrManaged), errors.Is(err, ErrManagedSilence), errors.Is(err, ErrManagedByBudget):
		ctx.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	default:
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	}
}
