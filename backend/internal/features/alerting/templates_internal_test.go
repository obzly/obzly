package alerting

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obzly/internal/features/sharedconfig"
	"obzly/internal/storage"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func Test_EmojiFor_EachSeverityAndStatus_IsTheAgreedTable(t *testing.T) {
	cases := []struct{ status, severity, want string }{
		{StatusFiring, SeverityCritical, "📣❗🔥"},
		{StatusFiring, SeverityHigh, "📣❗🚨"},
		{StatusFiring, SeverityMedium, "📣 🔔⚠️"},
		{StatusFiring, SeverityLow, "🔔🔔👀"},
		{StatusResolved, SeverityCritical, "✅️"},
		{StatusResolved, SeverityLow, "✅️"},
		{StatusTest, SeverityHigh, "🧪"},
		{StatusFiring, "", "📣 🔔⚠️"},
	}
	for _, c := range cases {
		require.Equal(t, c.want, emojiFor(c.status, c.severity), "%s %s", c.status, c.severity)
	}
	require.Equal(t, "✅️", EmojiResolved)
}

func Test_PagerDutySeverity_MapsEachSeverity(t *testing.T) {
	require.Equal(t, "critical", pagerDutySeverity(SeverityCritical))
	require.Equal(t, "error", pagerDutySeverity(SeverityHigh))
	require.Equal(t, "warning", pagerDutySeverity(SeverityMedium))
	require.Equal(t, "info", pagerDutySeverity(SeverityLow))
	require.Equal(t, "warning", pagerDutySeverity(""), "payloads queued before severities are medium")

	body := pagerDutyBody("rk", Notification{Status: StatusFiring, Severity: SeverityHigh, Fingerprint: "fp"},
		rendered{Text: "summary"})
	payload, _ := body["payload"].(map[string]any)
	require.Equal(t, "error", payload["severity"])
	require.Equal(t, "summary", payload["summary"])
}

func Test_SeverityFromLabel_MapsLikeTheMigration(t *testing.T) {
	cases := map[string]string{
		"critical": SeverityCritical, "CRITICAL": SeverityCritical, "high": SeverityHigh, "error": SeverityHigh,
		"warning": SeverityMedium, "medium": SeverityMedium, "info": SeverityLow, "low": SeverityLow,
	}
	for label, want := range cases {
		got, ok := SeverityFromLabel(label)
		require.True(t, ok, label)
		require.Equal(t, want, got, label)
	}
	_, ok := SeverityFromLabel("page")
	require.False(t, ok)
	require.Equal(t, SeverityMedium, ruleSeverity("", Labels{}))
	require.Equal(t, SeverityMedium, ruleSeverity("", Labels{"severity": "page"}))
	require.Equal(t, SeverityLow, ruleSeverity("LOW", Labels{"severity": "critical"}), "an explicit severity wins")
	sev, ok := managedSeriesSeverity("informational")
	require.True(t, ok)
	require.Equal(t, SeverityLow, sev)
}

