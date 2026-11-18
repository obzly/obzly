package alerting

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"text/template"
	"text/template/parse"
	"time"
	"unicode"
	"unicode/utf8"

	"obzly/internal/config"

	"github.com/google/uuid"
)

const (
	maxTemplateBytes = 16 << 10
	maxRenderedBytes = 64 << 10
	renderTimeout    = 2 * time.Second
	// pagerDutySummaryLimit is the Events API's limit on payload.summary.
	pagerDutySummaryLimit = 1024
	emailSubjectLimit     = 300
)

// Template fields.
const (
	FieldSubject = "subject"
	FieldBody    = "body"
)

var (
	errRenderTimeout  = errors.New("the template took too long to render")
	errRenderTooLarge = fmt.Errorf("the rendered message is larger than %d KB", maxRenderedBytes>>10)

	templateErrorPattern = regexp.MustCompile(`^template: [^:]+:(\d+):(?:\d+:)? ?(.*)$`)
)

// MessageTemplate is a channel kind's editable parts: an email's Subject
// and Body; Slack's text, PagerDuty's summary, or a webhook's custom JSON
// in Body (an empty webhook Body sends the built-in JSON).
type MessageTemplate struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// TemplateError is one problem with a template; Line is 0 when unknown.
type TemplateError struct {
	Field   string `json:"field"`
	Line    int    `json:"line"`
	Message string `json:"message"`
}

func (e TemplateError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("%s, line %d: %s", e.Field, e.Line, e.Message)
	}
	return e.Field + ": " + e.Message
}

// TemplateErrors is every problem found in a template.
type TemplateErrors []TemplateError

func (e TemplateErrors) Error() string {
	messages := make([]string, len(e))
	for i, item := range e {
		messages[i] = item.Error()
	}
	return strings.Join(messages, "; ")
}

// renderContext is what a notification doesn't carry: names, the link, and
// the channel's emoji switch.
type renderContext struct {
	ProjectName string
	SiteID      string
	SiteName    string
	Link        string
	UseEmoji    bool
	Now         time.Time
}

// rendered is one channel's message.
type rendered struct {
	Subject string
	Text    string
	// JSON is a webhook's body.
	JSON  []byte
	Color string
}

// templateData is "." in a template. Every string from a rule, a label or
// telemetry is escaped for the channel before the template sees it.
type templateData struct {
	Kind         string
	Status       string
	Test         bool
	Severity     string
	SeverityKey  string
	Emoji        string
	Rule         templateRule
	Summary      string
	Value        string
	Threshold    string
	Comparison   string
	Labels       map[string]string
	SortedLabels []templateLabel
	StartsAt     string
	StartsAtISO  string
	EndsAt       string
	EndsAtISO    string
	Duration     string
	Project      templateNamed
	Site         templateNamed
	Source       string
	Link         string
	Incident     *templateIncident
}

type templateRule struct {
	ID          string
	Name        string
	Description string
}

type templateNamed struct {
	ID   string
	Name string
}

type templateLabel struct {
	Name  string
	Value string
}

type templateIncident struct {
	ID           string
	Services     []string
	ServiceCount int
	SignalCount  int
	ErrorLogs    int
	ErrorSpans   int
	TopServices  []templateImpact
}

type templateImpact struct {
	Name       string
	ErrorLogs  int64
	ErrorSpans int64
	Total      int64
}

// templateFuncs is everything a template can call besides text/template's
// builtins: pure string helpers, no file, environment or process access.
// Each result is capped, so nesting can't grow a message without bound.
var templateFuncs = template.FuncMap{
	"upper":     strings.ToUpper,
	"lower":     strings.ToLower,
	"title":     titleCase,
	"trim":      strings.TrimSpace,
	"truncate":  truncateRunes,
	"join":      joinCapped,
	"default":   defaultString,
	"contains":  func(substr, s string) bool { return strings.Contains(s, substr) },
	"hasPrefix": func(prefix, s string) bool { return strings.HasPrefix(s, prefix) },
	"hasSuffix": func(suffix, s string) bool { return strings.HasSuffix(s, suffix) },
	"label":     func(labels map[string]string, name string) string { return labels[name] },
}

