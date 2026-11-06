// Package system_integrity runs the integrity audit: every
// INTEGRITY_AUDIT_HOURS, one worker checks a sample of uploaded segments
// against object storage (segments.Audit) and records the result.
package system_integrity

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"time"

	"obzly/internal/cache"
	"obzly/internal/config"
	"obzly/internal/storage"
	"obzly/internal/storage/segments"
	"obzly/internal/telemetry"
	"obzly/internal/util/leaderlock"

	"github.com/google/uuid"
)

// LastAudit is the newest recorded audit.
type LastAudit struct {
	FinishedAt time.Time               `json:"finishedAt"`
	Sampled    int                     `json:"sampled"`
	Failed     int                     `json:"failed"`
	Failures   []segments.AuditFailure `json:"failures"`
}

const (
	lockName   = "integrity-audit"
	checkEvery = time.Hour
)

// Start checks hourly whether an audit is due; off without object storage
// or with INTEGRITY_AUDIT_HOURS negative.
func Start(ctx context.Context, store *segments.ObjectStore, log *slog.Logger) {
	env := config.GetEnv()
	if store == nil || env.IntegrityAuditHours < 0 {
		return
	}
	interval := time.Duration(env.IntegrityAuditHours) * time.Hour
	go func() {
		ticker := time.NewTicker(checkEvery)
		defer ticker.Stop()
		for {
			_, err := leaderlock.RunExclusive(ctx, cache.GetCache(), lockName, 2*time.Hour,
				func(ctx context.Context) error { return runIfDue(ctx, store, interval, log) })
			if err != nil {
				log.Error("integrity audit failed", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// Run audits now and records the result.
func Run(ctx context.Context, store *segments.ObjectStore, projects []uuid.UUID) (*LastAudit, error) {
	env := config.GetEnv()
	// Postgres keeps microseconds.
	started := time.Now().UTC().Truncate(time.Microsecond)
	result, err := segments.Audit(ctx, store, segments.AuditOptions{
		SamplePercent: env.IntegrityAuditPercent, MaxSamples: env.IntegrityAuditMaxSamples,
		Projects: projects, TempDir: os.TempDir(),
	})
	if err != nil {
		return nil, err
	}
	audit := &LastAudit{
		FinishedAt: time.Now().
			UTC().
			Truncate(time.Microsecond),
		Sampled:  result.Sampled,
		Failed:   len(result.Failures),
		Failures: result.Failures,
	}
	failures, err := json.Marshal(result.Failures)
	if err != nil {
		return nil, err
	}
	if err := storage.GetDb().Exec(`INSERT INTO integrity_audits (started_at, finished_at, sampled, failed, failures)
		VALUES (?, ?, ?, ?, ?)`, started, audit.FinishedAt, audit.Sampled, audit.Failed, string(failures)).Error; err != nil {
		return nil, err
	}
	report(audit)
	return audit, nil
}

// Latest is the newest recorded audit, nil before the first.
func Latest() (*LastAudit, error) {
	var row struct {
		FinishedAt time.Time
		Sampled    int
		Failed     int
		Failures   string
	}
	result := storage.GetDb().Raw(`SELECT finished_at, sampled, failed, failures::text AS failures
		FROM integrity_audits ORDER BY finished_at DESC LIMIT 1`).Scan(&row)
	if result.Error != nil || result.RowsAffected == 0 {
		return nil, result.Error
	}
	audit := &LastAudit{FinishedAt: row.FinishedAt.UTC(), Sampled: row.Sampled, Failed: row.Failed}
	if err := json.Unmarshal([]byte(row.Failures), &audit.Failures); err != nil {
		return nil, err
	}
	return audit, nil
}

func runIfDue(ctx context.Context, store *segments.ObjectStore, interval time.Duration, log *slog.Logger) error {
	last, err := Latest()
	if err != nil {
		return err
	}
	if last != nil {
		report(last)
		if time.Since(last.FinishedAt) < interval {
			return nil
		}
	}
	audit, err := Run(ctx, store, nil)
	if err != nil {
		return err
	}
	level := slog.LevelInfo
	if audit.Failed > 0 {
		level = slog.LevelError
	}
	log.Log(ctx, level, "integrity audit finished", "sampled", audit.Sampled, "failed", audit.Failed)
	return nil
}

// report keeps the metrics on the last audit, whichever replica ran it.
func report(audit *LastAudit) {
	telemetry.IntegrityAuditSampled.Set(float64(audit.Sampled))
	telemetry.IntegrityAuditFailures.Set(float64(audit.Failed))
	telemetry.IntegrityAuditLastRun.Set(float64(audit.FinishedAt.Unix()))
}