// The migration maps existing rules' severity labels; run its UPDATE on
// fresh rules (rolled back).
func Test_SeverityMigration_MapsExistingRulesFromTheirLabel(t *testing.T) {
	migration, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations",
		"20261119160000_add_notification_templates.sql"))
	require.NoError(t, err)
	statements := strings.Split(string(migration), "-- +goose StatementBegin")
	var mapping, managed string
	for _, statement := range statements {
		statement, _, _ = strings.Cut(statement, "-- +goose StatementEnd")
		switch {
		case strings.Contains(statement, "UPDATE alert_rules") && strings.Contains(statement, "labels ->> 'severity'") &&
			strings.Contains(statement, "ELSE 'medium'"):
			mapping = statement
		case strings.Contains(statement, "UPDATE alert_rules") && strings.Contains(statement, "managed_by LIKE"):
			managed = statement
		}
	}
	require.NotEmpty(t, mapping)
	require.NotEmpty(t, managed)

	projectID := uuid.New()
	labels := map[string]string{
		"critical": "critical", "high": "high", "error": "high", "warning": "medium", "Medium": "medium",
		"info": "low", "low": "low", "page": "medium", "": "medium",
	}
	err = storage.GetDb().Transaction(func(tx *gorm.DB) error {
		require.NoError(t, tx.Exec("INSERT INTO projects (id, name) VALUES (?, ?)", projectID, "migration").Error)
		ids := map[uuid.UUID]string{}
		for label, want := range labels {
			id := uuid.New()
			ruleLabels := `{}`
			if label != "" {
				ruleLabels = `{"severity": "` + label + `"}`
			}
			require.NoError(t, tx.Exec(`INSERT INTO alert_rules (id, project_id, name, source, query, comparison, threshold,
				labels, severity) VALUES (?, ?, ?, 'metrics', '{}', '>', 0, ?::jsonb, 'low')`,
				id, projectID, "r "+label, ruleLabels).Error)
			ids[id] = want
		}
		budget := uuid.New()
		require.NoError(t, tx.Exec(`INSERT INTO alert_rules (id, project_id, name, source, query, comparison, threshold,
			labels, managed_by) VALUES (?, ?, 'budget', 'ai_cost', '{}', '>=', 80, '{}', ?)`,
			budget, projectID, "ai-budget:"+uuid.NewString()).Error)
		ids[budget] = SeverityHigh
		require.NoError(t, tx.Exec(mapping).Error)
		require.NoError(t, tx.Exec(managed).Error)
		for id, want := range ids {
			var got string
			require.NoError(t, tx.Raw("SELECT severity FROM alert_rules WHERE id = ?", id).Scan(&got).Error)
			require.Equal(t, want, got)
		}
		return gorm.ErrInvalidTransaction // roll back
	})
	require.ErrorIs(t, err, gorm.ErrInvalidTransaction)
}

func Test_DefaultTemplates_RenderEverySampleForEachKind(t *testing.T) {
	for _, kind := range ChannelKinds {
		require.Empty(t, ValidateTemplate(kind, DefaultTemplate(kind)), kind)
		for _, sample := range sampleNotifications(time.Now().UTC()) {
			out, err := renderWith(kind, DefaultTemplate(kind), sample.Notification, sample.context(true))
			require.NoError(t, err, "%s %s", kind, sample.Key)
			if kind == ChannelWebhook {
				require.True(t, json.Valid(out.JSON))
				continue
			}
			title := out.Text
			if kind == ChannelEmail {
				title = out.Subject
			}
			require.True(t, strings.HasPrefix(strings.TrimPrefix(title, "*"),
				emojiFor(sample.Notification.Status, sample.Notification.Severity)),
				"the title starts with the emoji: %s %s %q", kind, sample.Key, title)
		}
	}
	firing := sampleNotifications(time.Now().UTC())[0]
	out, err := renderWith(ChannelEmail, DefaultTemplate(ChannelEmail), firing.Notification, firing.context(true))
	require.NoError(t, err)
	require.Equal(t, "📣❗🔥 [FIRING] Critical: High error rate on checkout", out.Subject)
	require.Contains(t, out.Text, "Runbook:\nCheck the checkout dashboard")
	require.Contains(t, out.Text, "Open in Obzly: https://obzly.example.com/?")
	test := sampleNotifications(time.Now().UTC())[5]
	out, err = renderWith(ChannelSlack, DefaultTemplate(ChannelSlack), test.Notification, test.context(true))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(out.Text, "*🧪 [TEST]"))
	require.Equal(t, colorTest, out.Color)
}

func Test_Render_UseEmojiOff_EmojiIsEmpty(t *testing.T) {
	sample := sampleNotifications(time.Now().UTC())[0]
	out, err := renderWith(ChannelEmail, DefaultTemplate(ChannelEmail), sample.Notification, sample.context(false))
	require.NoError(t, err)
	require.Equal(t, "[FIRING] Critical: High error rate on checkout", out.Subject)
	out, err = renderWith(ChannelSlack, MessageTemplate{Body: "[{{.Emoji}}]"}, sample.Notification, sample.context(false))
	require.NoError(t, err)
	require.Equal(t, "[]", out.Text)
}

