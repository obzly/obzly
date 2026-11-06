package system_upgradestatus

import (
	"time"

	"obzly/internal/features/datamigration"
	system_integrity "obzly/internal/features/system/integrity"
)

// Schema states: whether the catalog schema matches the migrations this
// binary ships.
const (
	SchemaUpToDate = "up-to-date"
	// SchemaPending: this binary ships migrations the database hasn't
	// applied - startup runs them, so this only shows mid-rollout.
	SchemaPending = "pending"
	// SchemaAhead: the database was migrated by a newer binary than this
	// one - a rollback past the one-version boundary.
	SchemaAhead   = "ahead"
	SchemaUnknown = "unknown"
)

type Response struct {
	Version     string    `json:"version"`
	ServiceMode string    `json:"serviceMode"`
	CheckedAt   time.Time `json:"checkedAt"`

	Schema SchemaStatus `json:"schema"`
	// Rewrites and Segments are absent on a control plane, which holds no
	// signal data.
	Rewrites *RewriteStatus  `json:"rewrites,omitempty"`
	Segments []StreamBacklog `json:"segments,omitempty"`

	// The newest integrity audit (object storage only), nil before the first.
	IntegrityAudit *system_integrity.LastAudit `json:"integrityAudit,omitempty"`

	// The JetStream ingest buffer, when this replica is connected to one.
	IngestBuffer []IngestStreamStatus `json:"ingestBuffer,omitempty"`

	ObjectStorageEnabled bool    `json:"objectStorageEnabled"`
	Verdict              Verdict `json:"verdict"`
}

// IngestStreamStatus is one signal's acknowledged, not yet written batches.
type IngestStreamStatus struct {
	Stream   string     `json:"stream"`
	Messages uint64     `json:"messages"`
	Bytes    uint64     `json:"bytes"`
	MaxBytes int64      `json:"maxBytes"`
	OldestAt *time.Time `json:"oldestAt,omitempty"`
	// Replicas offline or behind the stream leader.
	ReplicasLagging int `json:"replicasLagging"`
}

type SchemaStatus struct {
	CurrentVersion  int64  `json:"currentVersion"`
	LatestAvailable int64  `json:"latestAvailable"`
	AppliedCount    int64  `json:"appliedCount"`
	State           string `json:"state"`
	// Releases recorded with a newer schema than this binary ships (see
	// RecordRelease): 1 is a rollout in progress or a supported rollback.
	ReleasesAhead int `json:"releasesAhead"`
	// The last five applied migrations, newest first.
	Recent []AppliedMigration `json:"recent"`
}

type AppliedMigration struct {
	Version   int64     `json:"version"`
	AppliedAt time.Time `json:"appliedAt"`
}

type RewriteStatus struct {
	// One-time application-level data migrations that have finished.
	Completed []CompletedMigration `json:"completed"`
	// Per-stream segment rewrite progress (legacy = still to rewrite).
	Streams []datamigration.StreamStatus `json:"streams"`
	// Legacy segments across all streams, minus ones the worker gave up on.
	Remaining int64 `json:"remaining"`
	// Whether this replica runs the rewrite worker. Skipped counts are
	// per-process, so they're only meaningful from a worker replica.
	WorkerOnThisReplica bool `json:"workerOnThisReplica"`
}

type CompletedMigration struct {
	Name        string    `json:"name"`
	CompletedAt time.Time `json:"completedAt"`
}

type StreamBacklog struct {
	Stream string `json:"stream"`
	Total  int64  `json:"total"`
	// Nil when object storage is off: nothing is ever uploaded then.
	PendingUpload         *int64     `json:"pendingUpload"`
	SmallSegments         int64      `json:"smallSegments"`
	OldestPendingUploadAt *time.Time `json:"oldestPendingUploadAt,omitempty"`
}

type Verdict struct {
	SafeToUpgrade bool `json:"safeToUpgrade"`
	// Blockers: wait for these before deploying another version.
	Blockers []string `json:"blockers"`
	// Warnings: worth knowing, but they don't block an upgrade.
	Warnings []string `json:"warnings"`
}
