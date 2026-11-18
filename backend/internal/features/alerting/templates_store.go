package alerting

import (
	"errors"
	"sync"
	"time"

	"obzly/internal/config"
	"obzly/internal/features/sharedconfig"
	"obzly/internal/storage"
	"obzly/internal/util/logger"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	templateCacheTTL    = 30 * time.Second
	projectNameCacheTTL = 5 * time.Minute
)

// Where a channel's template came from, for marking a render failure.
const (
	originBuiltIn  = "built-in"
	originOrg      = "organisation"
	originOverride = "override"
)

var templateCache struct {
	sync.Mutex
	rows map[string]*sharedconfig.MessageTemplate
	at   time.Time
}

var projectNames struct {
	sync.Mutex
	names map[uuid.UUID]string
	at    time.Time
}

// ForgetTemplates drops the cached organisation templates (after a save or
// a sync from the control plane).
func ForgetTemplates() {
	templateCache.Lock()
	templateCache.at = time.Time{}
	templateCache.Unlock()
}

// templateSource is where this deployment's organisation templates live: a
// control plane's own and an enrolled Site's (synced from it) are central,
// a standalone Site's local.
func templateSource() string {
	env := config.GetEnv()
	if env.ServiceMode.IsControlPlane() || env.SiteExchangeEnabled() {
		return sharedconfig.SourceCentral
	}
	return sharedconfig.SourceLocal
}

// orgTemplate is the stored organisation default for kind, nil when kind
// uses the built-in template.
func orgTemplate(kind string) (*sharedconfig.MessageTemplate, error) {
	templateCache.Lock()
	defer templateCache.Unlock()
	if time.Since(templateCache.at) >= templateCacheTTL {
		var rows []sharedconfig.MessageTemplate
		if err := storage.GetDb().Where("source = ?", templateSource()).Find(&rows).Error; err != nil {
			return nil, err
		}
		templateCache.rows = map[string]*sharedconfig.MessageTemplate{}
		for i := range rows {
			templateCache.rows[rows[i].Kind] = &rows[i]
		}
		templateCache.at = time.Now()
	}
	return templateCache.rows[kind], nil
}

// effectiveTemplate is what a channel renders with: its own override, else
// the organisation default for its kind, else the built-in template.
func effectiveTemplate(kind string, override bool, subject, body string) (MessageTemplate, string, error) {
	if override {
		return MessageTemplate{Subject: subject, Body: body}, originOverride, nil
	}
	stored, err := orgTemplate(kind)
	if err != nil {
		return DefaultTemplate(kind), originBuiltIn, err
	}
	if stored != nil {
		return MessageTemplate{Subject: stored.Subject, Body: stored.Body}, originOrg, nil
	}
	return DefaultTemplate(kind), originBuiltIn, nil
}

// newRenderContext is the names and link n renders with on this deployment.
func newRenderContext(n Notification, useEmoji bool) renderContext {
	env := config.GetEnv()
	siteName := env.SiteName
	if siteName == "" {
		siteName = env.SiteID
	}
	return renderContext{
		ProjectName: projectName(n.ProjectID), SiteID: env.SiteID, SiteName: siteName,
		Link: notificationLink(n), UseEmoji: useEmoji, Now: time.Now().UTC(),
	}
}

// projectName is the project's name, cached; empty when unknown.
func projectName(projectID uuid.UUID) string {
	if projectID == uuid.Nil {
		return ""
	}
	projectNames.Lock()
	defer projectNames.Unlock()
	if time.Since(projectNames.at) >= projectNameCacheTTL || projectNames.names == nil {
		projectNames.names, projectNames.at = map[uuid.UUID]string{}, time.Now()
	}
	if name, ok := projectNames.names[projectID]; ok {
		return name
	}
	var name string
	if err := storage.GetDb().Table("projects").Select("name").Where("id = ?", projectID).
		Scan(&name).Error; err != nil {
		return ""
	}
	projectNames.names[projectID] = name
	return name
}

// markTemplateResult records a channel's render failure where the UI shows
// it (the channel for an override, the organisation template otherwise) and
// clears it after a success.
func markTemplateResult(channel *NotificationChannel, origin string, renderErr error) {
	now := time.Now().UTC()
	switch origin {
	case originOverride:
		message := ""
		if renderErr != nil {
			message = truncate(renderErr.Error(), 1000)
		}
		if message == channel.TemplateError {
			return
		}
		updates := map[string]any{"template_error": message, "template_error_at": nil}
		if message != "" {
			updates["template_error_at"] = now
		}
		if err := storage.GetDb().Model(&NotificationChannel{}).Where("id = ?", channel.ID).
			Updates(updates).Error; err != nil {
			logger.GetLogger().
				Error("failed to record a channel's template error", "channelId", channel.ID, "error", err)
		}
	case originOrg:
		stored, err := orgTemplate(channel.Kind)
		if err != nil || stored == nil {
			return
		}
		message := ""
		if renderErr != nil {
			message = truncate(renderErr.Error(), 1000)
		}
		if message == stored.LastError {
			return
		}
		updates := map[string]any{"last_error": message, "last_error_at": nil}
		if message != "" {
			updates["last_error_at"] = now
		}
		err = storage.GetDb().Model(&sharedconfig.MessageTemplate{}).
			Where("source = ? AND kind = ?", stored.Source, stored.Kind).Updates(updates).Error
		if err != nil {
			logger.GetLogger().Error("failed to record a template error", "kind", channel.Kind, "error", err)
			return
		}
		ForgetTemplates()
	}
}

// storedTemplate is the stored organisation template for kind at source.
func storedTemplate(source, kind string) (*sharedconfig.MessageTemplate, error) {
	var row sharedconfig.MessageTemplate
	err := storage.GetDb().Where("source = ? AND kind = ?", source, kind).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &row, err
}
