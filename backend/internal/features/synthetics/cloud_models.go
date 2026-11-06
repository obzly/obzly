package synthetics

import (
	"database/sql/driver"
	"time"

	"github.com/google/uuid"

	"obzly/internal/synthetics/spec"
)

// CloudConnection is read-only access to an AWS account or Organization, or an Azure subscription
// or management group, scanned by a probe group. Only identifiers are stored, never keys.
type CloudConnection struct {
	ID       uuid.UUID `gorm:"column:id;primaryKey"`
	Name     string    `gorm:"column:name"`
	Provider string    `gorm:"column:provider"`
	Scope    string    `gorm:"column:scope"`
	ScopeID  string    `gorm:"column:scope_id"`
	Access   string    `gorm:"column:access"`
	// Identity: the role ARN (AWS) or client ID (Azure).
	Identity        string     `gorm:"column:identity"`
	ExternalID      string     `gorm:"column:external_id"`
	TenantID        string     `gorm:"column:tenant_id"`
	ProbeID         uuid.UUID  `gorm:"column:probe_id"`
	DestinationID   uuid.UUID  `gorm:"column:destination_id"`
	ScanTypes       Locations  `gorm:"column:scan_types"`
	Regions         Locations  `gorm:"column:regions"`
	ResourceTypes   Locations  `gorm:"column:resource_types"`
	IntervalSeconds int        `gorm:"column:interval_seconds"`
	Enabled         bool       `gorm:"column:enabled"`
	Status          CloudState `gorm:"column:status"`
	Version         int        `gorm:"column:version"`
	CreatedBy       *uuid.UUID `gorm:"column:created_by"`
	CreatedAt       time.Time  `gorm:"column:created_at"`
	UpdatedAt       time.Time  `gorm:"column:updated_at"`
	// HealthIntervalSeconds between health scans of what discovery found.
	HealthIntervalSeconds int `gorm:"column:health_interval_seconds"`
}

func (CloudConnection) TableName() string { return "cloud_connections" }

// CloudState is the latest discovery and health scan a probe reported,
// with when the last report arrived.
type CloudState struct {
	LastScan   *spec.CloudScan `json:"scan,omitempty"`
	LastHealth *spec.CloudScan `json:"health,omitempty"`
	ReportedAt *time.Time      `json:"reportedAt,omitempty"`
}

func (c CloudState) Value() (driver.Value, error) { return jsonValue(c) }
func (c *CloudState) Scan(value any) error        { return scanJSON(value, c) }

func (c *CloudConnection) wire() spec.CloudConnection {
	return spec.CloudConnection{
		ID: c.ID.String(), Name: c.Name, Provider: c.Provider, Scope: c.Scope, ScopeID: c.ScopeID,
		Access: c.Access, Identity: c.Identity, ExternalID: c.ExternalID, TenantID: c.TenantID,
		Uses: append([]string{}, c.ScanTypes...), Regions: append([]string{}, c.Regions...),
		ResourceTypes:   append([]string{}, c.ResourceTypes...),
		IntervalSeconds: c.IntervalSeconds, HealthIntervalSeconds: c.HealthIntervalSeconds,
		DestinationID: c.DestinationID.String(),
	}
}