func Test_Render_SlackColours_FollowSeverityAndStatus(t *testing.T) {
	colours := map[string]string{}
	for _, sample := range sampleNotifications(time.Now().UTC()) {
		out, err := renderWith(ChannelSlack, DefaultTemplate(ChannelSlack), sample.Notification, sample.context(true))
		require.NoError(t, err)
		colours[sample.Key] = out.Color
	}
	require.Equal(t, colorCritical, colours["firing-critical"])
	require.Equal(t, colorHigh, colours["firing-high"])
	require.Equal(t, colorMedium, colours["firing-medium"])
	require.Equal(t, colorLow, colours["firing-low"])
	require.Equal(t, colorResolved, colours["resolved"])
	require.Equal(t, colorResolved, colours["incident-end"])
}

// Telemetry and rule text can't inject Slack mentions or links, break a
// custom webhook's JSON, or add an email header.
func Test_Render_EscapesEveryVariablePerChannel(t *testing.T) {
	hostile := `<!channel> <https://evil.example.com|reset> "},"admin":true,"x":"` + "\r\nBcc: x@example.com"
	n := Notification{
		Status: StatusFiring, Severity: SeverityHigh, ProjectID: uuid.New(), RuleName: "Errors " + hostile,
		Summary: hostile, RuleDescription: hostile, Labels: Labels{"pattern": hostile, hostile: "key"},
		StartsAt: time.Now().UTC(),
	}
	rc := renderContext{UseEmoji: true, ProjectName: hostile, Now: time.Now().UTC()}
	all := `{{.Rule.Name}} {{.Summary}} {{.Rule.Description}} {{.Project.Name}} ` +
		`{{range .SortedLabels}}{{.Name}}={{.Value}} {{end}}{{label .Labels "pattern"}}`

	slack, err := renderWith(ChannelSlack, MessageTemplate{Body: all}, n, rc)
	require.NoError(t, err)
	require.NotContains(t, slack.Text, "<")
	require.NotContains(t, slack.Text, ">")
	require.Contains(t, slack.Text, "&lt;!channel&gt;")

	webhook, err := renderWith(ChannelWebhook, MessageTemplate{Body: `{"text": "` + all + `", "sev": "{{.SeverityKey}}"}`},
		n, rc)
	require.NoError(t, err)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(webhook.JSON, &parsed))
	require.Len(t, parsed, 2, "a value can't add keys")
	require.Contains(t, parsed["text"], `"},"admin":true`)
	require.Equal(t, "high", parsed["sev"])

	email, err := renderWith(ChannelEmail, MessageTemplate{Subject: "{{.Rule.Name}}", Body: all}, n, rc)
	require.NoError(t, err)
	require.NotContains(t, email.Subject, "\n")
	require.NotContains(t, email.Subject, "\r")
	require.NotContains(t, email.Text, "\r")

	pagerDuty, err := renderWith(ChannelPagerDuty, MessageTemplate{Body: strings.Repeat("{{.Summary}}", 40)}, n, rc)
	require.NoError(t, err)
	require.LessOrEqual(t, len([]rune(pagerDuty.Text)), pagerDutySummaryLimit)
	require.NotContains(t, pagerDuty.Text, "\n")
}

