package alerting

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"obzly/internal/config"
	"obzly/internal/features/sharedconfig"
	users_models "obzly/internal/features/users/models"
	"obzly/internal/storage"
)

// ChannelKinds in the order Message templates shows them.
var ChannelKinds = []string{ChannelEmail, ChannelSlack, ChannelPagerDuty, ChannelWebhook}

// MessageTemplateView is a channel kind's organisation default as Obzly
// settings › Message templates shows it.
type MessageTemplateView struct {
	Kind    string `json:"kind"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
	// IsDefault: nothing stored, the built-in template applies.
	IsDefault      bool       `json:"isDefault"`
	DefaultSubject string     `json:"defaultSubject"`
	DefaultBody    string     `json:"defaultBody"`
	UpdatedAt      *time.Time `json:"updatedAt"`
	// LastError is the last send-time render failure (the built-in template
	// was sent instead), on the deployment that sends.
	LastError   string     `json:"lastError"`
	LastErrorAt *time.Time `json:"lastErrorAt"`
}

// MessageTemplatesView is every kind's template; Managed: read-only here
// (a Site enrolled in a control plane gets them from it).
type MessageTemplatesView struct {
	Managed   bool                  `json:"managed"`
	Templates []MessageTemplateView `json:"templates"`
}

// TemplatePreviewRequest renders a template that isn't saved yet.
type TemplatePreviewRequest struct {
	Kind    string `json:"kind"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
	// UseEmoji defaults to on.
	UseEmoji *bool `json:"useEmoji"`
}

// TemplatePreviewSample is one sample notification rendered.
type TemplatePreviewSample struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Status   string `json:"status"`
	Severity string `json:"severity"`
	Emoji    string `json:"emoji"`
	Subject  string `json:"subject,omitempty"`
	Text     string `json:"text,omitempty"`
	// Body is a webhook's JSON, indented.
	Body  string `json:"body,omitempty"`
	Color string `json:"color,omitempty"`
	Error string `json:"error,omitempty"`
}

// TemplatePreviewResponse is the samples rendered, or why it can't render.
type TemplatePreviewResponse struct {
	Errors  TemplateErrors          `json:"errors"`
	Samples []TemplatePreviewSample `json:"samples"`
}

// TemplateTestRequest sends a test notification rendered with a template
// (saved or not) through a channel of the same kind.
type TemplateTestRequest struct {
	TemplatePreviewRequest
	ChannelID uuid.UUID `json:"channelId"`
}

// ChannelTemplateRequest sets a channel's emoji switch and template:
// UseOrgDefault, or its own Subject/Body.
type ChannelTemplateRequest struct {
	UseOrgDefault bool   `json:"useOrgDefault"`
	Subject       string `json:"subject"`
	Body          string `json:"body"`
	UseEmoji      bool   `json:"useEmoji"`
}

// MessageTemplates lists every kind's organisation default (admins).
func (s *SettingsService) MessageTemplates(user *users_models.User) (*MessageTemplatesView, error) {
	if !user.CanUpdateSettings() {
		return nil, ErrForbidden
	}
	source := templateSource()
	view := &MessageTemplatesView{
		Managed:   config.GetEnv().SiteExchangeEnabled() && !config.GetEnv().ServiceMode.IsControlPlane(),
		Templates: make([]MessageTemplateView, 0, len(ChannelKinds)),
	}
	for _, kind := range ChannelKinds {
		builtIn := DefaultTemplate(kind)
		item := MessageTemplateView{
			Kind: kind, Subject: builtIn.Subject, Body: builtIn.Body, IsDefault: true,
			DefaultSubject: builtIn.Subject, DefaultBody: builtIn.Body,
		}
		stored, err := storedTemplate(source, kind)
		if err != nil {
			return nil, err
		}
		if stored != nil {
			updatedAt := stored.UpdatedAt
			item.Subject, item.Body, item.IsDefault, item.UpdatedAt = stored.Subject, stored.Body, false, &updatedAt
			item.LastError, item.LastErrorAt = stored.LastError, stored.LastErrorAt
		}
		view.Templates = append(view.Templates, item)
	}
	return view, nil
}

// SaveMessageTemplate validates and stores kind's organisation default.
func (s *SettingsService) SaveMessageTemplate(
	user *users_models.User, kind string, tpl MessageTemplate,
) (*MessageTemplatesView, error) {
	if err := s.canWrite(user); err != nil {
		return nil, err
	}
	if !slices.Contains(ChannelKinds, kind) {
		return nil, fmt.Errorf("unknown channel kind %q", kind)
	}
	if kind != ChannelEmail {
		tpl.Subject = ""
	}
	if errs := ValidateTemplate(kind, tpl); len(errs) > 0 {
		return nil, errs
	}
	row := sharedconfig.MessageTemplate{
		Source: templateSource(), Kind: kind, Subject: tpl.Subject, Body: tpl.Body, UpdatedBy: &user.ID,
		UpdatedAt: time.Now().UTC(),
	}
	if err := storage.GetDb().Save(&row).Error; err != nil {
		return nil, err
	}
	ForgetTemplates()
	s.audit.WriteAuditLog("Message template updated: "+kind, &user.ID, nil)
	return s.MessageTemplates(user)
}

// ResetMessageTemplate goes back to kind's built-in template.
func (s *SettingsService) ResetMessageTemplate(user *users_models.User, kind string) (*MessageTemplatesView, error) {
	if err := s.canWrite(user); err != nil {
		return nil, err
	}
	if !slices.Contains(ChannelKinds, kind) {
		return nil, fmt.Errorf("unknown channel kind %q", kind)
	}
	if err := storage.GetDb().Where("source = ? AND kind = ?", templateSource(), kind).
		Delete(&sharedconfig.MessageTemplate{}).Error; err != nil {
		return nil, err
	}
	ForgetTemplates()
	s.audit.WriteAuditLog("Message template reset to default: "+kind, &user.ID, nil)
	return s.MessageTemplates(user)
}