// DefaultTemplate is kind's built-in template.
func DefaultTemplate(kind string) MessageTemplate {
	switch kind {
	case ChannelEmail:
		return MessageTemplate{Subject: defaultTitle, Body: defaultEmailBody}
	case ChannelSlack:
		return MessageTemplate{Body: defaultSlackBody}
	case ChannelPagerDuty:
		return MessageTemplate{Body: defaultPagerDutySummary}
	}
	return MessageTemplate{}
}

// ValidateTemplate checks kind's template parses, stays within the limits
// and renders every sample notification (and, for a webhook, renders valid
// JSON). Nil when it's fine.
func ValidateTemplate(kind string, tpl MessageTemplate) TemplateErrors {
	if errs := parseErrors(kind, tpl); len(errs) > 0 {
		return errs
	}
	var errs TemplateErrors
	seen := map[string]bool{}
	for _, sample := range sampleNotifications(time.Now().UTC()) {
		if _, err := renderWith(kind, tpl, sample.Notification, sample.context(true)); err != nil {
			for _, item := range asTemplateErrors(err) {
				key := item.Field + "\x00" + item.Message
				if !seen[key] {
					seen[key] = true
					item.Message += " (" + sample.Label + ")"
					errs = append(errs, item)
				}
			}
		}
	}
	return errs
}

// render renders n for a channel with tpl; when tpl fails it renders the
// built-in template instead (a notification is never dropped for its
// template) and returns tpl's failure.
func render(kind string, tpl MessageTemplate, n Notification, rc renderContext) (rendered, error) {
	out, err := renderWith(kind, tpl, n, rc)
	if err == nil {
		return out, nil
	}
	fallback, fallbackErr := renderWith(kind, DefaultTemplate(kind), n, rc)
	if fallbackErr != nil {
		// Never expected (the built-ins are tested); still send something.
		title := fmt.Sprintf("[%s] %s", strings.ToUpper(n.Status), n.RuleName)
		body, _ := json.Marshal(webhookBody(n, rc))
		fallback = rendered{Subject: title, Text: title, JSON: body, Color: slackColor(n.Status, n.Severity)}
	}
	return fallback, err
}

// renderWith renders n with tpl, nothing else.
func renderWith(kind string, tpl MessageTemplate, n Notification, rc renderContext) (rendered, error) {
	out := rendered{Color: slackColor(n.Status, n.Severity)}
	switch kind {
	case ChannelEmail:
		subject, err := execute(FieldSubject, tpl.Subject, buildData(n, rc, escapePlain))
		if err != nil {
			return out, err
		}
		out.Subject = truncateRunes(emailSubjectLimit, strings.TrimSpace(collapseControl(subject)))
		if out.Subject == "" {
			return out, TemplateError{Field: FieldSubject, Message: "the subject renders empty"}
		}
		if out.Text, err = execute(FieldBody, tpl.Body, buildData(n, rc, escapePlain)); err != nil {
			return out, err
		}
	case ChannelSlack:
		text, err := execute(FieldBody, tpl.Body, buildData(n, rc, escapeSlack))
		if err != nil {
			return out, err
		}
		out.Text = text
	case ChannelPagerDuty:
		text, err := execute(FieldBody, tpl.Body, buildData(n, rc, escapePlain))
		if err != nil {
			return out, err
		}
		out.Text = truncateRunes(pagerDutySummaryLimit, strings.TrimSpace(collapseControl(text)))
	case ChannelWebhook:
		if strings.TrimSpace(tpl.Body) == "" {
			body, err := json.Marshal(webhookBody(n, rc))
			out.JSON = body
			return out, err
		}
		text, err := execute(FieldBody, tpl.Body, buildData(n, rc, escapeJSON))
		if err != nil {
			return out, err
		}
		if err := checkJSON(text); err != nil {
			return out, err
		}
		out.JSON = []byte(text)
		return out, nil
	default:
		return out, fmt.Errorf("unknown channel kind %q", kind)
	}
	if strings.TrimSpace(out.Text) == "" {
		return out, TemplateError{Field: FieldBody, Message: "the message renders empty"}
	}
	return out, nil
}

// webhookBody is the built-in webhook JSON: v1.1's fields plus kind,
// severity, description, link and, for incidents, incident.
func webhookBody(n Notification, rc renderContext) map[string]any {
	body := map[string]any{
		"version": "obzly.alert/1", "status": n.Status, "projectId": n.ProjectID, "ruleId": n.RuleID,
		"ruleName": n.RuleName, "source": n.Source, "summary": n.Summary, "labels": n.Labels,
		"value": n.Measured, "comparison": n.Comparison, "threshold": n.Threshold,
		"fingerprint": n.Fingerprint, "startsAt": n.StartsAt, "endsAt": n.EndsAt,
		"kind": kindOf(n), "severity": normalizeSeverity(n.Severity), "description": n.RuleDescription,
		"link": rc.Link,
	}
	if n.Incident != nil {
		body["incident"] = n.Incident
	}
	return body
}

