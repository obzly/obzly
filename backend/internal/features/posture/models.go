// Package posture keeps cloud posture findings on a Site: native findings and baseline results a
// probe sends, with a lifecycle (open, resolved, reopened, suppressed) and per-scope runs that
// decide what is covered (docs/CLOUD_COST_POSTURE.md "Posture").
package posture

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"obzly/internal/synthetics/spec"
)

const (
	StatusOpen     = "open"
	StatusResolved = "resolved"

	EventOpened       = "opened"
	EventResolved     = "resolved"
	EventReopened     = "reopened"
	EventSuppressed   = "suppressed"
	EventUnsuppressed = "unsuppressed"

	// Coverage of a scope: native, fallback (the baseline where native isn't on or readable),
	// not covered, or stale (its last read is too old to trust).
	CoverageNative     = "native"
	CoverageFallback   = "fallback"
	CoverageBaseline   = "baseline"
	CoverageNotCovered = "not_covered"
	CoverageStale      = "stale"

	// staleAfter: a scope not read for this long is stale (refreshes are at most daily).
	staleAfter = 72 * time.Hour
	// alertLookback: a finding alerts once, in the evaluations within this of its opening.
	alertLookback  = 2 * time.Hour
	alertInterval  = 300
	maxReasonChars = 500
	maxNoteChars   = 1000
	maxPageSize    = 200
)

var (
	ErrForbidden    = errors.New("you can't see this project's posture")
	ErrNotFound     = errors.New("not found")
	ErrInvalidBatch = errors.New("invalid posture batch")

	// severityRank orders severities, most severe first.
	severityRank = map[string]int{
		spec.SeverityCritical: 0, spec.SeverityHigh: 1, spec.SeverityMedium: 2, spec.SeverityLow: 3,
		spec.SeverityInformational: 4,
	}
)

type Finding struct {
	ID             uuid.UUID    `gorm:"column:id;primaryKey"    json:"id"`
	ProjectID      uuid.UUID    `gorm:"column:project_id"       json:"projectId"`
	ConnectionID   uuid.UUID    `gorm:"column:connection_id"    json:"connectionId"`
	ConnectionName string       `gorm:"column:connection_name"  json:"connectionName"`
	Provider       string       `gorm:"column:provider"         json:"provider"`
	Account        string       `gorm:"column:account"          json:"account"`
	Region         string       `gorm:"column:region"           json:"region"`
	Source         string       `gorm:"column:source"           json:"source"`
	Control        string       `gorm:"column:control"          json:"control"`
	CheckID        string       `gorm:"column:check_id"         json:"check,omitempty"`
	Title          string       `gorm:"column:title"            json:"title"`
	Severity       string       `gorm:"column:severity"         json:"severity"`
	ResourceKey    string       `gorm:"column:resource_key"     json:"resourceKey"`
	ResourceType   string       `gorm:"column:resource_type"    json:"resourceType,omitempty"`
	Standards      standardRefs `gorm:"column:standards"        json:"standards"`
	Evidence       stringMap    `gorm:"column:evidence"         json:"evidence"`
	RemediationURL string       `gorm:"column:remediation_url"  json:"remediationUrl,omitempty"`
	Status         string       `gorm:"column:status"           json:"status"`
	FirstSeen      time.Time    `gorm:"column:first_seen"       json:"firstSeen"`
	LastSeen       time.Time    `gorm:"column:last_seen"        json:"lastSeen"`
	ResolvedAt     *time.Time   `gorm:"column:resolved_at"      json:"resolvedAt,omitempty"`
	Suppressed     bool         `gorm:"column:suppressed"       json:"suppressed"`
	SuppressReason string       `gorm:"column:suppress_reason"  json:"suppressReason,omitempty"`
	SuppressedTill *time.Time   `gorm:"column:suppressed_until" json:"suppressedUntil,omitempty"`
	SuppressedBy   string       `gorm:"column:suppressed_by"    json:"suppressedBy,omitempty"`
}

