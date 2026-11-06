package system_upgradestatus

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"obzly/internal/broker"
	"obzly/internal/config"
	"obzly/internal/features/datamigration"
	system_integrity "obzly/internal/features/system/integrity"
	"obzly/internal/storage"
	"obzly/internal/storage/segments"
)

// Same threshold every stream's compactor uses for "small enough to merge".
const smallSegmentThresholdBytes = segments.DefaultMaxBytes / 4

const recentMigrations = 5

// A buffer whose oldest batch waited this long means workers are behind.
const ingestBacklogWarnAge = 15 * time.Minute

var streams = []string{"logs", "spans", "metrics"}

type Service struct {
	segmentRepository *segments.SegmentRepository
}

func (s *Service) GetStatus() (*Response, error) {
	env := config.GetEnv()
	response := &Response{
		Version:              env.AppVersion,
		ServiceMode:          string(env.ServiceMode),
		CheckedAt:            time.Now().UTC(),
		ObjectStorageEnabled: env.ObjectStorageURL != "",
	}

	schema, err := s.schemaStatus(env.BackendRootPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read schema status: %w", err)
	}
	response.Schema = schema

	if env.ServiceMode.RunsSignalStorage() {
		rewrites, err := s.rewriteStatus(env.ServiceMode.RunsBackgroundWorkers())
		if err != nil {
			return nil, fmt.Errorf("failed to read rewrite status: %w", err)
		}
		response.Rewrites = rewrites

		backlog, err := s.segmentBacklog(response.ObjectStorageEnabled)
		if err != nil {
			return nil, fmt.Errorf("failed to read segment backlog: %w", err)
		}
		response.Segments = backlog
	}
	response.IngestBuffer = ingestBuffer()
	if response.ObjectStorageEnabled && env.ServiceMode.RunsSignalStorage() {
		if audit, err := system_integrity.Latest(); err == nil {
			response.IntegrityAudit = audit
		}
	}

	response.Verdict = BuildVerdict(response)
	return response, nil
}

// BuildVerdict decides whether deploying another version now would leave background
// work from this one unfinished.
func BuildVerdict(r *Response) Verdict {
	verdict := Verdict{Blockers: []string{}, Warnings: []string{}}

	switch r.Schema.State {
	case SchemaPending:
		verdict.Blockers = append(verdict.Blockers, fmt.Sprintf(
			"Database schema is behind this version (%d of %d applied) - migrations still running",
			r.Schema.CurrentVersion, r.Schema.LatestAvailable))
	case SchemaAhead:
		if r.Schema.ReleasesAhead == 1 {
			verdict.Warnings = append(verdict.Warnings, fmt.Sprintf(
				"Database schema (%d) is one release newer than this version ships (%d) - "+
					"a rollout in progress or a supported rollback",
				r.Schema.CurrentVersion, r.Schema.LatestAvailable))
		} else {
			verdict.Blockers = append(verdict.Blockers, fmt.Sprintf(
				"Database schema (%d) is newer than this version ships (%d) - "+
					"this is a rollback past the supported one-version boundary",
				r.Schema.CurrentVersion, r.Schema.LatestAvailable))
		}
	case SchemaUnknown:
		verdict.Warnings = append(verdict.Warnings,
			"Couldn't compare the database schema with this version's migrations")
	}

	if r.Rewrites != nil {
		if r.Rewrites.Remaining > 0 {
			verdict.Blockers = append(verdict.Blockers, fmt.Sprintf(
				"%d segments still being rewritten to the current format", r.Rewrites.Remaining))
		}
		var skipped int
		for _, stream := range r.Rewrites.Streams {
			skipped += stream.Skipped
		}
		if skipped > 0 {
			verdict.Warnings = append(verdict.Warnings, fmt.Sprintf(
				"%d segments couldn't be rewritten (unreadable) and were skipped - check the worker logs", skipped))
		}
	}

	if r.ObjectStorageEnabled {
		var pending int64
		for _, backlog := range r.Segments {
			if backlog.PendingUpload != nil {
				pending += *backlog.PendingUpload
			}
		}
		if pending > 0 {
			verdict.Blockers = append(verdict.Blockers, fmt.Sprintf(
				"%d segments not yet uploaded to object storage - they only exist on this replica's disk",
				pending))
		}
	}

	if audit := r.IntegrityAudit; audit != nil && audit.Failed > 0 {
		verdict.Warnings = append(verdict.Warnings, fmt.Sprintf(
			"The last integrity audit (%s) found %d of %d sampled segments missing, resized or unreadable - "+
				"run `obzly reconcile` (docs/runbooks/backup-restore.md)",
			audit.FinishedAt.Format("2006-01-02"), audit.Failed, audit.Sampled))
	}

	for _, stream := range r.IngestBuffer {
		if stream.OldestAt != nil && r.CheckedAt.Sub(*stream.OldestAt) > ingestBacklogWarnAge {
			verdict.Warnings = append(verdict.Warnings, fmt.Sprintf(
				"The %s ingest buffer's oldest batch has waited %s - workers are behind (it's durable; "+
					"an upgrade loses nothing, but restarting workers adds to the wait)",
				stream.Stream, r.CheckedAt.Sub(*stream.OldestAt).Round(time.Minute)))
		}
		if stream.ReplicasLagging > 0 {
			verdict.Warnings = append(verdict.Warnings, fmt.Sprintf(
				"%d %s broker replica(s) are offline or behind - roll brokers one at a time",
				stream.ReplicasLagging, stream.Stream))
		}
	}

	verdict.SafeToUpgrade = len(verdict.Blockers) == 0
	return verdict
}

