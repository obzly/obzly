package alerting

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	users_middleware "obzly/internal/features/users/middleware"
)

// SettingsController is Obzly settings → Notification channels, on a
// control plane and on a Site.
type SettingsController struct {
	service *SettingsService
}

func (c *SettingsController) RegisterRoutes(router *gin.RouterGroup) {
	router.GET("/settings/channels", c.ListSettingsChannels)
	router.POST("/settings/channels", c.CreateSettingsChannel)
	router.PUT("/settings/channels/:channelId", c.UpdateSettingsChannel)
	router.DELETE("/settings/channels/:channelId", c.DeleteSettingsChannel)
	router.POST("/settings/channels/:channelId/test", c.TestSettingsChannel)
	router.PUT("/settings/channels/:channelId/template", c.UpdateChannelTemplate)
	router.GET("/settings/message-templates", c.ListMessageTemplates)
	router.PUT("/settings/message-templates/:kind", c.UpdateMessageTemplate)
	router.DELETE("/settings/message-templates/:kind", c.ResetMessageTemplate)
	router.POST("/settings/message-templates/preview", c.PreviewMessageTemplate)
	router.POST("/settings/message-templates/test", c.TestMessageTemplate)
}

// UpdateChannelTemplate
// @Summary Set a channel's message template and emoji switch
// @Description Admins; the organisation default, or the channel's own template (validated). On a Site enrolled in a control plane, refused (managed there)
// @Tags alerting
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param channelId path string true "Channel ID"
// @Param request body ChannelTemplateRequest true "Template"
// @Success 200 {object} alerting.SettingsChannel
// @Failure 400 {object} map[string]any
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Router /settings/channels/{channelId}/template [put]
func (c *SettingsController) UpdateChannelTemplate(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	id, ok := pathID(ctx, "channelId")
	if !ok {
		return
	}
	var request ChannelTemplateRequest
	if !bind(ctx, &request) {
		return
	}
	channel, err := c.service.SaveChannelTemplate(user, id, request)
	respondSettings(ctx, channel, err)
}

// ListMessageTemplates
// @Summary List the organisation's message templates
// @Description Admins. Each channel kind's organisation default (stored, or the built-in one) with its last send-time render error
// @Tags alerting
// @Produce json
// @Security BearerAuth
// @Success 200 {object} alerting.MessageTemplatesView
// @Failure 403 {object} map[string]string
// @Router /settings/message-templates [get]
func (c *SettingsController) ListMessageTemplates(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	view, err := c.service.MessageTemplates(user)
	respondSettings(ctx, view, err)
}

// UpdateMessageTemplate
// @Summary Save a channel kind's message template
// @Description Admins; validated (syntax, size, renders every sample, webhook JSON). Audit-logged. On a Site enrolled in a control plane, refused (managed there)
// @Tags alerting
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param kind path string true "email, slack, pagerduty or webhook"
// @Param request body MessageTemplate true "Template"
// @Success 200 {object} alerting.MessageTemplatesView
// @Failure 400 {object} map[string]any
// @Failure 403 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Router /settings/message-templates/{kind} [put]
func (c *SettingsController) UpdateMessageTemplate(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	var request MessageTemplate
	if !bind(ctx, &request) {
		return
	}
	view, err := c.service.SaveMessageTemplate(user, ctx.Param("kind"), request)
	respondSettings(ctx, view, err)
}

// ResetMessageTemplate
// @Summary Reset a channel kind's message template to the built-in one
// @Description Admins; audit-logged
// @Tags alerting
// @Produce json
// @Security BearerAuth
// @Param kind path string true "email, slack, pagerduty or webhook"
// @Success 200 {object} alerting.MessageTemplatesView
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Router /settings/message-templates/{kind} [delete]
func (c *SettingsController) ResetMessageTemplate(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	view, err := c.service.ResetMessageTemplate(user, ctx.Param("kind"))
	respondSettings(ctx, view, err)
}