func Test_ValidateTemplate_ReportsProblemsWithLines(t *testing.T) {
	errs := ValidateTemplate(ChannelSlack, MessageTemplate{Body: "line one\n{{.Rule.Name"})
	require.Len(t, errs, 1)
	require.Equal(t, FieldBody, errs[0].Field)
	require.Equal(t, 2, errs[0].Line)

	errs = ValidateTemplate(ChannelSlack, MessageTemplate{Body: "ok\n\n{{.Nope}}"})
	require.NotEmpty(t, errs)
	require.Equal(t, 3, errs[0].Line)
	require.Contains(t, errs[0].Message, "Nope")

	errs = ValidateTemplate(ChannelSlack, MessageTemplate{Body: "{{.Incident.Services}}"})
	require.NotEmpty(t, errs, "incident variables need {{if .Incident}} for alerts")

	errs = ValidateTemplate(ChannelEmail, MessageTemplate{Subject: "", Body: "x"})
	require.Equal(t, FieldSubject, errs[0].Field)

	errs = ValidateTemplate(ChannelWebhook, MessageTemplate{Body: "{\n\"v\": {{.Summary}}\n}"})
	require.NotEmpty(t, errs)
	require.Contains(t, errs[0].Message, "invalid JSON")
	require.Equal(t, 2, errs[0].Line)
	require.Empty(t, ValidateTemplate(ChannelWebhook, MessageTemplate{}), "empty: the built-in JSON")

	errs = ValidateTemplate(ChannelSlack, MessageTemplate{Body: strings.Repeat("x", maxTemplateBytes+1)})
	require.Contains(t, errs[0].Message, "longer than")

	errs = ValidateTemplate(ChannelSlack, MessageTemplate{Body: `{{define "a"}}{{template "a"}}{{end}}{{template "a"}}`})
	require.NotEmpty(t, errs)
	errs = ValidateTemplate(ChannelSlack, MessageTemplate{Body: `{{template "a"}}`})
	require.Contains(t, errs[0].Message, "template calls")
	errs = ValidateTemplate(ChannelSlack, MessageTemplate{Body: `{{range 1000000000}}x{{end}}`})
	require.Contains(t, errs[0].Message, "range over a number")
	errs = ValidateTemplate(ChannelSlack, MessageTemplate{Body: `{{$n := 1000000000}}{{range $n}}x{{end}}`})
	require.Contains(t, errs[0].Message, "number literals")

	errs = ValidateTemplate(ChannelSlack, MessageTemplate{Body: `{{range .SortedLabels}}{{printf "%999999s" "x"}}{{end}}`})
	require.NotEmpty(t, errs)
	require.Contains(t, errs[0].Message, "larger than")
}

func Test_Execute_SlowTemplate_TimesOut(t *testing.T) {
	n := sampleNotifications(time.Now().UTC())[6].Notification
	n.Incident.SignalCount = 20_000_000
	start := time.Now()
	_, err := executeWithin(5*time.Millisecond, FieldBody, `{{range .Incident.SignalCount}}{{end}}`,
		buildData(n, renderContext{}, escapePlain))
	require.Error(t, err)
	require.Contains(t, err.Error(), "too long")
	require.Less(t, time.Since(start), time.Second)
}

// A template that fails at send time doesn't drop the notification: the
// built-in template is sent and the failure returned.
func Test_Render_FailingTemplate_FallsBackToTheBuiltIn(t *testing.T) {
	sample := sampleNotifications(time.Now().UTC())[1]
	out, err := render(ChannelSlack, MessageTemplate{Body: "{{.Nope}}"}, sample.Notification, sample.context(true))
	require.Error(t, err)
	builtIn, builtInErr := renderWith(ChannelSlack, DefaultTemplate(ChannelSlack), sample.Notification,
		sample.context(true))
	require.NoError(t, builtInErr)
	require.Equal(t, builtIn.Text, out.Text)

	out, err = render(ChannelWebhook, MessageTemplate{Body: `{"a": {{.Summary}}}`}, sample.Notification,
		sample.context(true))
	require.Error(t, err)
	require.True(t, json.Valid(out.JSON))
}

func Test_BuildLink_OpensTheAlertOrIncidentOnItsSite(t *testing.T) {
	projectID, ruleID, sessionID := uuid.New(), uuid.New(), uuid.New()
	alert := buildLink("https://cp.example.com", "eu-1", Notification{ProjectID: projectID, RuleID: &ruleID})
	require.Equal(t, "https://cp.example.com/?project="+projectID.String()+"&rule="+ruleID.String()+
		"&site=eu-1&tab=alerts", alert)
	incident := buildLink("https://obzly.example.com", "", Notification{ProjectID: projectID,
		Incident: &IncidentDetails{ID: sessionID}})
	require.Equal(t, "https://obzly.example.com/?project="+projectID.String()+"&session="+sessionID.String()+
		"&tab=incidents", incident)
	require.Empty(t, buildLink("", "", Notification{ProjectID: projectID}))
}

