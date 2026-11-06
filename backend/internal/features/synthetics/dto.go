package synthetics

import (
	"time"

	"github.com/google/uuid"

	"obzly/internal/synthetics/spec"
)

// CheckView is a monitor as the UI sees it: secret values never appear,
// only which placeholders have one stored.
type CheckView struct {
	ID              uuid.UUID       `json:"id"`
	Name            string          `json:"name"`
	Type            string          `json:"type"`
	Enabled         bool            `json:"enabled"`
	IntervalSeconds int             `json:"intervalSeconds"`
	DownLocations   int             `json:"downLocations"`
	Locations       []string        `json:"locations"`
	Destination     DestinationRef  `json:"destination"`
	Definition      spec.Definition `json:"definition"`
	Secrets         []SecretState   `json:"secrets"`
	Folder          string          `json:"folder"`
	ParentIDs       []uuid.UUID     `json:"parentIds"`
	Alerts          CheckAlerts     `json:"alerts"`
	Version         int             `json:"version"`
	CreatedAt       time.Time       `json:"createdAt"`
	UpdatedAt       time.Time       `json:"updatedAt"`
}

// DestinationRef is enough for the UI to query a monitor's results through
// the proxy: /sites/{siteId}/api/v1/.../{projectId}.
type DestinationRef struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	SiteID    string    `json:"siteId"`
	ProjectID string    `json:"projectId"`
}

// SecretState is one ${env:NAME} placeholder. Stored false means the
// probe's environment must provide it.
type SecretState struct {
	Name   string `json:"name"`
	Stored bool   `json:"stored"`
}

type CheckRequest struct {
	Name            string          `json:"name"`
	Enabled         bool            `json:"enabled"`
	IntervalSeconds int             `json:"intervalSeconds"`
	DownLocations   int             `json:"downLocations"`
	Locations       []string        `json:"locations"`
	DestinationID   uuid.UUID       `json:"destinationId"`
	Definition      spec.Definition `json:"definition"`
	// Secrets sets placeholder values; an empty value removes one. Names
	// left out keep what is stored.
	Secrets map[string]string `json:"secrets"`
	// Folder is a path such as "Payments/EU".
	Folder    string      `json:"folder"`
	ParentIDs []uuid.UUID `json:"parentIds"`
	// Alerts: channels from Obzly settings; none means no alerts.
	Alerts *CheckAlerts `json:"alerts"`
	// Version is the version the editor loaded; an update with an older
	// one is refused (someone saved in between).
	Version int `json:"version"`
}

type EnabledRequest struct {
	Enabled bool `json:"enabled"`
	Version int  `json:"version"`
}

// ProbeView is a probe group (one location) and its instances.
type ProbeView struct {
	*Probe
	Online  bool         `json:"online"`
	Checks  int          `json:"checks"`
	Members []MemberView `json:"members"`
}

type MemberView struct {
	ProbeMember
	// Live: heard from within the last minute, so it runs its share.
	Live bool `json:"live"`
}

type ProbeRequest struct {
	Name        string `json:"name"`
	Location    string `json:"location"`
	Description string `json:"description"`
}

// ProbeTokenResponse carries a new token, shown once.
type ProbeTokenResponse struct {
	Probe *Probe `json:"probe"`
	Token string `json:"token"`
}

type DestinationRequest struct {
	Name      string `json:"name"`
	SiteID    string `json:"siteId"`
	ProjectID string `json:"projectId"`
	OTLPURL   string `json:"otlpUrl"`
	// APIKey: required on create; empty on update keeps the stored key.
	APIKey string `json:"apiKey"`
}

type ImportRequest struct {
	YAML string `json:"yaml"`
	// Secrets are values for the file's ${env:NAME} placeholders.
	Secrets map[string]string `json:"secrets"`
}

// ImportPreview is what an import would do (or did).
type ImportPreview struct {
	Create    []string           `json:"create"`
	Update    []ImportChange     `json:"update"`
	Unchanged []string           `json:"unchanged"`
	Delete    []string           `json:"delete"`
	Secrets   []SecretState      `json:"secrets"`
	Errors    []spec.ImportError `json:"errors"`
	Applied   bool               `json:"applied"`
	// Maintenance is what happens to windows (only with a maintenance list).
	Maintenance MaintenancePreview `json:"maintenance"`
}

type ImportChange struct {
	Name    string   `json:"name"`
	Changes []string `json:"changes"`
}

type TestRequest struct {
	// CheckID lends a saved monitor's stored secrets to the test.
	CheckID   *uuid.UUID   `json:"checkId"`
	Check     CheckRequest `json:"check"`
	Locations []string     `json:"locations"`
	// FromControlPlane runs it here, for targets no probe can reach.
	FromControlPlane bool `json:"fromControlPlane"`
}

type TestRun struct {
	ID    string     `json:"id"`
	Tasks []TestTask `json:"tasks"`
}

type TestTask struct {
	ID       string          `json:"id"`
	Location string          `json:"location"`
	Probe    string          `json:"probe"`
	Status   string          `json:"status"`
	Result   *spec.RunResult `json:"result,omitempty"`
}

// Test task states.
const (
	TaskQueued   = "queued"
	TaskDone     = "done"
	TaskNoProbe  = "no_probe"
	TaskExpired  = "expired"
	TaskNotFound = "not_found"
)
