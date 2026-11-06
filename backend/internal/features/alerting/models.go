// Package alerting is alert rules, their evaluation and notifications: rules over
// metrics (PromQL), logs or traces run on worker replicas through the Prometheus state
// machine (pending -> firing -> resolved); notifications go out through an outbox to
// webhook, Slack, PagerDuty or email channels.
package alerting

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	logs_core "obzly/internal/features/logs/core"

	"github.com/google/uuid"
)

// Rule sources.
const (
	SourceMetrics = "metrics"
	SourceLogs    = "logs"
	SourceTraces  = "traces"
	// SourceLogPatterns: message patterns of the logs matching Query.Logs
	// that are new against the baseline before the window, or whose rate
	// rose to at least Threshold times it - one series per pattern.
	SourceLogPatterns = "log_patterns"
	// SourceAICost: an AI budget's spend this period (managed rules only;
	// see SetAICostSource).
	SourceAICost = "ai_cost"
)

// Channel kinds.
const (
	ChannelWebhook   = "webhook"
	ChannelSlack     = "slack"
	ChannelPagerDuty = "pagerduty"
	ChannelEmail     = "email"
)

// Alert states.
const (
	StatePending  = "pending"
	StateFiring   = "firing"
	StateResolved = "resolved"
)

// Notification statuses.
const (
	StatusFiring   = "firing"
	StatusResolved = "resolved"
	StatusTest     = "test"
)

// NotificationChannel is where notifications go, managed in Obzly settings:
// a Site's own (source local) or one its control plane sent (central),
// usable by every project or the listed ones. Its config (URLs, keys,
// recipients) is stored encrypted and never returned whole.
type NotificationChannel struct {
	ID uuid.UUID `json:"id" gorm:"column:id;primaryKey"`
	// ProjectID is the project a channel created before shared channels
	// belonged to; ProjectIDs carries it now.
	ProjectID       *uuid.UUID `json:"-"           gorm:"column:project_id"`
	Name            string     `json:"name"        gorm:"column:name"`
	Kind            string     `json:"kind"        gorm:"column:kind"`
	ConfigEncrypted string     `json:"-"           gorm:"column:config_encrypted"`
	Source          string     `json:"source"      gorm:"column:source"`
	AllProjects     bool       `json:"allProjects" gorm:"column:all_projects"`
	ProjectIDs      ProjectIDs `json:"projectIds"  gorm:"column:project_ids"`
	CreatedBy       *uuid.UUID `json:"createdBy"   gorm:"column:created_by"`
	CreatedAt       time.Time  `json:"createdAt"   gorm:"column:created_at"`
	UpdatedAt       time.Time  `json:"updatedAt"   gorm:"column:updated_at"`
	// Target is a non-secret description of where it sends (a URL's host,
	// a key's last characters, the recipients).
	Target string `json:"target" gorm:"-"`
}

func (NotificationChannel) TableName() string { return "notification_channels" }

// ProjectIDs are the projects a channel is available to (as strings).
type ProjectIDs []string

func (p ProjectIDs) Value() (driver.Value, error) {
	if p == nil {
		p = ProjectIDs{}
	}
	return jsonValue(p)
}
func (p *ProjectIDs) Scan(value any) error { return scanJSON(value, p) }