// RecordRelease notes that this release's schema is applied - called after
// startup migrations - so older binaries can count the releases ahead of
// them. Best effort: Upgrade status falls back to treating any newer
// schema as a blocker.
func RecordRelease() error {
	env := config.GetEnv()
	latest, err := latestShippedMigration(filepath.Join(env.BackendRootPath, "migrations"))
	if err != nil {
		return err
	}
	return storage.GetDb().Exec(`INSERT INTO schema_releases (release, goose_version) VALUES (?, ?)
		ON CONFLICT (release) DO UPDATE SET goose_version = EXCLUDED.goose_version, recorded_at = NOW()`,
		env.AppVersion, latest).Error
}

func ingestBuffer() []IngestStreamStatus {
	client := broker.Default()
	if client == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := []IngestStreamStatus{}
	for _, ingest := range broker.IngestStreams {
		stream, err := client.JetStream.Stream(ctx, ingest.Name)
		if err != nil {
			continue
		}
		info, err := stream.Info(ctx)
		if err != nil {
			continue
		}
		status := IngestStreamStatus{
			Stream: ingest.Name, Messages: info.State.Msgs, Bytes: info.State.Bytes, MaxBytes: info.Config.MaxBytes,
		}
		if info.State.Msgs > 0 && !info.State.FirstTime.IsZero() {
			oldest := info.State.FirstTime.UTC()
			status.OldestAt = &oldest
		}
		if info.Cluster != nil {
			for _, replica := range info.Cluster.Replicas {
				if replica.Offline || !replica.Current {
					status.ReplicasLagging++
				}
			}
		}
		result = append(result, status)
	}
	return result
}

func (s *Service) schemaStatus(backendRoot string) (SchemaStatus, error) {
	status := SchemaStatus{Recent: []AppliedMigration{}, State: SchemaUnknown}

	var applied []AppliedMigration
	err := storage.GetDb().Raw(`
		SELECT version_id AS version, tstamp AS applied_at
		FROM goose_db_version
		WHERE is_applied AND version_id > 0
		ORDER BY version_id DESC`).Scan(&applied).Error
	if err != nil {
		return status, err
	}
	status.AppliedCount = int64(len(applied))
	if len(applied) > 0 {
		status.CurrentVersion = applied[0].Version
		status.Recent = applied[:min(recentMigrations, len(applied))]
	}

	latest, err := latestShippedMigration(filepath.Join(backendRoot, "migrations"))
	if err != nil {
		// Not fatal: the verdict just can't compare. Happens when the
		// migrations directory isn't next to the binary.
		return status, nil
	}
	status.LatestAvailable = latest
	var ahead int64
	if err := storage.GetDb().Raw("SELECT COUNT(DISTINCT goose_version) FROM schema_releases WHERE goose_version > ?",
		latest).Scan(&ahead).Error; err == nil {
		status.ReleasesAhead = int(ahead)
	}

	switch {
	case status.CurrentVersion == latest:
		status.State = SchemaUpToDate
	case status.CurrentVersion < latest:
		status.State = SchemaPending
	default:
		status.State = SchemaAhead
	}
	return status, nil
}

func (s *Service) rewriteStatus(workerHere bool) (*RewriteStatus, error) {
	status := &RewriteStatus{Completed: []CompletedMigration{}, WorkerOnThisReplica: workerHere}

	err := storage.GetDb().Raw(`
		SELECT name, completed_at FROM data_migrations ORDER BY completed_at DESC`).Scan(&status.Completed).Error
	if err != nil {
		return nil, err
	}

	streams, err := datamigration.GetWorker().Status()
	if err != nil {
		return nil, err
	}
	status.Streams = streams
	for _, stream := range streams {
		remaining := stream.Legacy - int64(stream.Skipped)
		if remaining > 0 {
			status.Remaining += remaining
		}
	}
	return status, nil
}

func (s *Service) segmentBacklog(objectStorageEnabled bool) ([]StreamBacklog, error) {
	result := make([]StreamBacklog, 0, len(streams))
	for _, stream := range streams {
		counts, err := s.segmentRepository.CountBacklog(stream, smallSegmentThresholdBytes)
		if err != nil {
			return nil, err
		}
		backlog := StreamBacklog{
			Stream:        stream,
			Total:         counts.Total,
			SmallSegments: counts.SmallSegments,
		}
		if objectStorageEnabled {
			pending := counts.PendingUpload
			backlog.PendingUpload = &pending
			backlog.OldestPendingUploadAt = counts.OldestPendingUploadAt
		}
		result = append(result, backlog)
	}
	return result, nil
}

// latestShippedMigration is the highest goose version in dir - the schema
// this binary expects.
func latestShippedMigration(dir string) (int64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	var latest int64
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		prefix, _, found := strings.Cut(name, "_")
		if !found {
			continue
		}
		version, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil {
			continue
		}
		if version > latest {
			latest = version
		}
	}
	if latest == 0 {
		return 0, fmt.Errorf("no migrations found in %s", dir)
	}
	return latest, nil
}
