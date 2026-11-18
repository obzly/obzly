package alerting_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"obzly/internal/features/alerting"
	projects_controllers "obzly/internal/features/projects/controllers"
	projects_testing "obzly/internal/features/projects/testing"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	"obzly/internal/storage"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Message templates are admin-only: list, save (validated, audit-logged),
// preview, test and reset; a channel's override and emoji switch too.
func Test_MessageTemplatesAPI_AdminOnlyValidatedAndAudited(t *testing.T) {
	router := projects_testing.CreateTestRouter(alerting.GetSettingsController())
	member := users_testing.CreateTestUser(users_enums.UserRoleMember)
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	require.NoError(t, storage.GetDb().Exec("DELETE FROM notification_templates WHERE source = 'local'").Error)
	alerting.ForgetTemplates()
	t.Cleanup(func() {
		storage.GetDb().Exec("DELETE FROM notification_templates WHERE source = 'local'")
		alerting.ForgetTemplates()
	})
	slackTemplate := alerting.MessageTemplate{Body: "{{.Emoji}} *{{.Rule.Name}}* {{.Summary}}"}
	preview := alerting.TemplatePreviewRequest{Kind: "slack", Body: slackTemplate.Body}

	member403 := "Bearer " + member.Token
	test_utils.MakeGetRequest(t, router, "/api/v1/settings/message-templates", member403, http.StatusForbidden)
	test_utils.MakePutRequest(t, router, "/api/v1/settings/message-templates/slack", member403, slackTemplate,
		http.StatusForbidden)
	test_utils.MakeDeleteRequest(t, router, "/api/v1/settings/message-templates/slack", member403,
		http.StatusForbidden)
	test_utils.MakePostRequest(t, router, "/api/v1/settings/message-templates/preview", member403, preview,
		http.StatusForbidden)
	test_utils.MakePostRequest(t, router, "/api/v1/settings/message-templates/test", member403,
		alerting.TemplateTestRequest{TemplatePreviewRequest: preview, ChannelID: uuid.New()}, http.StatusForbidden)
	test_utils.MakePutRequest(t, router, "/api/v1/settings/channels/"+uuid.NewString()+"/template", member403,
		alerting.ChannelTemplateRequest{UseOrgDefault: true, UseEmoji: true}, http.StatusForbidden)

	adminAuth := "Bearer " + admin.Token
	var view alerting.MessageTemplatesView
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/settings/message-templates", adminAuth,
		http.StatusOK, &view)
	require.Len(t, view.Templates, 4)
	assert.False(t, view.Managed)
	for _, item := range view.Templates {
		assert.True(t, item.IsDefault, item.Kind)
	}

	// Invalid: 400 with the problems and their lines; nothing stored.
	bad := test_utils.MakePutRequest(t, router, "/api/v1/settings/message-templates/slack", adminAuth,
		alerting.MessageTemplate{Body: "ok\n{{.Rule.Name"}, http.StatusBadRequest)
	var problems struct {
		Errors []alerting.TemplateError `json:"errors"`
	}
	require.NoError(t, json.Unmarshal(bad.Body, &problems))
	require.Len(t, problems.Errors, 1)
	assert.Equal(t, 2, problems.Errors[0].Line)
	test_utils.MakePutRequest(t, router, "/api/v1/settings/message-templates/webhook", adminAuth,
		alerting.MessageTemplate{Body: `{"text": {{.Summary}}}`}, http.StatusBadRequest)
	test_utils.MakePutRequest(t, router, "/api/v1/settings/message-templates/sms", adminAuth, slackTemplate,
		http.StatusBadRequest)

	test_utils.MakePutRequestAndUnmarshal(t, router, "/api/v1/settings/message-templates/slack", adminAuth,
		slackTemplate, http.StatusOK, &view)
	assert.False(t, templateOf(view, "slack").IsDefault)
	assert.Equal(t, slackTemplate.Body, templateOf(view, "slack").Body)
	assertAudited(t, admin.UserID, "Message template updated: slack")

	// Preview: every sample, with its emoji, server-rendered.
	var rendered alerting.TemplatePreviewResponse
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/settings/message-templates/preview", adminAuth,
		preview, http.StatusOK, &rendered)
	require.Empty(t, rendered.Errors)
	require.Len(t, rendered.Samples, 8)
	assert.Equal(t, "📣❗🔥", rendered.Samples[0].Emoji)
	assert.Equal(t, "📣❗🔥 *High error rate on checkout* 5xx responses above 5% for 5 minutes",
		rendered.Samples[0].Text)
	assert.Equal(t, "#DC2626", rendered.Samples[0].Color)
	noEmoji := false
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/settings/message-templates/preview", adminAuth,
		alerting.TemplatePreviewRequest{Kind: "slack", Body: preview.Body, UseEmoji: &noEmoji}, http.StatusOK,
		&rendered)
	assert.Empty(t, rendered.Samples[0].Emoji)
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/settings/message-templates/preview", adminAuth,
		alerting.TemplatePreviewRequest{Kind: "slack", Body: "a\nb\n{{if}}"}, http.StatusOK, &rendered)
	require.NotEmpty(t, rendered.Errors)
	assert.Equal(t, 3, rendered.Errors[0].Line)

	// Send test to a channel of another kind: refused.
	channelID := createSettingsChannel(t, router, adminAuth, "email", &alerting.ChannelConfig{To: []string{"a@example.com"}})
	test_utils.MakePostRequest(t, router, "/api/v1/settings/message-templates/test", adminAuth,
		alerting.TemplateTestRequest{TemplatePreviewRequest: preview, ChannelID: channelID}, http.StatusBadRequest)

	// A channel's own template and emoji switch.
	var channel alerting.SettingsChannel
	test_utils.MakePutRequest(t, router, "/api/v1/settings/channels/"+channelID.String()+"/template", adminAuth,
		alerting.ChannelTemplateRequest{Subject: "", Body: "x"}, http.StatusBadRequest)
	test_utils.MakePutRequestAndUnmarshal(t, router, "/api/v1/settings/channels/"+channelID.String()+"/template",
		adminAuth, alerting.ChannelTemplateRequest{Subject: "{{.Rule.Name}}", Body: "{{.Summary}}", UseEmoji: false},
		http.StatusOK, &channel)
	assert.True(t, channel.TemplateOverride)
	assert.False(t, channel.UseEmoji)
	assert.Equal(t, "{{.Rule.Name}}", channel.TemplateSubject)
	// Editing the channel keeps its template settings.
	test_utils.MakePutRequestAndUnmarshal(t, router, "/api/v1/settings/channels/"+channelID.String(), adminAuth,
		alerting.SettingsChannelRequest{Name: channel.Name + " renamed", Kind: "email", AllProjects: true},
		http.StatusOK, &channel)
	assert.True(t, channel.TemplateOverride)
	assert.False(t, channel.UseEmoji)
	test_utils.MakePutRequestAndUnmarshal(t, router, "/api/v1/settings/channels/"+channelID.String()+"/template",
		adminAuth, alerting.ChannelTemplateRequest{UseOrgDefault: true, UseEmoji: true}, http.StatusOK, &channel)
	assert.False(t, channel.TemplateOverride)
	assert.Empty(t, channel.TemplateBody)
	assertAudited(t, admin.UserID, "uses the organisation default template")

	test_utils.MakeDeleteRequest(t, router, "/api/v1/settings/message-templates/slack", adminAuth, http.StatusOK)
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/settings/message-templates", adminAuth,
		http.StatusOK, &view)
	assert.True(t, templateOf(view, "slack").IsDefault)
	assertAudited(t, admin.UserID, "Message template reset to default: slack")
}

