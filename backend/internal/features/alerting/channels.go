package alerting

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"obzly/internal/config"
	"obzly/internal/features/sharedconfig"
	"obzly/internal/util/egress"
)

const (
	// sendTimeout bounds one delivery, response included.
	sendTimeout = 10 * time.Second
	// responseLimit: how much of a receiver's response is read (and
	// dropped - only the status code is ever surfaced).
	responseLimit   = 64 << 10
	pagerDutyEvents = "https://events.pagerduty.com/v2/enqueue"
)

// sender delivers notifications. Its client is built on first use, from
// the egress settings (validated at startup); a test may set it first.
type sender struct {
	once   sync.Once
	client *http.Client
	email  func(ctx context.Context, to []string, subject, body string) error
}

func newSender() *sender {
	return &sender{email: sharedconfig.SendMail}
}

func (s *sender) httpClient() *http.Client {
	s.once.Do(func() {
		if s.client != nil {
			return
		}
		env := config.GetEnv()
		allow, _ := egress.ParseCIDRs(env.AlertEgressAllowlist)
		deny, _ := egress.ParseCIDRs(env.AlertEgressDenylist)
		s.client = egress.NewClient(egress.PublicOnly(allow, deny), sendTimeout)
	})
	return s.client
}

func validateConfig(kind string, cfg ChannelConfig) error {
	switch kind {
	case ChannelWebhook, ChannelSlack:
		parsed, err := url.Parse(cfg.URL)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
			return errors.New("a valid http(s) URL is required")
		}
		if kind == ChannelSlack && len(cfg.Headers) > 0 {
			return errors.New("slack channels take no headers")
		}
		for name := range cfg.Headers {
			if strings.ContainsAny(name, " :\r\n") || name == "" {
				return fmt.Errorf("invalid header name %q", name)
			}
		}
	case ChannelPagerDuty:
		if strings.TrimSpace(cfg.RoutingKey) == "" {
			return errors.New("a PagerDuty routing key is required")
		}
	case ChannelEmail:
		if len(cfg.To) == 0 || len(cfg.To) > 20 {
			return errors.New("between 1 and 20 recipients are required")
		}
		for _, address := range cfg.To {
			if _, err := mail.ParseAddress(address); err != nil {
				return fmt.Errorf("invalid recipient %q", address)
			}
		}
	default:
		return fmt.Errorf("unknown channel kind %q", kind)
	}
	return nil
}

// describe is a channel's non-secret target.
func describe(kind string, cfg ChannelConfig) string {
	switch kind {
	case ChannelWebhook, ChannelSlack:
		if parsed, err := url.Parse(cfg.URL); err == nil {
			return parsed.Scheme + "://" + parsed.Host
		}
	case ChannelPagerDuty:
		key := cfg.RoutingKey
		if len(key) > 4 {
			key = key[len(key)-4:]
		}
		return "routing key …" + key
	case ChannelEmail:
		return strings.Join(cfg.To, ", ")
	}
	return ""
}

// send delivers a rendered notification through a channel. The error holds
// only what is safe to show (an HTTP status, not the receiver's body).
func (s *sender) send(ctx context.Context, kind string, cfg ChannelConfig, n Notification, msg rendered) error {
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	switch kind {
	case ChannelWebhook:
		return s.post(ctx, cfg.URL, cfg.Headers, msg.JSON)
	case ChannelSlack:
		body, _ := json.Marshal(slackBody(msg))
		return s.post(ctx, cfg.URL, nil, body)
	case ChannelPagerDuty:
		body, _ := json.Marshal(pagerDutyBody(cfg.RoutingKey, n, msg))
		return s.post(ctx, pagerDutyEvents, nil, body)
	case ChannelEmail:
		return s.email(ctx, cfg.To, msg.Subject, msg.Text)
	}
	return fmt.Errorf("unknown channel kind %q", kind)
}

// deliver renders n with tpl (the built-in template if tpl fails) and sends
// it; templateErr is tpl's render failure, sendErr the delivery's.
func (s *sender) deliver(
	ctx context.Context, kind string, cfg ChannelConfig, tpl MessageTemplate, n Notification, rc renderContext,
) (templateErr, sendErr error) {
	msg, templateErr := render(kind, tpl, n, rc)
	return templateErr, s.send(ctx, kind, cfg, n, msg)
}

func (s *sender) post(ctx context.Context, target string, headers map[string]string, body []byte) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return errors.New("invalid URL")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "Obzly-Alerting/1")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := s.httpClient().Do(request)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return fmt.Errorf("request failed: %w", urlErr.Err)
		}
		return fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = egress.ReadLimited(response.Body, responseLimit)
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("receiver answered HTTP %d", response.StatusCode)
	}
	return nil
}

// slackBody is the message with a side bar coloured by severity and status.
func slackBody(msg rendered) map[string]any {
	fallback, _, _ := strings.Cut(msg.Text, "\n")
	return map[string]any{"attachments": []map[string]any{{
		"color": msg.Color, "text": msg.Text, "fallback": fallback, "mrkdwn_in": []string{"text"},
	}}}
}

func pagerDutyBody(routingKey string, n Notification, msg rendered) map[string]any {
	action := "trigger"
	if n.Status == StatusResolved {
		action = "resolve"
	}
	dedup := n.Fingerprint
	if n.RuleID != nil {
		dedup = n.RuleID.String() + ":" + n.Fingerprint
	}
	details := map[string]any{"labels": n.Labels, "value": n.Measured, "summary": n.Summary}
	if n.RuleDescription != "" {
		details["runbook"] = n.RuleDescription
	}
	if n.Incident != nil {
		details["incident"] = n.Incident
	}
	body := map[string]any{
		"routing_key": routingKey, "event_action": action, "dedup_key": dedup,
		"payload": map[string]any{
			"summary": msg.Text, "source": "obzly", "severity": pagerDutySeverity(n.Severity),
			"custom_details": details,
		},
	}
	if link := notificationLink(n); link != "" {
		body["links"] = []map[string]string{{"href": link, "text": "Open in Obzly"}}
	}
	return body
}

// slackEscaper escapes Slack's control characters, so a label or pattern
// taken from telemetry can't become a mention (<!channel>) or a disguised
// link (<https://...|text>).
var slackEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func slackEscape(s string) string { return slackEscaper.Replace(s) }

func formatValue(v float64) string {
	return strconv.FormatFloat(v, 'g', 6, 64)
}
