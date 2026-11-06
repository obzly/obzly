// Package inventory builds and serves the infrastructure inventory: every
// cluster, node, host, namespace, workload, pod, container and service a
// project's agents report, with health, lifecycle and a change feed
// (docs/INFRASTRUCTURE.md). Built on Sites from data already stored.
package inventory

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const (
	KindCluster         = "cluster"
	KindNode            = "node"
	KindHost            = "host"
	KindNamespace       = "namespace"
	KindWorkload        = "workload"
	KindPod             = "pod"
	KindContainer       = "container"
	KindDockerContainer = "docker_container"
	KindService         = "service"
	// From cloud connections (docs/INFRASTRUCTURE.md "Cloud connections").
	KindLoadBalancer = "load_balancer"
	KindDatabase     = "database"
	KindBucket       = "bucket"
	KindFunction     = "function"
	KindScalingGroup = "scaling_group"
)

const (
	HealthHealthy  = "healthy"
	HealthWarning  = "warning"
	HealthCritical = "critical"
	HealthInactive = "inactive"
	HealthUnknown  = "unknown"
)

const (
	StateNew          = "new"
	StateActive       = "active"
	StateNotReporting = "not_reporting"
	StateTerminated   = "terminated"
)

const (
	ChangeNew    = "new"
	ChangeState  = "state"
	ChangeHealth = "health"
)

// Asset is one inventory entry of a project, identified by Key.
type Asset struct {
	ID         uuid.UUID  `json:"id"         gorm:"column:id;primaryKey;default:gen_random_uuid()"`
	ProjectID  uuid.UUID  `json:"projectId"  gorm:"column:project_id"`
	Kind       string     `json:"kind"       gorm:"column:kind"`
	Key        string     `json:"key"        gorm:"column:key"`
	Name       string     `json:"name"       gorm:"column:name"`
	Subtype    string     `json:"subtype"    gorm:"column:subtype"`
	ParentKey  string     `json:"parentKey"  gorm:"column:parent_key"`
	NodeKey    string     `json:"nodeKey"    gorm:"column:node_key"`
	Cluster    string     `json:"cluster"    gorm:"column:cluster"`
	Namespace  string     `json:"namespace"  gorm:"column:namespace"`
	Node       string     `json:"node"       gorm:"column:node"`
	Attributes Attributes `json:"attributes" gorm:"column:attributes"`
	Sources    Strings    `json:"sources"    gorm:"column:sources"`
	FirstSeen  time.Time  `json:"firstSeen"  gorm:"column:first_seen"`
	LastSeen   time.Time  `json:"lastSeen"   gorm:"column:last_seen"`
	State      string     `json:"state"      gorm:"column:state"`
	Health     string     `json:"health"     gorm:"column:health"`
	Reasons    Reasons    `json:"reasons"    gorm:"column:reasons"`
	Metrics    Metrics    `json:"metrics"    gorm:"column:metrics"`
	UpdatedAt  time.Time  `json:"updatedAt"  gorm:"column:updated_at"`
}

func (Asset) TableName() string { return "inventory_assets" }

// Change is one entry of the change feed: an asset appeared, or its state
// or health changed.
type Change struct {
	ID        uuid.UUID `json:"id"        gorm:"column:id;primaryKey;default:gen_random_uuid()"`
	ProjectID uuid.UUID `json:"projectId" gorm:"column:project_id"`
	AssetKey  string    `json:"assetKey"  gorm:"column:asset_key"`
	AssetName string    `json:"assetName" gorm:"column:asset_name"`
	Kind      string    `json:"kind"      gorm:"column:kind"`
	Type      string    `json:"change"    gorm:"column:change"`
	From      string    `json:"from"      gorm:"column:from_value"`
	To        string    `json:"to"        gorm:"column:to_value"`
	Detail    string    `json:"detail"    gorm:"column:detail"`
	At        time.Time `json:"at"        gorm:"column:at"`
}

func (Change) TableName() string { return "inventory_changes" }

// Reason is why an asset has its health, shown next to it.
type Reason struct {
	Health string `json:"health"`
	Text   string `json:"text"`
}

// Metrics are an asset's latest values. Percentages are 0-100; CPUBasis and
// MemoryBasis say what a pod's or container's percentage is of ("limit" or
// "node").
type Metrics struct {
	CPUPercent    *float64 `json:"cpuPercent,omitempty"`
	MemoryPercent *float64 `json:"memoryPercent,omitempty"`
	DiskPercent   *float64 `json:"diskPercent,omitempty"`
	CPUBasis      string   `json:"cpuBasis,omitempty"`
	MemoryBasis   string   `json:"memoryBasis,omitempty"`
	Restarts1h    *float64 `json:"restarts1h,omitempty"`
	Restarts24h   *float64 `json:"restarts24h,omitempty"`
	ErrorShare    *float64 `json:"errorShare,omitempty"`
	Desired       *float64 `json:"desired,omitempty"`
	Available     *float64 `json:"available,omitempty"`
	Phase         string   `json:"phase,omitempty"`
	Ready         *bool    `json:"ready,omitempty"`
}

type (
	Attributes map[string]string
	Strings    []string
	Reasons    []Reason
)

func (a Attributes) Value() (driver.Value, error) {
	if a == nil {
		a = Attributes{}
	}
	return jsonValue(a)
}

func (a *Attributes) Scan(value any) error { return scanJSON(value, a) }

func (s Strings) Value() (driver.Value, error) {
	if s == nil {
		s = Strings{}
	}
	return jsonValue(s)
}

func (s *Strings) Scan(value any) error { return scanJSON(value, s) }

func (r Reasons) Value() (driver.Value, error) {
	if r == nil {
		r = Reasons{}
	}
	return jsonValue(r)
}

func (r *Reasons) Scan(value any) error { return scanJSON(value, r) }

func (m Metrics) Value() (driver.Value, error) { return jsonValue(m) }

func (m *Metrics) Scan(value any) error { return scanJSON(value, m) }

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
