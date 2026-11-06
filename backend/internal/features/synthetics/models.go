package synthetics

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"

	"obzly/internal/synthetics/spec"
)

// Probe is one running obzly-probe, known by its location label.
type Probe struct {
	ID          uuid.UUID  `json:"id"          gorm:"column:id;primaryKey"`
	Name        string     `json:"name"        gorm:"column:name"`
	Location    string     `json:"location"    gorm:"column:location"`
	Description string     `json:"description" gorm:"column:description"`
	Version     string     `json:"version"     gorm:"column:version"`
	LastSeenAt  *time.Time `json:"lastSeenAt"  gorm:"column:last_seen_at"`
	CreatedBy   *uuid.UUID `json:"-"           gorm:"column:created_by"`
	CreatedAt   time.Time  `json:"createdAt"   gorm:"column:created_at"`
	UpdatedAt   time.Time  `json:"updatedAt"   gorm:"column:updated_at"`
}

func (Probe) TableName() string { return "synthetic_probes" }

type ProbeToken struct {
	ID          uuid.UUID  `gorm:"column:id;primaryKey"`
	ProbeID     uuid.UUID  `gorm:"column:probe_id"`
	TokenHash   string     `gorm:"column:token_hash"`
	TokenPrefix string     `gorm:"column:token_prefix"`
	CreatedAt   time.Time  `gorm:"column:created_at"`
	RevokedAt   *time.Time `gorm:"column:revoked_at"`
}

func (ProbeToken) TableName() string { return "synthetic_probe_tokens" }

// Destination is a Site project that receives results, with its ingest key.
type Destination struct {
	ID              uuid.UUID  `json:"id"         gorm:"column:id;primaryKey"`
	Name            string     `json:"name"       gorm:"column:name"`
	SiteID          string     `json:"siteId"     gorm:"column:site_id"`
	ProjectID       string     `json:"projectId"  gorm:"column:project_id"`
	OTLPURL         string     `json:"otlpUrl"    gorm:"column:otlp_url"`
	APIKeyEncrypted string     `json:"-"          gorm:"column:api_key_encrypted"`
	APIKeyHint      string     `json:"apiKeyHint" gorm:"column:api_key_hint"`
	CreatedBy       *uuid.UUID `json:"-"          gorm:"column:created_by"`
	CreatedAt       time.Time  `json:"createdAt"  gorm:"column:created_at"`
	UpdatedAt       time.Time  `json:"updatedAt"  gorm:"column:updated_at"`
}

func (Destination) TableName() string { return "synthetic_destinations" }

// ProbeMember is one instance of a probe group, by the name it reports.
type ProbeMember struct {
	ProbeID    uuid.UUID `json:"-"          gorm:"column:probe_id;primaryKey"`
	Name       string    `json:"name"       gorm:"column:name;primaryKey"`
	Version    string    `json:"version"    gorm:"column:version"`
	Checks     int       `json:"checks"     gorm:"column:checks"`
	FirstSeen  time.Time `json:"firstSeen"  gorm:"column:first_seen"`
	LastSeenAt time.Time `json:"lastSeenAt" gorm:"column:last_seen_at"`
}

func (ProbeMember) TableName() string { return "synthetic_probe_members" }

// Check is a stored monitor. Secrets are its ${env:NAME} values, encrypted; they leave the
// control plane only for probes.
type Check struct {
	ID               uuid.UUID      `gorm:"column:id;primaryKey"`
	Name             string         `gorm:"column:name"`
	Type             string         `gorm:"column:type"`
	Enabled          bool           `gorm:"column:enabled"`
	IntervalSeconds  int            `gorm:"column:interval_seconds"`
	DownLocations    int            `gorm:"column:down_locations"`
	Locations        Locations      `gorm:"column:locations"`
	DestinationID    uuid.UUID      `gorm:"column:destination_id"`
	Definition       DefinitionJSON `gorm:"column:definition"`
	SecretsEncrypted string         `gorm:"column:secrets_encrypted"`
	Folder           string         `gorm:"column:folder"`
	ParentIDs        UUIDs          `gorm:"column:parent_ids"`
	Alerts           CheckAlerts    `gorm:"column:alerts"`
	Version          int            `gorm:"column:version"`
	CreatedBy        *uuid.UUID     `gorm:"column:created_by"`
	CreatedAt        time.Time      `gorm:"column:created_at"`
	UpdatedAt        time.Time      `gorm:"column:updated_at"`
}

func (Check) TableName() string { return "synthetic_checks" }

// Locations are probe location labels; empty means every location.
type Locations []string

func (l Locations) Value() (driver.Value, error) {
	if l == nil {
		l = Locations{}
	}
	return jsonValue(l)
}
func (l *Locations) Scan(value any) error { return scanJSON(value, l) }

// UUIDs is a JSON list of ids.
type UUIDs []uuid.UUID

func (u UUIDs) Value() (driver.Value, error) {
	if u == nil {
		u = UUIDs{}
	}
	return jsonValue(u)
}
func (u *UUIDs) Scan(value any) error { return scanJSON(value, u) }

// CheckAlerts is when a monitor notifies and through which shared
// channels; no channels means no alerts.
type CheckAlerts struct {
	ChannelIDs    []uuid.UUID `json:"channelIds"`
	Notify        string      `json:"notify"`
	CertDays      int         `json:"certDays"`
	RepeatSeconds int         `json:"repeatSeconds"`
}

func (a CheckAlerts) Value() (driver.Value, error) {
	if a.ChannelIDs == nil {
		a.ChannelIDs = []uuid.UUID{}
	}
	return jsonValue(a)
}
func (a *CheckAlerts) Scan(value any) error { return scanJSON(value, a) }

type DefinitionJSON spec.Definition

func (d DefinitionJSON) Value() (driver.Value, error) { return jsonValue(d) }
func (d *DefinitionJSON) Scan(value any) error        { return scanJSON(value, d) }

// monitor is c in the YAML format: parents and channels by name.
func (c *Check) monitor(destinationName string, checkNames, channelNames map[uuid.UUID]string) spec.Monitor {
	monitor := spec.Monitor{
		Name: c.Name, Enabled: c.Enabled, IntervalSeconds: c.IntervalSeconds, Locations: c.Locations,
		Destination: destinationName, DownLocations: c.DownLocations, Definition: spec.Definition(c.Definition),
		Folder: c.Folder,
	}
	for _, id := range c.ParentIDs {
		if name, ok := checkNames[id]; ok {
			monitor.Parents = append(monitor.Parents, name)
		}
	}
	if len(c.Alerts.ChannelIDs) > 0 {
		monitor.Alerts = &spec.MonitorAlerts{
			Notify: c.Alerts.Notify, CertDays: c.Alerts.CertDays, RepeatSeconds: c.Alerts.RepeatSeconds,
		}
		for _, id := range c.Alerts.ChannelIDs {
			if name, ok := channelNames[id]; ok {
				monitor.Alerts.Channels = append(monitor.Alerts.Channels, name)
			}
		}
	}
	return monitor
}

func (c *Check) runsAt(location string) bool {
	return len(c.Locations) == 0 || slices.Contains(c.Locations, location)
}

func jsonValue(v any) (driver.Value, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return string(data), nil
}

func scanJSON(value any, target any) error {
	switch typed := value.(type) {
	case []byte:
		return json.Unmarshal(typed, target)
	case string:
		return json.Unmarshal([]byte(typed), target)
	case nil:
		return nil
	default:
		return errors.New("unsupported JSON column type")
	}
}