// notificationLink opens the alert (or incident) in Obzly: the Alerts or
// Incidents page of its project, on its Site. Empty when no app address is
// known (see linkBase).
func notificationLink(n Notification) string {
	base, siteID := linkBase()
	return buildLink(base, siteID, n)
}

func buildLink(base, siteID string, n Notification) string {
	if base == "" {
		return ""
	}
	if n.ProjectID == uuid.Nil {
		return base + "/"
	}
	query := url.Values{}
	if siteID != "" {
		query.Set("site", siteID)
	}
	query.Set("project", n.ProjectID.String())
	if n.Incident != nil {
		query.Set("tab", "incidents")
		query.Set("session", n.Incident.ID.String())
	} else {
		query.Set("tab", "alerts")
		if n.RuleID != nil {
			query.Set("rule", n.RuleID.String())
		}
	}
	return base + "/?" + query.Encode()
}

// linkBase is the browser-facing address of the app: on a Site enrolled in
// a control plane, the control plane's (users reach the Site through it,
// CONTROL_PLANE_ISSUER) with the Site to select; otherwise PUBLIC_URL.
func linkBase() (base, siteID string) {
	env := config.GetEnv()
	if env.SiteExchangeEnabled() && !env.ServiceMode.IsControlPlane() {
		return strings.TrimRight(env.ControlPlaneIssuer, "/"), env.SiteID
	}
	return strings.TrimRight(env.PublicURL, "/"), ""
}

func kindOf(n Notification) string {
	if n.Kind == KindIncident || n.Incident != nil {
		return KindIncident
	}
	return KindAlert
}

func buildData(n Notification, rc renderContext, esc func(string) string) templateData {
	status := n.Status
	data := templateData{
		Kind:        kindOf(n),
		Status:      esc(status),
		Test:        status == StatusTest,
		Severity:    severityTitle(n.Severity),
		SeverityKey: normalizeSeverity(n.Severity),
		Rule:        templateRule{Name: esc(n.RuleName), Description: esc(n.RuleDescription)},
		Summary:     esc(n.Summary),
		Value:       formatValue(n.Measured),
		Threshold:   formatValue(n.Threshold),
		Comparison:  esc(n.Comparison),
		Labels:      map[string]string{},
		Project:     templateNamed{ID: n.ProjectID.String(), Name: esc(rc.ProjectName)},
		Site:        templateNamed{ID: esc(rc.SiteID), Name: esc(rc.SiteName)},
		Source:      esc(n.Source),
		Link:        esc(rc.Link),
		Duration:    humanDuration(notificationDuration(n, rc.Now)),
	}
	if n.ProjectID == uuid.Nil {
		data.Project.ID = ""
	}
	if rc.UseEmoji {
		data.Emoji = emojiFor(status, n.Severity)
	}
	if n.RuleID != nil {
		data.Rule.ID = n.RuleID.String()
	}
	if !n.StartsAt.IsZero() {
		data.StartsAt = n.StartsAt.UTC().Format("2006-01-02 15:04:05 UTC")
		data.StartsAtISO = n.StartsAt.UTC().Format(time.RFC3339)
	}
	if n.EndsAt != nil {
		data.EndsAt = n.EndsAt.UTC().Format("2006-01-02 15:04:05 UTC")
		data.EndsAtISO = n.EndsAt.UTC().Format(time.RFC3339)
	}
	keys := make([]string, 0, len(n.Labels))
	for key := range n.Labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		name, value := esc(key), esc(n.Labels[key])
		data.Labels[name] = value
		data.SortedLabels = append(data.SortedLabels, templateLabel{Name: name, Value: value})
	}
	if n.Incident != nil {
		incident := &templateIncident{
			ID: n.Incident.ID.String(), ServiceCount: len(n.Incident.Services),
			SignalCount: n.Incident.SignalCount, ErrorLogs: n.Incident.ErrorLogs, ErrorSpans: n.Incident.ErrorSpans,
			Services: make([]string, 0, len(n.Incident.Services)),
		}
		for _, service := range n.Incident.Services {
			incident.Services = append(incident.Services, esc(service))
		}
		for _, impact := range n.Incident.TopServices {
			incident.TopServices = append(incident.TopServices, templateImpact{
				Name: esc(impact.Name), ErrorLogs: impact.ErrorLogs, ErrorSpans: impact.ErrorSpans, Total: impact.Total,
			})
		}
		data.Incident = incident
	}
	return data
}

