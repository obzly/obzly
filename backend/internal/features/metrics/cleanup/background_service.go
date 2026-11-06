// Package metrics_cleanup runs the metrics stream's retention, compaction
// and reconciliation background workers.
package metrics_cleanup

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"obzly/internal/broker"
	"obzly/internal/config"
	metrics_core "obzly/internal/features/metrics/core"
	projects_models "obzly/internal/features/projects/models"
	projects_services "obzly/internal/features/projects/services"
	"obzly/internal/util/leaderlock"

	"github.com/google/uuid"
	"github.com/valkey-io/valkey-go"
)

type MetricCleanupBackgroundService struct {
	projectService *projects_services.ProjectService
	logger         *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// leaderLockClient lets one replica at a time run each pass.
	leaderLockClient valkey.Client
}

const (
	retentionCleanupInterval = 1 * time.Minute
	// compactionInterval is longer than the per-project flush so each pass
	// has a real backlog to merge.
	compactionInterval     = 5 * time.Minute
	reconciliationInterval = 10 * time.Minute
)

func (s *MetricCleanupBackgroundService) StartWorkers() {
	s.ctx, s.cancel = context.WithCancel(context.Background())

	s.logger.Info("Starting metric cleanup background workers",
		slog.Duration("retentionInterval", retentionCleanupInterval),
		slog.Duration("compactionInterval", compactionInterval),
		slog.Duration("reconciliationInterval", reconciliationInterval))

	s.wg.Add(3)
	go s.retentionWorker()
	go s.compactionWorker()
	go s.reconciliationWorker()

	s.logger.Info("Metric cleanup workers started successfully")
}

func (s *MetricCleanupBackgroundService) ExecuteAllTasksForTest() error {
	if err := s.enforceAllProjectsRetention(); err != nil {
		s.logger.Error("Error during retention cleanup in test execution", slog.String("error", err.Error()))
		return err
	}
	if err := s.compactAllProjects(nil); err != nil {
		s.logger.Error("Error during compaction in test execution", slog.String("error", err.Error()))
		return err
	}
	if err := s.reconcileSegments(); err != nil {
		s.logger.Error("Error during segment reconciliation in test execution", slog.String("error", err.Error()))
		return err
	}
	return nil
}

func (s *MetricCleanupBackgroundService) retentionWorker() {
	defer s.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("Retention worker panicked and recovered",
				slog.Any("panic", r),
				slog.String("worker", "retentionCleanup"))
		}
	}()

	ticker := time.NewTicker(retentionCleanupInterval)
	defer ticker.Stop()

	s.logger.Info("Retention cleanup worker started",
		slog.Duration("interval", retentionCleanupInterval))

	for {
		if config.IsShouldShutdown() {
			s.logger.Info("Retention cleanup worker shutting down due to shutdown signal")
			return
		}

		select {
		case <-s.ctx.Done():
			s.logger.Info("Retention cleanup worker shutting down")
			return

		case <-ticker.C:
			ran, err := leaderlock.RunExclusive(s.ctx, s.leaderLockClient, "metrics-retention-cleanup",
				retentionCleanupInterval,
				func(ctx context.Context) error { return s.enforceAllProjectsRetention() })
			if err != nil {
				s.logger.Error("Error during retention cleanup", slog.String("error", err.Error()))
			} else if !ran {
				s.logger.Debug("Skipping retention cleanup pass - another replica holds the lock")
			}
		}
	}
}

func (s *MetricCleanupBackgroundService) compactionWorker() {
	defer s.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("Compaction worker panicked and recovered",
				slog.Any("panic", r),
				slog.String("worker", "compaction"))
		}
	}()

	ticker := time.NewTicker(compactionInterval)
	defer ticker.Stop()

	s.logger.Info("Compaction worker started",
		slog.Duration("interval", compactionInterval))

	for {
		if config.IsShouldShutdown() {
			s.logger.Info("Compaction worker shutting down due to shutdown signal")
			return
		}

		select {
		case <-s.ctx.Done():
			s.logger.Info("Compaction worker shutting down")
			return

		case <-ticker.C:
			// With JetStream each worker compacts the projects whose ingest
			// shard it owns, so compaction scales with workers; otherwise
			// one replica at a time compacts everything.
			if _, sharded := broker.OwnsProject(broker.SignalMetrics, uuid.Nil); sharded {
				if err := s.compactAllProjects(ownedProject); err != nil {
					s.logger.Error("Error during compaction", slog.String("error", err.Error()))
				}
				if err := metrics_core.GetMetricCoreRepository().UploadPendingSegments(); err != nil {
					s.logger.Error("Failed to upload pending segments", slog.String("error", err.Error()))
				}
				continue
			}
			ran, err := leaderlock.RunExclusive(s.ctx, s.leaderLockClient, "metrics-compaction",
				compactionInterval,
				func(ctx context.Context) error { return s.compactAllProjects(nil) })
			if err != nil {
				s.logger.Error("Error during compaction", slog.String("error", err.Error()))
			} else if !ran {
				s.logger.Debug("Skipping compaction pass - another replica holds the lock")
			}
			// Every replica uploads its own segments, after compaction so
			// object storage mostly gets full-sized ones.
			if err := metrics_core.GetMetricCoreRepository().UploadPendingSegments(); err != nil {
				s.logger.Error("Failed to upload pending segments", slog.String("error", err.Error()))
			}
		}
	}
}

