// Package datamigration rewrites stored data to the attrnames naming rule:
// legacy (version 1) Parquet segments and stored dashboards. Leader-locked,
// throttled and resumable - the catalog's schema_version is the state.
package datamigration

import (
	"context"
	"errors"
	"sync"
	"time"

	"obzly/internal/cache"
	dashboards_models "obzly/internal/features/dashboards/models"
	dashboards_services "obzly/internal/features/dashboards/services"
	logs_core "obzly/internal/features/logs/core"
	metrics_core "obzly/internal/features/metrics/core"
	spans_core "obzly/internal/features/spans/core"
	"obzly/internal/storage"
	"obzly/internal/storage/segments"
	"obzly/internal/util/leaderlock"
	"obzly/internal/util/logger"

	"github.com/google/uuid"
)

const (
	segmentLockName    = "data-migration-segment-names-v2"
	dashboardsLockName = "data-migration-dashboard-names-v2"
	dashboardsMarker   = "dashboard_attribute_names_v2"
	lockTTL            = 2 * time.Minute
	batchBudget        = time.Minute
	batchSize          = 20
	tickInterval       = 5 * time.Second
	pauseBetween       = 100 * time.Millisecond
)

var streams = []string{"logs", "metrics", "spans"}

type legacyMigrator interface {
	MigrateLegacySegment(ctx context.Context, record *segments.SegmentRecord) error
}

type Worker struct {
	repo   *segments.SegmentRepository
	mu     sync.Mutex
	failed map[string][]uuid.UUID
}

var worker = &Worker{repo: &segments.SegmentRepository{}, failed: map[string][]uuid.UUID{}}

func GetWorker() *Worker {
	return worker
}

// Start runs the dashboards rewrite once, then migrates segments until none
// are left. Call only on replicas that hold signal storage.
func (w *Worker) Start(ctx context.Context) {
	go func() {
		log := logger.GetLogger()
		if _, err := leaderlock.RunExclusive(
			ctx,
			cache.GetCache(),
			dashboardsLockName,
			lockTTL,
			migrateDashboards,
		); err != nil {
			log.Error("dashboard name migration failed", "error", err)
		}

		ticker := time.NewTicker(tickInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			var remaining bool
			ran, err := leaderlock.RunExclusive(ctx, cache.GetCache(), segmentLockName, lockTTL,
				func(ctx context.Context) error {
					var batchErr error
					remaining, batchErr = w.migrateBatch(ctx)
					return batchErr
				})
			if err != nil {
				log.Error("segment name migration batch failed", "error", err)
				continue
			}
			if ran && !remaining {
				log.Info("segment name migration complete")
				return
			}
		}
	}()
}

// migrateBatch migrates up to batchSize segments per stream within the
// budget. It reports whether legacy segments (other than failed ones) remain.
func (w *Worker) migrateBatch(ctx context.Context) (bool, error) {
	log := logger.GetLogger()
	deadline := time.Now().Add(batchBudget)
	remaining := false

	// Each version moves to the current one its own way: 2 by relabelling
	// (every stream), 1 by a rewrite with normalized names (below).
	for _, stream := range segments.PartitionedStreams {
		if relabelled, err := w.repo.RelabelNamed(stream); err != nil {
			return true, err
		} else if relabelled > 0 {
			log.Info("segment schema relabelled", "stream", stream, "segments", relabelled,
				"version", segments.CurrentSchemaVersion)
		}
	}

	for _, stream := range streams {
		migrator := migratorFor(stream)
		if migrator == nil {
			continue
		}
		w.mu.Lock()
		skip := append([]uuid.UUID(nil), w.failed[stream]...)
		w.mu.Unlock()

		records, err := w.repo.ListLegacy(stream, batchSize, skip)
		if err != nil {
			return true, err
		}
		if len(records) == batchSize {
			remaining = true
		}
		for _, record := range records {
			if time.Now().After(deadline) || ctx.Err() != nil {
				return true, nil
			}
			if err := migrator.MigrateLegacySegment(ctx, record); err != nil {
				w.mu.Lock()
				w.failed[stream] = append(w.failed[stream], record.ID)
				w.mu.Unlock()
				if markErr := w.repo.MarkRewriteFailed(stream, record.ID, err); markErr != nil {
					log.Warn("failed to record a rewrite failure", "segmentId", record.ID, "error", markErr)
				}
				level := "error"
				if errors.Is(err, segments.ErrSegmentUnreadable) {
					level = "warn"
				}
				log.Warn("segment name migration skipped a segment",
					"stream", stream, "segmentId", record.ID, "severity", level, "error", err)
				continue
			}
			time.Sleep(pauseBetween)
		}
		legacy, total, err := w.repo.CountBySchemaVersion(stream)
		if err == nil && legacy > int64(len(skip)) {
			remaining = true
			log.Info("segment name migration progress", "stream", stream, "remaining", legacy, "total", total)
		}
	}
	return remaining, nil
}

func migratorFor(stream string) legacyMigrator {
	var repository any
	switch stream {
	case "logs":
		repository = logs_core.GetLogCoreRepository()
	case "metrics":
		repository = metrics_core.GetMetricCoreRepository()
	case "spans":
		repository = spans_core.GetSpanCoreRepository()
	}
	migrator, _ := repository.(legacyMigrator)
	return migrator
}

// migrateDashboards rewrites every stored dashboard once.
func migrateDashboards(context.Context) error {
	db := storage.GetDb()
	var done int64
	if err := db.Table("data_migrations").Where("name = ?", dashboardsMarker).Count(&done).Error; err != nil {
		return err
	}
	if done > 0 {
		return nil
	}

	var dashboards []*dashboards_models.Dashboard
	if err := db.Find(&dashboards).Error; err != nil {
		return err
	}
	changed := 0
	for _, dashboard := range dashboards {
		if !dashboards_services.NormalizeDashboardNames(dashboard) {
			continue
		}
		if err := db.Model(dashboard).Select("tabs", "variables").Updates(dashboard).Error; err != nil {
			return err
		}
		changed++
	}
	logger.GetLogger().Info("dashboard name migration complete", "updated", changed, "total", len(dashboards))
	return db.Exec("INSERT INTO data_migrations (name) VALUES (?) ON CONFLICT DO NOTHING", dashboardsMarker).Error
}

type StreamStatus struct {
	Stream  string `json:"stream"`
	Legacy  int64  `json:"legacy"`
	Total   int64  `json:"total"`
	Skipped int    `json:"skipped"`
}

// Status reports, per stream, how many segments still use legacy names.
func (w *Worker) Status() ([]StreamStatus, error) {
	statuses := make([]StreamStatus, 0, len(streams))
	for _, stream := range streams {
		legacy, total, err := w.repo.CountBySchemaVersion(stream)
		if err != nil {
			return nil, err
		}
		// From the catalog, not this process: every replica reports it.
		skipped, err := w.repo.CountRewriteFailed(stream)
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, StreamStatus{stream, legacy, total, int(skipped)})
	}
	return statuses, nil
}