// PreviewMessageTemplate renders a template against the sample firing,
// resolved, test and incident notifications (admins).
func (s *SettingsService) PreviewMessageTemplate(
	user *users_models.User, request TemplatePreviewRequest,
) (*TemplatePreviewResponse, error) {
	if !user.CanUpdateSettings() {
		return nil, ErrForbidden
	}
	if !slices.Contains(ChannelKinds, request.Kind) {
		return nil, fmt.Errorf("unknown channel kind %q", request.Kind)
	}
	tpl := MessageTemplate{Subject: request.Subject, Body: request.Body}
	response := &TemplatePreviewResponse{Errors: TemplateErrors{}, Samples: []TemplatePreviewSample{}}
	if errs := parseErrors(request.Kind, tpl); len(errs) > 0 {
		response.Errors = errs
		return response, nil
	}
	useEmoji := request.UseEmoji == nil || *request.UseEmoji
	for _, sample := range sampleNotifications(time.Now().UTC()) {
		n := sample.Notification
		item := TemplatePreviewSample{
			Key: sample.Key, Label: sample.Label, Status: n.Status, Severity: normalizeSeverity(n.Severity),
		}
		if useEmoji {
			item.Emoji = emojiFor(n.Status, n.Severity)
		}
		out, err := renderWith(request.Kind, tpl, n, sample.context(useEmoji))
		if err != nil {
			for _, problem := range asTemplateErrors(err) {
				problem.Message += " (" + sample.Label + ")"
				response.Errors = append(response.Errors, problem)
				if item.Error == "" {
					item.Error = problem.Error()
				}
			}
		} else {
			item.Subject, item.Text = out.Subject, out.Text
			if request.Kind == ChannelSlack {
				item.Color = out.Color
			}
			if len(out.JSON) > 0 {
				var indented bytes.Buffer
				if json.Indent(&indented, out.JSON, "", "  ") == nil {
					item.Body = indented.String()
				} else {
					item.Body = string(out.JSON)
				}
			}
		}
		response.Samples = append(response.Samples, item)
	}
	return response, nil
}

// TestMessageTemplate sends a test notification rendered with the given
// template through a channel of that kind (admins).
func (s *SettingsService) TestMessageTemplate(
	ctx context.Context, user *users_models.User, request TemplateTestRequest,
) error {
	if !user.CanUpdateSettings() {
		return ErrForbidden
	}
	channel, err := s.storedChannel(request.ChannelID)
	if err != nil {
		return err
	}
	if channel.Kind != request.Kind {
		return fmt.Errorf("choose a %s channel to test a %s template", request.Kind, request.Kind)
	}
	tpl := MessageTemplate{Subject: request.Subject, Body: request.Body}
	if errs := ValidateTemplate(request.Kind, tpl); len(errs) > 0 {
		return errs
	}
	return s.sendTest(ctx, channel, tpl, request.UseEmoji == nil || *request.UseEmoji)
}

// SaveChannelTemplate sets a channel's "Use emoji" switch and whether it
// uses the organisation default or its own template (admins).
func (s *SettingsService) SaveChannelTemplate(
	user *users_models.User, id uuid.UUID, request ChannelTemplateRequest,
) (*SettingsChannel, error) {
	if err := s.canWrite(user); err != nil {
		return nil, err
	}
	channel, err := s.storedChannel(id)
	if err != nil {
		return nil, err
	}
	if request.UseOrgDefault {
		request.Subject, request.Body = "", ""
	} else {
		if channel.Kind != ChannelEmail {
			request.Subject = ""
		}
		if errs := ValidateTemplate(
			channel.Kind,
			MessageTemplate{Subject: request.Subject, Body: request.Body},
		); len(
			errs,
		) > 0 {
			return nil, errs
		}
	}
	updates := map[string]any{
		"use_emoji": request.UseEmoji, "template_override": !request.UseOrgDefault,
		"template_subject": request.Subject, "template_body": request.Body, "updated_at": time.Now().UTC(),
	}
	if config.GetEnv().ServiceMode.IsControlPlane() {
		err = storage.GetDb().Model(&sharedconfig.SharedChannel{}).Where("id = ?", id).Updates(updates).Error
	} else {
		updates["template_error"], updates["template_error_at"] = "", nil
		err = storage.GetDb().Model(&NotificationChannel{}).Where("id = ?", id).Updates(updates).Error
	}
	if err != nil {
		return nil, err
	}
	what := "uses the organisation default template"
	if !request.UseOrgDefault {
		what = "uses its own template"
	}
	s.audit.WriteAuditLog(fmt.Sprintf("Notification channel %s %s (emoji %t)", channel.Name, what, request.UseEmoji),
		&user.ID, nil)
	views, err := s.List(user)
	if err != nil {
		return nil, err
	}
	for i := range views {
		if views[i].ID == id {
			return &views[i], nil
		}
	}
	return nil, ErrNotFound
}

// templateErrorsOf is err's template problems, if it is a template error.
func templateErrorsOf(err error) (TemplateErrors, bool) {
	var many TemplateErrors
	if errors.As(err, &many) {
		return many, true
	}
	var one TemplateError
	if errors.As(err, &one) {
		return TemplateErrors{one}, true
	}
	return nil, false
}