func (Finding) TableName() string { return "posture_findings" }

type Event struct {
	ID        int64     `gorm:"column:id;primaryKey" json:"-"`
	FindingID uuid.UUID `gorm:"column:finding_id"    json:"-"`
	ProjectID uuid.UUID `gorm:"column:project_id"    json:"-"`
	Kind      string    `gorm:"column:kind"          json:"kind"`
	Severity  string    `gorm:"column:severity"      json:"severity"`
	At        time.Time `gorm:"column:at"            json:"at"`
	ByUser    string    `gorm:"column:by_user"       json:"by,omitempty"`
	Note      string    `gorm:"column:note"          json:"note,omitempty"`
}

func (Event) TableName() string { return "posture_finding_events" }

type scopeRun struct {
	ProjectID      uuid.UUID   `gorm:"column:project_id;primaryKey"`
	ConnectionID   uuid.UUID   `gorm:"column:connection_id;primaryKey"`
	ConnectionName string      `gorm:"column:connection_name"`
	Provider       string      `gorm:"column:provider"`
	Account        string      `gorm:"column:account;primaryKey"`
	Region         string      `gorm:"column:region;primaryKey"`
	Standard       string      `gorm:"column:standard;primaryKey"`
	Source         string      `gorm:"column:source;primaryKey"`
	Result         string      `gorm:"column:result"`
	Reason         string      `gorm:"column:reason"`
	Passed         stringSlice `gorm:"column:passed"`
	Failed         stringSlice `gorm:"column:failed"`
	ScannedAt      time.Time   `gorm:"column:scanned_at"`
}

func (scopeRun) TableName() string { return "posture_scope_runs" }

type alertSettings struct {
	ProjectID   uuid.UUID `gorm:"column:project_id;primaryKey"`
	ChannelIDs  uuidList  `gorm:"column:channel_ids"`
	MinSeverity string    `gorm:"column:min_severity"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
}

func (alertSettings) TableName() string { return "posture_alert_settings" }

// Filters narrow findings; each list matches any of its values.
type Filters struct {
	Status     string   `json:"status,omitempty"`
	Severity   []string `json:"severity,omitempty"`
	Standard   []string `json:"standard,omitempty"`
	Provider   []string `json:"provider,omitempty"`
	Account    []string `json:"account,omitempty"`
	Region     []string `json:"region,omitempty"`
	Source     []string `json:"source,omitempty"`
	Connection []string `json:"connection,omitempty"`
	Control    []string `json:"control,omitempty"`
	Resource   []string `json:"resource,omitempty"`
	// Search matches the title, control or resource.
	Search string `json:"search,omitempty"`
}

type standardRefs []spec.StandardRef

func (r standardRefs) Value() (driver.Value, error) { return jsonValue(r) }
func (r *standardRefs) Scan(value any) error        { return scanJSON(value, r) }

type stringMap map[string]string

func (m stringMap) Value() (driver.Value, error) {
	if m == nil {
		return "{}", nil
	}
	return jsonValue(m)
}
func (m *stringMap) Scan(value any) error { return scanJSON(value, m) }

type stringSlice []string

func (s stringSlice) Value() (driver.Value, error) { return jsonValue(s) }
func (s *stringSlice) Scan(value any) error        { return scanJSON(value, s) }

type uuidList []uuid.UUID

func (l uuidList) Value() (driver.Value, error) { return jsonValue(l) }
func (l *uuidList) Scan(value any) error        { return scanJSON(value, l) }

func jsonValue(v any) (driver.Value, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if string(data) == "null" {
		return "[]", nil
	}
	return string(data), nil
}

func scanJSON(value any, target any) error {
	switch v := value.(type) {
	case nil:
		return nil
	case []byte:
		return json.Unmarshal(v, target)
	case string:
		return json.Unmarshal([]byte(v), target)
	}
	return fmt.Errorf("unsupported JSON column type %T", value)
}
