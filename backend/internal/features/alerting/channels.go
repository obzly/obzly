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
	"sort"
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

// send delivers n through a channel. The error holds only what is safe to
// show (an HTTP status, not the receiver's body).
func (s *sender) send(ctx context.Context, kind string, cfg ChannelConfig, n Notification) error {
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	switch kind {
	case ChannelWebhook:
		body, _ := json.Marshal(webhookBody(n))
		return s.post(ctx, cfg.URL, cfg.Headers, body)
	case ChannelSlack:
		body, _ := json.Marshal(map[string]string{"text": slackEscape(text(n))})
		return s.post(ctx, cfg.URL, nil, body)
	case ChannelPagerDuty:
		body, _ := json.Marshal(pagerDutyBody(cfg.RoutingKey, n))
		return s.post(ctx, pagerDutyEvents, nil, body)
	case ChannelEmail:
		return s.email(ctx, cfg.To, subject(n), text(n))
	}
	return fmt.Errorf("unknown channel kind %q", kind)
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

func webhookBody(n Notification) map[string]any {
	return map[string]any{
		"version": "obzly.alert/1", "status": n.Status, "projectId": n.ProjectID, "ruleId": n.RuleID,
		"ruleName": n.RuleName, "source": n.Source, "summary": n.Summary, "labels": n.Labels,
		"value": n.Measured, "comparison": n.Comparison, "threshold": n.Threshold,
		"fingerprint": n.Fingerprint, "startsAt": n.StartsAt, "endsAt": n.EndsAt,
	}
}

func pagerDutyBody(routingKey string, n Notification) map[string]any {
	action := "trigger"
	if n.Status == StatusResolved {
		action = "resolve"
	}
	dedup := n.Fingerprint
	if n.RuleID != nil {
		dedup = n.RuleID.String() + ":" + n.Fingerprint
	}
	return map[string]any{
		"routing_key": routingKey, "event_action": action, "dedup_key": dedup,
		"payload": map[string]any{
			"summary": subject(n), "source": "obzly", "severity": "error",
			"custom_details": map[string]any{"labels": n.Labels, "value": n.Measured, "summary": n.Summary},
		},
	}
}

func subject(n Notification) string {
	return fmt.Sprintf("[%s] %s", strings.ToUpper(n.Status), n.RuleName)
}

func text(n Notification) string {
	var b strings.Builder
	b.WriteString(subject(n))
	if n.Summary != "" {
		b.WriteString(": " + n.Summary)
	}
	if n.Status != StatusTest {
		fmt.Fprintf(&b, "\nvalue %s (%s %s)", formatValue(n.Measured), n.Comparison, formatValue(n.Threshold))
	}
	keys := make([]string, 0, len(n.Labels))
	for key := range n.Labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(&b, "\n%s=%s", key, n.Labels[key])
	}
	return b.String()
}

// slackEscaper escapes Slack's control characters, so a label or pattern
// taken from telemetry can't become a mention (<!channel>) or a disguised
// link (<https://...|text>).
var slackEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func slackEscape(s string) string { return slackEscaper.Replace(s) }

func formatValue(v float64) string {
	return strconv.FormatFloat(v, 'g', 6, 64)
}
