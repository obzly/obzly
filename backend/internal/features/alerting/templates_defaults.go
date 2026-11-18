package alerting

import (
	"time"

	"github.com/google/uuid"
)

// The built-in templates. Each starts its title with .Emoji, empty when the
// channel's "Use emoji" is off.
const (
	defaultTitle = `{{if .Emoji}}{{.Emoji}} {{end}}[{{upper .Status}}] {{if not .Test}}{{.Severity}}: {{end}}{{.Rule.Name}}`

	defaultEmailBody = defaultTitle + `
{{- if .Summary}}

{{.Summary}}
{{- end}}
{{if .Incident}}
Services: {{join ", " .Incident.Services}}
Signals: {{.Incident.SignalCount}} ({{.Incident.ErrorLogs}} error logs, {{.Incident.ErrorSpans}} error spans)
{{- range .Incident.TopServices}}
- {{.Name}}: {{.Total}} errors
{{- end}}
{{else if not .Test}}
Value {{.Value}} ({{.Comparison}} {{.Threshold}})
{{end -}}
{{if .StartsAt}}Started {{.StartsAt}}{{if .Duration}} ({{.Duration}}){{end}}
{{end -}}
{{if .Project.Name}}Project {{.Project.Name}}{{if .Site.Name}} on {{.Site.Name}}{{end}}
{{end -}}
{{range .SortedLabels}}{{.Name}}={{.Value}}
{{end -}}
{{if .Rule.Description}}
Runbook:
{{.Rule.Description}}
{{end -}}
{{if .Link}}
Open in Obzly: {{.Link}}
{{end}}`

	defaultSlackBody = `*` + defaultTitle + `*
{{- if .Summary}}
{{.Summary}}
{{- end}}
{{- if .Incident}}
Services: {{join ", " .Incident.Services}} · {{.Incident.SignalCount}} signals
{{- range .Incident.TopServices}}
• {{.Name}}: {{.Total}} errors
{{- end}}
{{- else if not .Test}}
Value ` + "`{{.Value}}`" + ` ({{.Comparison}} {{.Threshold}})
{{- end}}
{{- if .Duration}}
For {{.Duration}}{{if .Project.Name}} · {{.Project.Name}}{{end}}
{{- end}}
{{- if .SortedLabels}}
{{range $i, $l := .SortedLabels}}{{if $i}}, {{end}}` + "`{{$l.Name}}={{$l.Value}}`" + `{{end}}
{{- end}}
{{- if .Rule.Description}}
_Runbook:_ {{.Rule.Description}}
{{- end}}
{{- if .Link}}
<{{.Link}}|Open in Obzly>
{{- end}}`

	defaultPagerDutySummary = `{{if .Emoji}}{{.Emoji}} {{end}}[{{upper .Status}}] {{.Rule.Name}}` +
		`{{if .Summary}}: {{.Summary}}{{end}}`
)

// templateSample is a notification the preview and validation render.
type templateSample struct {
	Key          string
	Label        string
	Notification Notification
}

func (s templateSample) context(useEmoji bool) renderContext {
	return renderContext{
		ProjectName: "Contoso Shop", SiteID: "eu-1", SiteName: "eu-1", UseEmoji: useEmoji,
		Link: buildLink("https://obzly.example.com", "eu-1", s.Notification),
		Now:  s.Notification.StartsAt.Add(12 * time.Minute),
	}
}

// sampleNotifications are firing alerts of each severity, a resolved one, a
// test, and an incident opening and ending. A label carries characters a
// channel must escape, so the preview shows the escaping.
func sampleNotifications(now time.Time) []templateSample {
	projectID := uuid.MustParse("6f1c2a52-8c3e-4f8e-9a51-0d7c1e5b2a10")
	ruleID := uuid.MustParse("3b9d7f0e-2a4c-4b6e-8f1a-5c2d9e7b4a31")
	startsAt := now.Add(-12 * time.Minute).Truncate(time.Second)
	alert := func(status, severity string) Notification {
		n := Notification{
			Kind: KindAlert, Status: status, Severity: severity, ProjectID: projectID, RuleID: &ruleID,
			RuleName: "High error rate on checkout", Source: SourceMetrics,
			Summary:         "5xx responses above 5% for 5 minutes",
			RuleDescription: "Check the checkout dashboard; roll back the last deploy if it started then.",
			Comparison:      ">", Threshold: 0.05, Measured: 0.0734, StartsAt: startsAt,
			Labels: Labels{"alertname": "High error rate on checkout", "service_name": "checkout",
				"env": "production", "team": `payments <oncall> & "night"`},
			Fingerprint: "a1b2c3d4e5f60718",
		}
		if status == StatusResolved {
			endsAt := startsAt.Add(9 * time.Minute)
			n.EndsAt = &endsAt
		}
		return n
	}
	test := Notification{
		Kind: KindAlert, Status: StatusTest, Severity: SeverityMedium, RuleName: "Test notification from Obzly",
		Summary: "If you can read this, the channel ops-alerts works.", Labels: Labels{"channel": "ops-alerts"},
		StartsAt: startsAt,
	}
	incident := func(status string) Notification {
		sessionID := uuid.MustParse("9e4a1c7b-5d2f-4a8e-b6c3-1f0d2e3a4b5c")
		n := Notification{
			Kind: KindIncident, Status: status, Severity: SeverityHigh, ProjectID: projectID,
			RuleName: "Incident: checkout, payments", Source: KindIncident,
			Summary:  "14 error signals across 2 services",
			Labels:   Labels{"alertname": incidentAlertName, "obzly_kind": KindIncident, "severity": SeverityHigh},
			StartsAt: startsAt, Fingerprint: "incident:" + sessionID.String(),
			Incident: &IncidentDetails{
				ID: sessionID, Services: []string{"checkout", "payments"}, SignalCount: 14, ErrorLogs: 9, ErrorSpans: 5,
				TopServices: []ImpactedService{
					{Name: "checkout", ErrorLogs: 7, ErrorSpans: 4, Total: 11},
					{Name: "payments", ErrorLogs: 2, ErrorSpans: 1, Total: 3},
				},
			},
		}
		if status == StatusResolved {
			endsAt := startsAt.Add(25 * time.Minute)
			n.EndsAt = &endsAt
		}
		return n
	}
	return []templateSample{
		{"firing-critical", "Critical firing", alert(StatusFiring, SeverityCritical)},
		{"firing-high", "High firing", alert(StatusFiring, SeverityHigh)},
		{"firing-medium", "Medium firing", alert(StatusFiring, SeverityMedium)},
		{"firing-low", "Low firing", alert(StatusFiring, SeverityLow)},
		{"resolved", "Resolved", alert(StatusResolved, SeverityHigh)},
		{"test", "Test", test},
		{"incident-open", "Incident opened", incident(StatusFiring)},
		{"incident-end", "Incident ended", incident(StatusResolved)},
	}
}
