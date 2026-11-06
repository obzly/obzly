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
	respond(ctx, body, err)
}