func notificationDuration(n Notification, now time.Time) time.Duration {
	if n.StartsAt.IsZero() || n.Status == StatusTest {
		return 0
	}
	end := now
	if n.EndsAt != nil {
		end = *n.EndsAt
	}
	return end.Sub(n.StartsAt)
}

// humanDuration is "45s", "12m", "3h 5m" or "2d 4h"; empty for none.
func humanDuration(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		hours := int(d.Hours())
		return fmt.Sprintf("%dh %dm", hours, int(d.Minutes())-hours*60)
	}
	days := int(d.Hours()) / 24
	return fmt.Sprintf("%dd %dh", days, int(d.Hours())-days*24)
}

func parseErrors(kind string, tpl MessageTemplate) TemplateErrors {
	var errs TemplateErrors
	fields := []struct{ name, source string }{{FieldBody, tpl.Body}}
	if kind == ChannelEmail {
		fields = append([]struct{ name, source string }{{FieldSubject, tpl.Subject}}, fields...)
	}
	for _, field := range fields {
		if strings.TrimSpace(field.source) == "" {
			if kind == ChannelWebhook {
				continue
			}
			errs = append(errs, TemplateError{Field: field.name, Message: "can't be empty"})
			continue
		}
		if _, err := parseTemplate(field.name, field.source); err != nil {
			errs = append(errs, asTemplateErrors(err)...)
		}
	}
	return errs
}

func parseTemplate(name, source string) (*template.Template, error) {
	if len(source) > maxTemplateBytes {
		return nil, TemplateError{Field: name, Message: fmt.Sprintf("is longer than %d KB", maxTemplateBytes>>10)}
	}
	if !utf8.ValidString(source) {
		return nil, TemplateError{Field: name, Message: "isn't valid UTF-8"}
	}
	parsed, err := template.New(name).Option("missingkey=zero").Funcs(templateFuncs).Parse(source)
	if err != nil {
		return nil, templateError(name, err)
	}
	if len(parsed.Templates()) > 1 {
		return nil, TemplateError{Field: name, Message: "define and block aren't supported"}
	}
	if err := checkNodes(name, parsed.Root); err != nil {
		return nil, err
	}
	return parsed, nil
}

// checkNodes refuses what could run without bound: calling templates
// (recursion) and ranging over a number.
func checkNodes(field string, node parse.Node) error {
	switch typed := node.(type) {
	case *parse.ListNode:
		if typed == nil {
			return nil
		}
		for _, child := range typed.Nodes {
			if err := checkNodes(field, child); err != nil {
				return err
			}
		}
	case *parse.TemplateNode:
		return TemplateError{Field: field, Line: typed.Line, Message: "template calls aren't supported"}
	case *parse.ActionNode:
		return checkPipe(field, typed.Pipe, false)
	case *parse.IfNode:
		return checkBranch(field, &typed.BranchNode, false)
	case *parse.WithNode:
		return checkBranch(field, &typed.BranchNode, false)
	case *parse.RangeNode:
		return checkBranch(field, &typed.BranchNode, true)
	}
	return nil
}

func checkBranch(field string, branch *parse.BranchNode, isRange bool) error {
	if err := checkPipe(field, branch.Pipe, isRange); err != nil {
		return err
	}
	if err := checkNodes(field, branch.List); err != nil {
		return err
	}
	if branch.ElseList != nil {
		return checkNodes(field, branch.ElseList)
	}
	return nil
}

func checkPipe(field string, pipe *parse.PipeNode, isRange bool) error {
	if pipe == nil {
		return nil
	}
	line := pipe.Line
	// No arithmetic exists, so a number held in a variable can only come
	// from a literal: refusing those leaves nothing huge to range over.
	if len(pipe.Decl) > 0 && isNumberPipe(pipe) {
		return TemplateError{Field: field, Line: line, Message: "variables can't hold number literals"}
	}
	if isRange && isNumberPipe(pipe) {
		return TemplateError{Field: field, Line: line, Message: "range over a number isn't supported"}
	}
	return nil
}

