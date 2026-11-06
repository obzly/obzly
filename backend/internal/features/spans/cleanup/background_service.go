// Package spans_cleanup runs the spans stream's retention, compaction and
// reconciliation workers (no quota enforcement, unlike logs_cleanup).
package spans_cleanup

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"obzly/internal/broker"
	"obzly/internal/config"
	projects_models "obzly/internal/features/projects/models"
	projects_services "obzly/internal/features/projects/services"
	spans_core "obzly/internal/features/spans/core"
	"obzly/internal/util/leaderlock"

	"github.com/google/uuid"
	"github.com/valkey-io/valkey-go"
)

type SpanCleanupBackgroundService struct {
	projectService *projects_services.ProjectService
	logger         *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// leaderLockClient elects one replica per pass for exclusive work.
	leaderLockClient valkey.Client
}

const (
	retentionCleanupInterval = 1 * time.Minute
	// compactionInterval is longer than the per-project flush so each pass
	// has a real backlog to merge.
	compactionInterval     = 5 * time.Minute
	reconciliationInterval = 10 * time.Minute
)

func (s *SpanCleanupBackgroundService) StartWorkers() {
	s.ctx, s.cancel = context.WithCancel(context.Background())

	s.logger.Info("Starting span cleanup background workers",
		slog.Duration("retentionInterval", retentionCleanupInterval),
		slog.Duration("compactionInterval", compactionInterval),
		slog.Duration("reconciliationInterval", reconciliationInterval))

	s.wg.Add(3)
	go s.retentionWorker()
	go s.compactionWorker()
	go s.reconciliationWorker()

	s.logger.Info("Span cleanup workers started successfully")
}

func (s *SpanCleanupBackgroundService) ExecuteAllTasksForTest() error {
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

func (s *SpanCleanupBackgroundService) retentionWorker() {
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
			ran, err := leaderlock.RunExclusive(s.ctx, s.leaderLockClient, "spans-retention-cleanup",
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

func (s *SpanCleanupBackgroundService) compactionWorker() {
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
			if _, sharded := broker.OwnsProject(broker.SignalSpans, uuid.Nil); sharded {
				if err := s.compactAllProjects(ownedProject); err != nil {
					s.logger.Error("Error during compaction", slog.String("error", err.Error()))
				}
				if err := spans_core.GetSpanCoreRepository().UploadPendingSegments(); err != nil {
					s.logger.Error("Failed to upload pending segments", slog.String("error", err.Error()))
				}
				continue
			}
			ran, err := leaderlock.RunExclusive(s.ctx, s.leaderLockClient, "spans-compaction",
				compactionInterval,
				func(ctx context.Context) error { return s.compactAllProjects(nil) })
			if err != nil {
				s.logger.Error("Error during compaction", slog.String("error", err.Error()))
			} else if !ran {
				s.logger.Debug("Skipping compaction pass - another replica holds the lock")
			}
			// Every replica uploads its own segments, after the compaction attempt.
			if err := spans_core.GetSpanCoreRepository().UploadPendingSegments(); err != nil {
				s.logger.Error("Failed to upload pending segments", slog.String("error", err.Error()))
			}
		}
	}
}

// compactAllProjects compacts every project, or only those owned accepts
// (nil: all).
func (s *SpanCleanupBackgroundService) compactAllProjects(owned func(uuid.UUID) bool) error {
	projects, err := s.projectService.GetAllProjects()
	if err != nil {
		return fmt.Errorf("failed to get all projects: %w", err)
	}

	policies, err := projects_services.GetRetentionPolicyService().AllPolicies()
	if err != nil {
		return err
	}

	compactionFailures := 0
	processedProjects := 0

	for _, project := range projects {
		if owned != nil && !owned(project.ID) {
			continue
		}
		policy := projects_services.PolicyOf(policies, project.ID, projects_models.SignalTraces)
		sampling := spans_core.SpanSampling{Rate: policy.SampleRate, SlowMs: policy.SlowMs}
		if err := spans_core.GetSpanCoreRepository().CompactSmallSegments(project.ID, sampling); err != nil {
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

func (s *SpanCleanupBackgroundService) reconciliationWorker() {
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
			ran, err := leaderlock.RunExclusive(s.ctx, s.leaderLockClient, "spans-reconciliation",
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

func (s *SpanCleanupBackgroundService) reconcileSegments() error {
	return spans_core.GetSpanCoreRepository().ReconcileSegments()
}

func (s *SpanCleanupBackgroundService) enforceAllProjectsRetention() error {
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
		days := project.EffectiveRetentionDays(
			projects_services.PolicyOf(policies, project.ID, projects_models.SignalTraces))
		if days <= 0 {
			continue
		}

		if err := s.enforceSpanRetention(project.ID, days); err != nil {
			cleanupFailures++
			s.logger.Error("Failed to enforce span retention for project",
				slog.String("projectId", project.ID.String()),
				slog.String("error", err.Error()))
		}
		processedProjects++
	}

	s.logger.Info("Span retention cleanup completed",
		slog.Int("processedProjects", processedProjects),
		slog.Int("cleanupFailures", cleanupFailures))

	if cleanupFailures > 0 {
		return fmt.Errorf("span retention cleanup failed for %d projects", cleanupFailures)
	}

	return nil
}

func (s *SpanCleanupBackgroundService) enforceSpanRetention(projectID uuid.UUID, maxLifeDays int) error {
	cutoffTime := time.Now().UTC().AddDate(0, 0, -maxLifeDays)

	if err := spans_core.GetSpanCoreRepository().DeleteOldSpans(projectID, cutoffTime); err != nil {
		return fmt.Errorf("failed to delete old spans: %w", err)
	}
	return nil
}

func ownedProject(projectID uuid.UUID) bool {
	owns, _ := broker.OwnsProject(broker.SignalSpans, projectID)
	return owns
}