// Incident notification settings: any member reads, owners and project
// admins change them, with channels available to the project.
func Test_IncidentNotificationsAPI_OwnersAndAdminsChangeThem(t *testing.T) {
	router := projects_testing.CreateTestRouter(projects_controllers.GetProjectController(),
		projects_controllers.GetMembershipController(), alerting.GetController(), alerting.GetSettingsController())
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	projectAdmin := users_testing.CreateTestUser(users_enums.UserRoleMember)
	member := users_testing.CreateTestUser(users_enums.UserRoleMember)
	viewer := users_testing.CreateTestUser(users_enums.UserRoleMember)
	globalAdmin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	project, _ := projects_testing.CreateTestProjectWithToken("Incident notifications", owner.Token, router)
	projects_testing.AddMemberToProject(project, projectAdmin, users_enums.ProjectRoleAdmin, owner.Token, router)
	projects_testing.AddMemberToProject(project, member, users_enums.ProjectRoleMember, owner.Token, router)
	projects_testing.AddMemberToProject(project, viewer, users_enums.ProjectRoleViewer, owner.Token, router)
	path := "/api/v1/projects/" + project.ID.String() + "/incident-notifications"
	channelID := createSettingsChannel(t, router, "Bearer "+globalAdmin.Token, "slack",
		&alerting.ChannelConfig{URL: "https://hooks.example.com/services/x"})

	var settings alerting.IncidentNotificationSettings
	test_utils.MakeGetRequestAndUnmarshal(t, router, path, "Bearer "+viewer.Token, http.StatusOK, &settings)
	assert.False(t, settings.NotifyOpen)
	assert.Equal(t, 3, settings.MinSignals)
	assert.Equal(t, 2, settings.MinServices)
	assert.Equal(t, alerting.SeverityHigh, settings.Severity)

	request := alerting.IncidentNotificationRequest{NotifyOpen: true, NotifyEnd: true, MinSignals: 5,
		MinServices: 2, Severity: "critical", ChannelIDs: []uuid.UUID{channelID}}
	test_utils.MakePutRequest(t, router, path, "Bearer "+viewer.Token, request, http.StatusForbidden)
	test_utils.MakePutRequest(t, router, path, "Bearer "+member.Token, request, http.StatusForbidden)
	test_utils.MakePutRequestAndUnmarshal(t, router, path, "Bearer "+projectAdmin.Token, request, http.StatusOK,
		&settings)
	assert.True(t, settings.NotifyEnd)
	assert.Equal(t, alerting.SeverityCritical, settings.Severity)
	test_utils.MakePutRequestAndUnmarshal(t, router, path, "Bearer "+owner.Token, request, http.StatusOK, &settings)
	assertAudited(t, owner.UserID, "Incident notifications on")

	invalid := request
	invalid.ChannelIDs = []uuid.UUID{uuid.New()}
	test_utils.MakePutRequest(t, router, path, "Bearer "+owner.Token, invalid, http.StatusBadRequest)
	invalid = request
	invalid.ChannelIDs = nil
	test_utils.MakePutRequest(t, router, path, "Bearer "+owner.Token, invalid, http.StatusBadRequest)
	invalid = request
	invalid.Severity = "urgent"
	test_utils.MakePutRequest(t, router, path, "Bearer "+owner.Token, invalid, http.StatusBadRequest)
	invalid = request
	invalid.MinSignals = 0
	test_utils.MakePutRequest(t, router, path, "Bearer "+owner.Token, invalid, http.StatusBadRequest)

	test_utils.MakeGetRequestAndUnmarshal(t, router, path, "Bearer "+member.Token, http.StatusOK, &settings)
	assert.Equal(t, 5, settings.MinSignals)
	assert.Equal(t, []uuid.UUID{channelID}, []uuid.UUID(settings.ChannelIDs))
}