// compactAllProjects compacts every project, or only those owned accepts
// (nil: all). Not gated by any project setting: it is pure maintenance.
func (s *MetricCleanupBackgroundService) compactAllProjects(owned func(uuid.UUID) bool) error {
	projects, err := s.projectService.GetAllProjects()
	if err != nil {
		return fmt.Errorf("failed to get all projects: %w", err)
	}

	compactionFailures := 0
	processedProjects := 0

	for _, project := range projects {
		if owned != nil && !owned(project.ID) {
			continue
		}
		if err := metrics_core.GetMetricCoreRepository().CompactSmallSegments(project.ID); err != nil {
			compactionFailures++
			s.logger.Error("Failed to compact segments for project",
				slog.String("projectId", project.ID.String()),
				slog.String("error", err.Error()))
		}
		processedProjects++
	}

	s.logger.Info("Compaction completed",
		slog.Int("processedProjects", processedProjects),
		slog.Int("compactionFailures", compactionFailures))

	if compactionFailures > 0 {
		return fmt.Errorf("compaction failed for %d projects", compactionFailures)
	}

	return nil
}

func (s *MetricCleanupBackgroundService) reconciliationWorker() {
	defer s.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("Reconciliation worker panicked and recovered",
				slog.Any("panic", r),
				slog.String("worker", "reconciliation"))
		}
	}()

	ticker := time.NewTicker(reconciliationInterval)
	defer ticker.Stop()

	s.logger.Info("Reconciliation worker started",
		slog.Duration("interval", reconciliationInterval))

	for {
		if config.IsShouldShutdown() {
			s.logger.Info("Reconciliation worker shutting down due to shutdown signal")
			return
		}

		select {
		case <-s.ctx.Done():
			s.logger.Info("Reconciliation worker shutting down")
			return

		case <-ticker.C:
			ran, err := leaderlock.RunExclusive(s.ctx, s.leaderLockClient, "metrics-reconciliation",
				reconciliationInterval,
				func(ctx context.Context) error { return s.reconcileSegments() })
			if err != nil {
				s.logger.Error("Error during segment reconciliation", slog.String("error", err.Error()))
			} else if !ran {
				s.logger.Debug("Skipping reconciliation pass - another replica holds the lock")
			}
		}
	}
}

func (s *MetricCleanupBackgroundService) reconcileSegments() error {
	return metrics_core.GetMetricCoreRepository().ReconcileSegments()
}

func (s *MetricCleanupBackgroundService) enforceAllProjectsRetention() error {
	projects, err := s.projectService.GetAllProjects()
	if err != nil {
		return fmt.Errorf("failed to get all projects: %w", err)
	}

	policies, err := projects_services.GetRetentionPolicyService().AllPolicies()
	if err != nil {
		return err
	}

	cleanupFailures := 0
	processedProjects := 0

	for _, project := range projects {
		policy := projects_services.PolicyOf(policies, project.ID, projects_models.SignalMetrics)
		days := project.EffectiveRetentionDays(policy)
		if days <= 0 {
			continue
		}

		if err := s.enforceMetricRetention(project.ID, days, policy.RollupDays); err != nil {
			cleanupFailures++
			s.logger.Error("Failed to enforce metric retention for project",
				slog.String("projectId", project.ID.String()),
				slog.String("error", err.Error()))
		}
		processedProjects++
	}

	s.logger.Info("Metric retention cleanup completed",
		slog.Int("processedProjects", processedProjects),
		slog.Int("cleanupFailures", cleanupFailures))

	if cleanupFailures > 0 {
		return fmt.Errorf("metric retention cleanup failed for %d projects", cleanupFailures)
	}

	return nil
}

// enforceMetricRetention retires samples after maxLifeDays and rollups
// after rollupLifeDays when that is longer.
func (s *MetricCleanupBackgroundService) enforceMetricRetention(
	projectID uuid.UUID,
	maxLifeDays, rollupLifeDays int,
) error {
	now := time.Now().UTC()
	rawBefore := now.AddDate(0, 0, -maxLifeDays)
	rollupsBefore := now.AddDate(0, 0, -max(maxLifeDays, rollupLifeDays))

	if err := metrics_core.GetMetricCoreRepository().DeleteOldMetrics(projectID, rawBefore, rollupsBefore); err != nil {
		return fmt.Errorf("failed to delete old metrics: %w", err)
	}
	return nil
}

func ownedProject(projectID uuid.UUID) bool {
	owns, _ := broker.OwnsProject(broker.SignalMetrics, projectID)
	return owns
}