// Precedence: the channel's override, else the organisation default for
// its kind, else the built-in template.
func Test_EffectiveTemplate_OverrideBeatsOrganisationBeatsBuiltIn(t *testing.T) {
	cleanTemplates(t)
	tpl, origin, err := effectiveTemplate(ChannelSlack, false, "", "")
	require.NoError(t, err)
	require.Equal(t, originBuiltIn, origin)
	require.Equal(t, DefaultTemplate(ChannelSlack), tpl)

	require.NoError(t, storage.GetDb().Create(&sharedconfig.MessageTemplate{
		Source: sharedconfig.SourceLocal, Kind: ChannelSlack, Body: "org {{.Rule.Name}}", UpdatedAt: time.Now().UTC(),
	}).Error)
	ForgetTemplates()
	tpl, origin, err = effectiveTemplate(ChannelSlack, false, "", "")
	require.NoError(t, err)
	require.Equal(t, originOrg, origin)
	require.Equal(t, "org {{.Rule.Name}}", tpl.Body)

	tpl, origin, err = effectiveTemplate(ChannelSlack, true, "", "own {{.Rule.Name}}")
	require.NoError(t, err)
	require.Equal(t, originOverride, origin)
	require.Equal(t, "own {{.Rule.Name}}", tpl.Body)

	tpl, _, err = effectiveTemplate(ChannelEmail, false, "", "")
	require.NoError(t, err)
	require.Equal(t, DefaultTemplate(ChannelEmail), tpl, "other kinds keep their built-in")
}

// At send time a broken override (stored before validation, or failing on
// real data) sends the built-in message and marks the channel; the next
// good send clears the mark.
func Test_Dispatch_OverrideFailsToRender_SendsTheBuiltInAndMarksTheChannel(t *testing.T) {
	cleanTemplates(t)
	rec := newReceiver(t)
	f := newFixture(t, rec.server.URL, loopbackSender(t))
	require.NoError(t, storage.GetDb().Model(&NotificationChannel{}).Where("id = ?", f.channel.ID).
		Updates(map[string]any{"template_override": true, "template_body": `{"rule": "{{.Nope}}"}`}).Error)
	rule := f.rule(t, "vector(1)", 0, 0)
	f.advanceWith(t, rule, sample{labels: Labels{"service": "web"}, value: 1})
	_, err := f.dispatcher.dispatchOnce(context.Background())
	require.NoError(t, err)

	payloads := rec.payloads()
	require.Len(t, payloads, 1)
	require.Equal(t, rule.Name, payloads[0]["ruleName"], "the built-in JSON was sent")
	require.Equal(t, SeverityMedium, payloads[0]["severity"])
	channel, err := f.repo.channelByID(f.channel.ID)
	require.NoError(t, err)
	require.Contains(t, channel.TemplateError, "Nope")
	require.NotNil(t, channel.TemplateErrorAt)

	require.NoError(t, storage.GetDb().Model(&NotificationChannel{}).Where("id = ?", f.channel.ID).
		Update("template_body", `{"rule": "{{.Rule.Name}}", "emoji": "{{.Emoji}}"}`).Error)
	f.clock = f.clock.Add(time.Minute)
	f.advanceWith(t, rule, sample{labels: Labels{"service": "web"}, value: -1})
	_, err = f.dispatcher.dispatchOnce(context.Background())
	require.NoError(t, err)
	payloads = rec.payloads()
	require.Len(t, payloads, 2)
	require.Equal(t, map[string]any{"rule": rule.Name, "emoji": ""}, payloads[1],
		"a channel saved without use_emoji sends none")
	channel, err = f.repo.channelByID(f.channel.ID)
	require.NoError(t, err)
	require.Empty(t, channel.TemplateError)
}

func cleanTemplates(t *testing.T) {
	t.Helper()
	require.NoError(t, storage.GetDb().Exec("DELETE FROM notification_templates WHERE source = 'local'").Error)
	ForgetTemplates()
	t.Cleanup(func() {
		storage.GetDb().Exec("DELETE FROM notification_templates WHERE source = 'local'")
		ForgetTemplates()
	})
}
