package ingestion

import "time"

// Health states of a source and of each of its signals.
const (
	HealthSending = "sending"
	HealthQuiet   = "quiet"
	HealthStopped = "stopped"
)

// IngestionStatusDTO is each source sending to a project.
type IngestionStatusDTO struct {
	Sources []SourceStatusDTO `json:"sources"`
	// Truncated: more sources than the response holds; the quietest are left out.
	Truncated   bool      `json:"truncated"`
	GeneratedAt time.Time `json:"generatedAt"`
	// QuietAfterMinutes: a signal with nothing for this long is quiet.
	QuietAfterMinutes int `json:"quietAfterMinutes"`
	// StoppedAfterHours: a signal with nothing for this long is stopped.
	StoppedAfterHours int `json:"stoppedAfterHours"`
	// RateWindowMinutes: PerMinute averages over this window.
	RateWindowMinutes int `json:"rateWindowMinutes"`
	// Refused: records refused in the last RefusedWindowHours, most first.
	Refused            []RefusedDTO `json:"refused"`
	RefusedWindowHours int          `json:"refusedWindowHours"`
}

// RefusedDTO is what receivers refused for one signal and reason. Counts are
// per attempt: a batch refused and retried counts each time.
type RefusedDTO struct {
	Signal string `json:"signal"`
	// Reason: an error code such as RATE_LIMIT_EXCEEDED, LOG_TOO_LARGE or OTHER.
	Reason string    `json:"reason"`
	Count  int64     `json:"count"`
	LastAt time.Time `json:"lastAt"`
	// Retried: the sender was told to retry (429/503), so the data is late
	// rather than lost. Otherwise the sender dropped it.
	Retried bool `json:"retried"`
}

type SourceStatusDTO struct {
	// ID is stable for the same source.
	ID   string `json:"id"`
	Name string `json:"name"`
	// IdentifiedBy: obzly_source, k8s_workload, service, k8s_cluster, host or unidentified.
	IdentifiedBy string `json:"identifiedBy"`
	// DataSource: the Data Sources catalog id it most likely is, "" when unknown.
	DataSource string            `json:"dataSource,omitempty"`
	Location   SourceLocationDTO `json:"location"`
	Health     string            `json:"health"`
	FirstSeen  time.Time         `json:"firstSeen"`
	LastSeen   time.Time         `json:"lastSeen"`
	Signals    []SignalStatusDTO `json:"signals"`
}

// SourceLocationDTO is where a source runs; empty fields are unknown.
type SourceLocationDTO struct {
	Host             string `json:"host,omitempty"`
	Cluster          string `json:"cluster,omitempty"`
	Namespace        string `json:"namespace,omitempty"`
	WorkloadKind     string `json:"workloadKind,omitempty"`
	Workload         string `json:"workload,omitempty"`
	ServiceName      string `json:"serviceName,omitempty"`
	ServiceNamespace string `json:"serviceNamespace,omitempty"`
	SDKLanguage      string `json:"sdkLanguage,omitempty"`
}

type SignalStatusDTO struct {
	// Signal: logs, traces or metrics.
	Signal    string    `json:"signal"`
	Health    string    `json:"health"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
	// RecentCount: records (logs, spans, metric data points) accepted in the rate window.
	RecentCount int64 `json:"recentCount"`
	// PerMinute: RecentCount per minute over the window, or since first seen when later.
	PerMinute float64 `json:"perMinute"`
}