// Rules carry a severity (explicit, else from their severity label, else
// medium) and a runbook description.
func Test_AlertRuleSeverity_ExplicitFromLabelOrMedium(t *testing.T) {
	router := projects_testing.CreateTestRouter(projects_controllers.GetProjectController(), alerting.GetController())
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Rule severity", owner.Token, router)
	base := "/api/v1/projects/" + project.ID.String() + "/alert-rules"
	rule := alerting.RuleRequest{Name: "Errors", Source: "metrics", Query: alerting.RuleQuery{PromQL: "vector(1)"},
		Comparison: ">", Threshold: 0, Severity: "critical", Description: "Restart the consumer."}

	var created alerting.AlertRule
	test_utils.MakePostRequestAndUnmarshal(t, router, base, "Bearer "+owner.Token, rule, http.StatusOK, &created)
	assert.Equal(t, alerting.SeverityCritical, created.Severity)
	assert.Equal(t, "Restart the consumer.", created.Description)

	rule.Severity, rule.Labels = "", alerting.Labels{"severity": "warning"}
	test_utils.MakePostRequestAndUnmarshal(t, router, base, "Bearer "+owner.Token, rule, http.StatusOK, &created)
	assert.Equal(t, alerting.SeverityMedium, created.Severity)
	rule.Labels = nil
	test_utils.MakePostRequestAndUnmarshal(t, router, base, "Bearer "+owner.Token, rule, http.StatusOK, &created)
	assert.Equal(t, alerting.SeverityMedium, created.Severity)
	rule.Severity = "urgent"
	test_utils.MakePostRequest(t, router, base, "Bearer "+owner.Token, rule, http.StatusBadRequest)
}

func templateOf(view alerting.MessageTemplatesView, kind string) alerting.MessageTemplateView {
	for _, item := range view.Templates {
		if item.Kind == kind {
			return item
		}
	}
	return alerting.MessageTemplateView{}
}

func createSettingsChannel(
	t *testing.T, router *gin.Engine, auth, kind string, config *alerting.ChannelConfig,
) uuid.UUID {
	t.Helper()
	var channel alerting.SettingsChannel
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/settings/channels", auth,
		alerting.SettingsChannelRequest{Name: kind + " " + uuid.NewString()[:8], Kind: kind, Config: config,
			AllProjects: true}, http.StatusOK, &channel)
	t.Cleanup(func() { storage.GetDb().Exec("DELETE FROM notification_channels WHERE id = ?", channel.ID) })
	return channel.ID
}

func assertAudited(t *testing.T, userID uuid.UUID, fragment string) {
	t.Helper()
	var count int64
	require.NoError(t, storage.GetDb().Raw("SELECT count(*) FROM audit_logs WHERE user_id = ? AND message LIKE ?",
		userID, "%"+fragment+"%").Scan(&count).Error)
	assert.Positive(t, count, "audit log %q", fragment)
}