func isNumberPipe(pipe *parse.PipeNode) bool {
	if len(pipe.Cmds) != 1 || len(pipe.Cmds[0].Args) != 1 {
		return false
	}
	switch arg := pipe.Cmds[0].Args[0].(type) {
	case *parse.NumberNode:
		return true
	case *parse.PipeNode:
		return isNumberPipe(arg)
	}
	return false
}

// execute runs source with data, bounded in size and time.
func execute(field, source string, data templateData) (string, error) {
	return executeWithin(renderTimeout, field, source, data)
}

func executeWithin(timeout time.Duration, field, source string, data templateData) (string, error) {
	parsed, err := parseTemplate(field, source)
	if err != nil {
		return "", err
	}
	stop := &atomic.Bool{}
	writer := &limitedWriter{limit: maxRenderedBytes, stop: stop}
	done := make(chan error, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				done <- fmt.Errorf("the template failed: %v", recovered)
			}
		}()
		done <- parsed.Execute(writer, data)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil {
			return "", templateError(field, err)
		}
		return writer.String(), nil
	case <-timer.C:
		// The goroutine stops at its next write.
		stop.Store(true)
		return "", TemplateError{Field: field, Message: errRenderTimeout.Error()}
	}
}

type limitedWriter struct {
	builder strings.Builder
	limit   int
	stop    *atomic.Bool
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if w.stop.Load() {
		return 0, errRenderTimeout
	}
	if w.builder.Len()+len(p) > w.limit {
		return 0, errRenderTooLarge
	}
	return w.builder.Write(p)
}

func (w *limitedWriter) String() string { return w.builder.String() }

// templateError turns a text/template error into a TemplateError with its
// line ("template: body:3:14: executing ... " -> line 3).
func templateError(field string, err error) error {
	var existing TemplateError
	if errors.As(err, &existing) {
		return existing
	}
	message := err.Error()
	switch {
	case errors.Is(err, errRenderTooLarge):
		return TemplateError{Field: field, Message: errRenderTooLarge.Error()}
	case errors.Is(err, errRenderTimeout):
		return TemplateError{Field: field, Message: errRenderTimeout.Error()}
	}
	if match := templateErrorPattern.FindStringSubmatch(message); match != nil {
		line, _ := strconv.Atoi(match[1])
		detail := match[2]
		// "executing \"body\" at <.Foo>: can't evaluate ..." -> "at <.Foo>: can't evaluate ..."
		if _, rest, found := strings.Cut(detail, "executing \""+field+"\" "); found {
			detail = rest
		}
		return TemplateError{Field: field, Line: line, Message: detail}
	}
	return TemplateError{Field: field, Message: strings.TrimPrefix(message, "template: ")}
}

func asTemplateErrors(err error) TemplateErrors {
	var many TemplateErrors
	if errors.As(err, &many) {
		return many
	}
	var one TemplateError
	if errors.As(err, &one) {
		return TemplateErrors{one}
	}
	return TemplateErrors{{Field: FieldBody, Message: err.Error()}}
}

// checkJSON reports where a webhook body stops being JSON.
func checkJSON(text string) error {
	var value any
	err := json.Unmarshal([]byte(text), &value)
	if err == nil {
		return nil
	}
	line := 0
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		line = 1 + strings.Count(text[:min(int(syntax.Offset), len(text))], "\n")
	}
	return TemplateError{Field: FieldBody, Line: line, Message: "renders invalid JSON: " + err.Error()}
}

// escapePlain drops control characters a value could use to forge lines or
// headers, keeping newlines and tabs.
func escapePlain(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

func escapeSlack(s string) string { return slackEscape(escapePlain(s)) }

// escapeJSON is s as the inside of a JSON string literal.
func escapeJSON(s string) string {
	encoded, _ := json.Marshal(s)
	return string(encoded[1 : len(encoded)-1])
}

func collapseControl(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

func truncateRunes(limit int, s string) string {
	if limit < 0 {
		limit = 0
	}
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	runes := []rune(s)
	if limit <= 1 {
		return string(runes[:limit])
	}
	return string(runes[:limit-1]) + "…"
}

func joinCapped(separator string, items []string) string {
	return truncateRunes(maxRenderedBytes, strings.Join(items, separator))
}

func defaultString(fallback, value string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	first, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(first)) + s[size:]
}