// ChannelConfig is a channel's secret settings: URL (webhook, Slack) with
// optional Headers (webhook), RoutingKey (PagerDuty), To (email).
type ChannelConfig struct {
	URL        string            `json:"url,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	RoutingKey string            `json:"routingKey,omitempty"`
	To         []string          `json:"to,omitempty"`
}

// AlertRule is a condition evaluated every IntervalSeconds: the query's
// value per series/group compared to Threshold; a series past it for
// ForSeconds fires, and notifies its channels again every RepeatSeconds
// while firing (0: once).
type AlertRule struct {
	ID              uuid.UUID   `json:"id"              gorm:"column:id;primaryKey"`
	ProjectID       uuid.UUID   `json:"projectId"       gorm:"column:project_id"`
	Name            string      `json:"name"            gorm:"column:name"`
	Enabled         bool        `json:"enabled"         gorm:"column:enabled"`
	Source          string      `json:"source"          gorm:"column:source"`
	Query           RuleQuery   `json:"query"           gorm:"column:query"`
	Comparison      string      `json:"comparison"      gorm:"column:comparison"`
	Threshold       float64     `json:"threshold"       gorm:"column:threshold"`
	WindowSeconds   int         `json:"windowSeconds"   gorm:"column:window_seconds"`
	IntervalSeconds int         `json:"intervalSeconds" gorm:"column:interval_seconds"`
	ForSeconds      int         `json:"forSeconds"      gorm:"column:for_seconds"`
	RepeatSeconds   int         `json:"repeatSeconds"   gorm:"column:repeat_seconds"`
	Labels          Labels      `json:"labels"          gorm:"column:labels"`
	Summary         string      `json:"summary"         gorm:"column:summary"`
	LastEvalAt      *time.Time  `json:"lastEvalAt"      gorm:"column:last_eval_at"`
	LastError       string      `json:"lastError"       gorm:"column:last_error"`
	CreatedBy       *uuid.UUID  `json:"createdBy"       gorm:"column:created_by"`
	CreatedAt       time.Time   `json:"createdAt"       gorm:"column:created_at"`
	UpdatedAt       time.Time   `json:"updatedAt"       gorm:"column:updated_at"`
	ChannelIDs      []uuid.UUID `json:"channelIds"      gorm:"-"`
	// ManagedBy names the feature that maintains this rule
	// ("synthetics:<monitor id>:<kind>"); such rules are read-only here.
	ManagedBy string `json:"managedBy" gorm:"column:managed_by"`
}

func (AlertRule) TableName() string { return "alert_rules" }

// RuleQuery is what a rule measures. Metrics: PromQL (an instant query;
// each result series is checked). Logs: the count of logs matching Logs
// in the window, per GroupBy value when set. Traces: Metric (count,
// error_count, avg/p50/p95/p99_duration_ms) of spans of ServiceName /
// OperationName (RootSpansOnly: requests only) in the window, per GroupBy.
// Log patterns: each pattern of the logs matching Logs in the window
// against the BaselineSeconds before it (MinCount: fewest logs to count);
// the value is its rate over the baseline's, very large when it's new.
type RuleQuery struct {
	PromQL          string               `json:"promql,omitempty"`
	Logs            *logs_core.QueryNode `json:"logs,omitempty"`
	GroupBy         string               `json:"groupBy,omitempty"`
	Metric          string               `json:"metric,omitempty"`
	ServiceName     string               `json:"serviceName,omitempty"`
	OperationName   string               `json:"operationName,omitempty"`
	RootSpansOnly   bool                 `json:"rootSpansOnly,omitempty"`
	BaselineSeconds int                  `json:"baselineSeconds,omitempty"`
	MinCount        int64                `json:"minCount,omitempty"`
	// BudgetID is the AI budget an ai_cost rule watches.
	BudgetID string `json:"budgetId,omitempty"`
}

func (q RuleQuery) Value() (driver.Value, error) { return jsonValue(q) }
func (q *RuleQuery) Scan(value any) error        { return scanJSON(value, q) }

// Labels are an alert's identifying labels.
type Labels map[string]string

func (l Labels) Value() (driver.Value, error) {
	if l == nil {
		l = Labels{}
	}
	return jsonValue(l)
}
func (l *Labels) Scan(value any) error { return scanJSON(value, l) }

// AlertState is one series of a rule that is pending, firing, or was
// recently resolved.
type AlertState struct {
	RuleID         uuid.UUID  `json:"ruleId"         gorm:"column:rule_id;primaryKey"`
	Fingerprint    string     `json:"fingerprint"    gorm:"column:fingerprint;primaryKey"`
	Labels         Labels     `json:"labels"         gorm:"column:labels"`
	State          string     `json:"state"          gorm:"column:state"`
	Value          float64    `json:"value"          gorm:"column:value"`
	ActiveAt       time.Time  `json:"activeAt"       gorm:"column:active_at"`
	FiredAt        *time.Time `json:"firedAt"        gorm:"column:fired_at"`
	ResolvedAt     *time.Time `json:"resolvedAt"     gorm:"column:resolved_at"`
	LastNotifiedAt *time.Time `json:"lastNotifiedAt" gorm:"column:last_notified_at"`
	UpdatedAt      time.Time  `json:"updatedAt"      gorm:"column:updated_at"`
}

func (AlertState) TableName() string { return "alert_states" }

// Silence mutes the notifications of alerts whose labels match every
// matcher, between StartsAt and EndsAt.
type Silence struct {
	ID        uuid.UUID  `json:"id"        gorm:"column:id;primaryKey"`
	ProjectID uuid.UUID  `json:"projectId" gorm:"column:project_id"`
	Matchers  Matchers   `json:"matchers"  gorm:"column:matchers"`
	StartsAt  time.Time  `json:"startsAt"  gorm:"column:starts_at"`
	EndsAt    time.Time  `json:"endsAt"    gorm:"column:ends_at"`
	Comment   string     `json:"comment"   gorm:"column:comment"`
	CreatedBy *uuid.UUID `json:"createdBy" gorm:"column:created_by"`
	CreatedAt time.Time  `json:"createdAt" gorm:"column:created_at"`
	// ManagedBy names what maintains this silence ("synthetics-maintenance:
	// <window id>:..."); such silences are read-only here.
	ManagedBy string `json:"managedBy" gorm:"column:managed_by"`
}

func (Silence) TableName() string { return "silences" }

// Matcher matches a label by value, or by an RE2 regex anchored at both
// ends when Regex.
type Matcher struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Regex bool   `json:"regex,omitempty"`
}

type Matchers []Matcher

func (m Matchers) Value() (driver.Value, error) {
	if m == nil {
		m = Matchers{}
	}
	return jsonValue(m)
}
func (m *Matchers) Scan(value any) error { return scanJSON(value, m) }

// OutboxItem is one notification to deliver to one channel.
type OutboxItem struct {
	ID            uuid.UUID    `gorm:"column:id;primaryKey"`
	ProjectID     uuid.UUID    `gorm:"column:project_id"`
	ChannelID     uuid.UUID    `gorm:"column:channel_id"`
	RuleID        *uuid.UUID   `gorm:"column:rule_id"`
	Fingerprint   string       `gorm:"column:fingerprint"`
	Status        string       `gorm:"column:status"`
	Payload       Notification `gorm:"column:payload"`
	Attempts      int          `gorm:"column:attempts"`
	NextAttemptAt time.Time    `gorm:"column:next_attempt_at"`
	SentAt        *time.Time   `gorm:"column:sent_at"`
	FailedAt      *time.Time   `gorm:"column:failed_at"`
	LastError     string       `gorm:"column:last_error"`
	CreatedAt     time.Time    `gorm:"column:created_at"`
}

func (OutboxItem) TableName() string { return "notification_outbox" }

// Notification is what every channel renders.
type Notification struct {
	Status      string     `json:"status"`
	ProjectID   uuid.UUID  `json:"projectId"`
	RuleID      *uuid.UUID `json:"ruleId,omitempty"`
	RuleName    string     `json:"ruleName"`
	Source      string     `json:"source,omitempty"`
	Summary     string     `json:"summary,omitempty"`
	Comparison  string     `json:"comparison,omitempty"`
	Threshold   float64    `json:"threshold"`
	Measured    float64    `json:"value"`
	Labels      Labels     `json:"labels"`
	Fingerprint string     `json:"fingerprint,omitempty"`
	StartsAt    time.Time  `json:"startsAt"`
	EndsAt      *time.Time `json:"endsAt,omitempty"`
}

func (n Notification) Value() (driver.Value, error) { return jsonValue(n) }
func (n *Notification) Scan(value any) error        { return scanJSON(value, n) }

func jsonValue(v any) (driver.Value, error) {
	encoded, err := json.Marshal(v)
	return string(encoded), err
}

func scanJSON(value any, target any) error {
	switch v := value.(type) {
	case nil:
		return nil
	case []byte:
		return json.Unmarshal(v, target)
	case string:
		return json.Unmarshal([]byte(v), target)
	default:
		return fmt.Errorf("unexpected JSON column type %T", value)
	}
}
