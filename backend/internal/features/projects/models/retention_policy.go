package projects_models

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"obzly/internal/features/orgplan"

	"github.com/google/uuid"
)

// Signals a retention policy applies to.
const (
	SignalLogs    = "logs"
	SignalTraces  = "traces"
	SignalMetrics = "metrics"
)

var Signals = []string{SignalLogs, SignalTraces, SignalMetrics}

// RetentionPolicy is one signal's retention and sampling for a project. Zero values
// mean the defaults: the project's log retention, no rollup-only aging, keep everything.
type RetentionPolicy struct {
	ProjectID uuid.UUID `json:"projectId" gorm:"column:project_id;primaryKey"`
	Signal    string    `json:"signal"    gorm:"column:signal;primaryKey"`
	// RetentionDays: how long raw data is kept; 0 = the project's log
	// retention (MaxLogsLifeDays). A plan's retention caps it.
	RetentionDays int `json:"retentionDays" gorm:"column:retention_days"`
	// RollupDays (metrics): keep the 5 m / 1 h rollups this long when it is
	// longer than RetentionDays; 0 = delete them with the samples.
	RollupDays int `json:"rollupDays" gorm:"column:rollup_days"`
	// SampleRate (traces): the share of traces kept besides error and slow
	// ones, decided per trace when a day's spans are compacted; 1 = all.
	SampleRate float64 `json:"sampleRate" gorm:"column:sample_rate"`
	// SlowMs (traces): a trace with a span at least this long is always
	// kept; 0 = no slow rule.
	SlowMs int `json:"slowMs" gorm:"column:slow_ms"`
	// SampleRules (logs): head sampling at ingest, first match wins.
	SampleRules LogSampleRules `json:"sampleRules" gorm:"column:sample_rules"`
	UpdatedAt   time.Time      `json:"updatedAt"   gorm:"column:updated_at"`
}

func (RetentionPolicy) TableName() string {
	return "retention_policies"
}

// LogSampleRule keeps Rate of the logs of Service (any if empty) at Level
// (any if empty).
type LogSampleRule struct {
	Service string  `json:"service"`
	Level   string  `json:"level"`
	Rate    float64 `json:"rate"`
}

type LogSampleRules []LogSampleRule

func (r LogSampleRules) Value() (driver.Value, error) {
	if r == nil {
		r = LogSampleRules{}
	}
	encoded, err := json.Marshal(r)
	return string(encoded), err
}

func (r *LogSampleRules) Scan(value any) error {
	switch v := value.(type) {
	case nil:
		*r = LogSampleRules{}
		return nil
	case []byte:
		return json.Unmarshal(v, r)
	case string:
		return json.Unmarshal([]byte(v), r)
	default:
		return fmt.Errorf("unexpected sample_rules type %T", value)
	}
}

// DefaultRetentionPolicy is signal's policy before anyone sets one.
func DefaultRetentionPolicy(projectID uuid.UUID, signal string) RetentionPolicy {
	return RetentionPolicy{ProjectID: projectID, Signal: signal, SampleRate: 1, SampleRules: LogSampleRules{}}
}

// Validate checks a policy as a user sent it.
func (p *RetentionPolicy) Validate() error {
	switch {
	case p.Signal != SignalLogs && p.Signal != SignalTraces && p.Signal != SignalMetrics:
		return fmt.Errorf("unknown signal %q", p.Signal)
	case p.RetentionDays < 0 || p.RollupDays < 0 || p.SlowMs < 0:
		return errors.New("days and thresholds can't be negative")
	case p.RetentionDays > 3650 || p.RollupDays > 3650:
		return errors.New("retention is at most 3650 days")
	case p.SampleRate < 0 || p.SampleRate > 1:
		return errors.New("sample rate must be between 0 and 1")
	case p.RollupDays > 0 && p.Signal != SignalMetrics:
		return errors.New("rollup retention applies to metrics only")
	case (p.SampleRate != 1 || p.SlowMs > 0) && p.Signal != SignalTraces:
		return errors.New("trace sampling applies to traces only")
	case len(p.SampleRules) > 0 && p.Signal != SignalLogs:
		return errors.New("sampling rules apply to logs only")
	case len(p.SampleRules) > 50:
		return errors.New("at most 50 sampling rules")
	}
	for _, rule := range p.SampleRules {
		if rule.Rate < 0 || rule.Rate > 1 {
			return errors.New("a rule's rate must be between 0 and 1")
		}
	}
	return nil
}

// EffectiveRetentionDays is how long signal's raw data is kept: the
// policy's days, else the project's log retention, capped by the owner's
// plan and the organization's.
func (p *Project) EffectiveRetentionDays(policy RetentionPolicy) int {
	days := policy.RetentionDays
	if days == 0 {
		days = p.MaxLogsLifeDays
	}
	if p.Plan != nil {
		days = orgplan.Stricter(days, p.Plan.MaxLogsLifeDays)
	}
	return orgplan.Stricter(days, orgplan.Current().MaxLogsLifeDays)
}

// RateFor is the share of logs of service at level to keep: the first
// matching rule's, else 1. level is matched case-insensitively.
func (r LogSampleRules) RateFor(service, level string) float64 {
	for _, rule := range r {
		if (rule.Service == "" || rule.Service == service) &&
			(rule.Level == "" || strings.EqualFold(rule.Level, level)) {
			return rule.Rate
		}
	}
	return 1
}