// PreviewMessageTemplate
// @Summary Preview a message template
// @Description Admins. Renders an unsaved template against sample firing (each severity), resolved, test and incident notifications; errors carry line numbers
// @Tags alerting
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body TemplatePreviewRequest true "Template"
// @Success 200 {object} alerting.TemplatePreviewResponse
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /settings/message-templates/preview [post]
func (c *SettingsController) PreviewMessageTemplate(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	var request TemplatePreviewRequest
	if !bind(ctx, &request) {
		return
	}
	preview, err := c.service.PreviewMessageTemplate(user, request)
	respondSettings(ctx, preview, err)
}

// TestMessageTemplate
// @Summary Send a test rendered with a message template
// @Description Admins. Sends a test notification rendered with the given (unsaved) template through a channel of the same kind
// @Tags alerting
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body TemplateTestRequest true "Template and channel"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]any
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /settings/message-templates/test [post]
func (c *SettingsController) TestMessageTemplate(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	var request TemplateTestRequest
	if !bind(ctx, &request) {
		return
	}
	err := c.service.TestMessageTemplate(ctx.Request.Context(), user, request)
	respondSettings(ctx, gin.H{"message": "Test notification sent"}, err)
}

// ListSettingsChannels
// @Summary List notification channels
// @Description Admins. A control plane's central channels, or a Site's channels (its own and those its control plane sent)
// @Tags alerting
// @Produce json
// @Security BearerAuth
// @Success 200 {array} alerting.SettingsChannel
// @Failure 403 {object} map[string]string
// @Router /settings/channels [get]
func (c *SettingsController) ListSettingsChannels(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	channels, err := c.service.List(user)
	respondSettings(ctx, channels, err)
}

// CreateSettingsChannel
// @Summary Create a notification channel
// @Description Admins; on a Site enrolled in a control plane, refused (managed there)
// @Tags alerting
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body SettingsChannelRequest true "Channel"
// @Success 200 {object} alerting.SettingsChannel
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Router /settings/channels [post]
func (c *SettingsController) CreateSettingsChannel(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	var request SettingsChannelRequest
	if !bind(ctx, &request) {
		return
	}
	channel, err := c.service.Create(user, request)
	respondSettings(ctx, channel, err)
}

// UpdateSettingsChannel
// @Summary Update a notification channel
// @Description A nil config keeps the stored one
// @Tags alerting
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param channelId path string true "Channel ID"
// @Param request body SettingsChannelRequest true "Channel"
// @Success 200 {object} alerting.SettingsChannel
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Router /settings/channels/{channelId} [put]
func (c *SettingsController) UpdateSettingsChannel(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	id, ok := pathID(ctx, "channelId")
	if !ok {
		return
	}
	var request SettingsChannelRequest
	if !bind(ctx, &request) {
		return
	}
	channel, err := c.service.Update(user, id, request)
	respondSettings(ctx, channel, err)
}

// DeleteSettingsChannel
// @Summary Delete a notification channel
// @Description Rules that used it stop sending to it
// @Tags alerting
// @Produce json
// @Security BearerAuth
// @Param channelId path string true "Channel ID"
// @Success 200 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Router /settings/channels/{channelId} [delete]
func (c *SettingsController) DeleteSettingsChannel(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	id, ok := pathID(ctx, "channelId")
	if !ok {
		return
	}
	respondSettings(ctx, gin.H{"message": "Channel deleted"}, c.service.Delete(user, id))
}

// TestSettingsChannel
// @Summary Send a test notification
// @Tags alerting
// @Produce json
// @Security BearerAuth
// @Param channelId path string true "Channel ID"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /settings/channels/{channelId}/test [post]
func (c *SettingsController) TestSettingsChannel(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	id, ok := pathID(ctx, "channelId")
	if !ok {
		return
	}
	respondSettings(ctx, gin.H{"message": "Test notification sent"}, c.service.Test(ctx.Request.Context(), user, id))
}

func respondSettings(ctx *gin.Context, body any, err error) {
	if errors.Is(err, ErrManagedCentrally) {
		ctx.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	if problems, ok := templateErrorsOf(err); ok {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": problems.Error(), "errors": problems})
		return
	}
	respond(ctx, body, err)
}
