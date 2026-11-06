package logs_cleanup

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"obzly/internal/broker"
	"obzly/internal/config"
	logs_core "obzly/internal/features/logs/core"
	projects_models "obzly/internal/features/projects/models"
	projects_services "obzly/internal/features/projects/services"
	"obzly/internal/util/leaderlock"

	"github.com/google/uuid"
	"github.com/valkey-io/valkey-go"
)

type LogCleanupBackgroundService struct {
	projectService *projects_services.ProjectService
	logger         *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// leaderLockClient backs a Valkey lease (util/leaderlock) taken before each
	// pass, so only one replica runs a given pass at a time.
	leaderLockClient valkey.Client
}

// leaderLockTTL is the lock's crash-safety fallback (it is released when a
// pass ends): long enough that a slow pass keeps it, short enough that a dead
// replica doesn't block takeover for long.
func leaderLockTTL(interval time.Duration) time.Duration {
	return interval
}

const (
	quotaEnforcementInterval = 5 * time.Second
	retentionCleanupInterval = 1 * time.Minute
	// compactionInterval is well above the 30s flush so each pass has a real
	// backlog of small segments to merge.
	compactionInterval = 5 * time.Minute
	// reconciliationInterval is independent: reconciliation only restores
	// missing local copies, so it has no ordering hazard with compaction/upload.
	reconciliationInterval = 10 * time.Minute
)

func (s *LogCleanupBackgroundService) StartWorkers() {
	s.ctx, s.cancel = context.WithCancel(context.Background())

	s.logger.Info("Starting log cleanup background workers",
		slog.Duration("quotaInterval", quotaEnforcementInterval),
		slog.Duration("retentionInterval", retentionCleanupInterval),
		slog.Duration("compactionInterval", compactionInterval),
		slog.Duration("reconciliationInterval", reconciliationInterval))

	s.wg.Add(4)
	go s.quotaEnforcerWorker()
	go s.retentionWorker()
	go s.compactionWorker()
	go s.reconciliationWorker()

	s.logger.Info("Log cleanup workers started successfully")
}

func (s *LogCleanupBackgroundService) ExecuteAllTasksForTest() error {
	if err := s.enforceAllProjectQuotas(); err != nil {
		s.logger.Error("Error during quota enforcement in test execution", slog.String("error", err.Error()))
		return err
	}

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

func (s *LogCleanupBackgroundService) quotaEnforcerWorker() {
	defer s.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("Quota enforcer worker panicked and recovered",
				slog.Any("panic", r),
				slog.String("worker", "quotaEnforcer"))
		}
	}()

	ticker := time.NewTicker(quotaEnforcementInterval)
	defer ticker.Stop()

	s.logger.Info("Quota enforcer worker started",
		slog.Duration("interval", quotaEnforcementInterval))

	for {
		if config.IsShouldShutdown() {
			s.logger.Info("Quota enforcer worker shutting down due to shutdown signal")
			return
		}

		select {
		case <-s.ctx.Done():
			s.logger.Info("Quota enforcer worker shutting down")
			return

		case <-ticker.C:
			ran, err := leaderlock.RunExclusive(s.ctx, s.leaderLockClient, "logs-quota-enforcement",
				leaderLockTTL(quotaEnforcementInterval),
				func(ctx context.Context) error { return s.enforceAllProjectQuotas() })
			if err != nil {
				s.logger.Error("Error during quota enforcement", slog.String("error", err.Error()))
			} else if !ran {
				s.logger.Debug("Skipping quota enforcement pass - another replica holds the lock")
			}
		}
	}
}

func (s *LogCleanupBackgroundService) retentionWorker() {
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
			ran, err := leaderlock.RunExclusive(s.ctx, s.leaderLockClient, "logs-retention-cleanup",
				leaderLockTTL(retentionCleanupInterval),
				func(ctx context.Context) error { return s.enforceAllProjectsRetention() })
			if err != nil {
				s.logger.Error("Error during retention cleanup", slog.String("error", err.Error()))
			} else if !ran {
				s.logger.Debug("Skipping retention cleanup pass - another replica holds the lock")
			}
		}
	}
}

func (s *LogCleanupBackgroundService) compactionWorker() {
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
			if _, sharded := broker.OwnsProject(broker.SignalLogs, uuid.Nil); sharded {
				if err := s.compactAllProjects(ownedProject); err != nil {
					s.logger.Error("Error during compaction", slog.String("error", err.Error()))
				}
				s.uploadPendingSegments()
				continue
			}
			ran, err := leaderlock.RunExclusive(s.ctx, s.leaderLockClient, "logs-compaction",
				leaderLockTTL(compactionInterval),
				func(ctx context.Context) error { return s.compactAllProjects(nil) })
			if err != nil {
				s.logger.Error("Error during compaction", slog.String("error", err.Error()))
			} else if !ran {
				s.logger.Debug("Skipping compaction pass - another replica holds the lock")
			}
			s.uploadPendingSegments()
		}
	}
}

// uploadPendingSegments uploads this replica's own pending segments, on every
// replica (uploads read local files). It runs after compaction so object
// storage mostly receives merged, full-sized segments. No-op without object
// storage.
func (s *LogCleanupBackgroundService) uploadPendingSegments() {
	for _, repository := range logs_core.GetAllRepositories() {
		if err := repository.UploadPendingSegments(); err != nil {
			s.logger.Error("Failed to upload pending segments", slog.String("error", err.Error()))
		}
	}
}

// compactAllProjects compacts every project, or only those owned accepts
// (nil: all). Not gated by project settings: it is pure maintenance.
func (s *LogCleanupBackgroundService) compactAllProjects(owned func(uuid.UUID) bool) error {
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
		for _, repository := range logs_core.GetAllRepositories() {
			if err := repository.CompactSmallSegments(project.ID); err != nil {
				compactionFailures++
				s.logger.Error("Failed to compact segments for project",
					slog.String("projectId", project.ID.String()),
					slog.String("error", err.Error()))
			}
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

func (s *LogCleanupBackgroundService) reconciliationWorker() {
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
			ran, err := leaderlock.RunExclusive(s.ctx, s.leaderLockClient, "logs-reconciliation",
				leaderLockTTL(reconciliationInterval),
				func(ctx context.Context) error { return s.reconcileSegments() })
			if err != nil {
				s.logger.Error("Error during segment reconciliation", slog.String("error", err.Error()))
			} else if !ran {
				s.logger.Debug("Skipping reconciliation pass - another replica holds the lock")
			}
		}
	}
}

// reconcileSegments is stream-wide, not per-project.
func (s *LogCleanupBackgroundService) reconcileSegments() error {
	for _, repository := range logs_core.GetAllRepositories() {
		if err := repository.ReconcileSegments(); err != nil {
			return err
		}
	}
	return nil
}

func (s *LogCleanupBackgroundService) enforceAllProjectQuotas() error {
	projects, err := s.projectService.GetAllProjects()
	if err != nil {
		return fmt.Errorf("failed to get all projects: %w", err)
	}

	s.logger.Info(fmt.Sprintf("Enforcing quota for %d projects", len(projects)))

	quotaViolations := 0
	processedProjects := 0

	for _, project := range projects {
		if err := s.enforceProjectQuotas(project.ID, project); err != nil {
			quotaViolations++
			s.logger.Error("Failed to enforce quotas for project",
				slog.String("projectId", project.ID.String()),
				slog.String("error", err.Error()))
		}
		processedProjects++
	}

	s.logger.Info("Quota enforcement completed",
		slog.Int("processedProjects", processedProjects),
		slog.Int("quotaViolations", quotaViolations))

	if quotaViolations > 0 {
		return fmt.Errorf("quota violations detected in %d projects", quotaViolations)
	}

	return nil
}

func (s *LogCleanupBackgroundService) enforceAllProjectsRetention() error {
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
	totalCleaned := 0

	for _, project := range projects {
		days := project.EffectiveRetentionDays(
			projects_services.PolicyOf(policies, project.ID, projects_models.SignalLogs))
		if days > 0 {
			if err := s.enforceLogRetention(project.ID, days); err != nil {
				cleanupFailures++
				s.logger.Error("Failed to enforce retention for project",
					slog.String("projectId", project.ID.String()),
					slog.String("error", err.Error()))
			} else {
				totalCleaned++
			}
		}
		processedProjects++
	}

	s.logger.Info("Retention cleanup completed",
		slog.Int("processedProjects", processedProjects),
		slog.Int("cleanupFailures", cleanupFailures),
		slog.Int("projectsCleaned", totalCleaned))

	if cleanupFailures > 0 {
		return fmt.Errorf("retention cleanup failed for %d projects", cleanupFailures)
	}

	return nil
}

func (s *LogCleanupBackgroundService) enforceProjectQuotas(
	projectID uuid.UUID,
	project *projects_models.Project,
) error {
	// Quotas are skipped when the project turns rate limiting off, but retention
	// (below) always applies. The organization's plan applies either way.
	maxLogsAmount := project.EffectiveMaxLogsAmount()
	maxLogsSizeMB := project.EffectiveMaxLogsSizeMB()
	if maxLogsAmount == 0 && maxLogsSizeMB == 0 {
		return nil
	}

	cleanupPercentage := s.calculateCleanupPercentage(maxLogsSizeMB)

	stats, err := logs_core.GetLogCoreRepository().GetProjectLogStats(projectID)
	if err != nil {
		return fmt.Errorf("failed to get project log stats: %w", err)
	}

	quotaViolated := false

	if maxLogsAmount > 0 && stats.TotalLogs > maxLogsAmount {
		logsToDelete := stats.TotalLogs - maxLogsAmount

		if logsToDelete > 0 {
			cutoffTime, err := s.calculateCutoffTimeForLogCount(projectID, logsToDelete, stats)
			if err != nil {
				s.logger.Error(
					"Failed to calculate cutoff time for count quota - skipping this pass rather than guessing",
					slog.String("projectId", projectID.String()),
					slog.String("error", err.Error()),
				)
				quotaViolated = true
			} else if err := logs_core.GetLogCoreRepository().DeleteOldLogs(projectID, cutoffTime); err != nil {
				s.logger.Error("Failed to delete old logs for count quota",
					slog.String("projectId", projectID.String()),
					slog.String("error", err.Error()))
				quotaViolated = true
			}
		}
	}

	if maxLogsSizeMB > 0 && stats.TotalSizeMB > float64(maxLogsSizeMB) {
		s.logger.Info("Project exceeds storage size quota, cleanup needed",
			slog.String("projectId", projectID.String()),
			slog.Float64("currentSizeMB", stats.TotalSizeMB),
			slog.Int("maxSizeMB", maxLogsSizeMB))

		targetSizeMB := float64(maxLogsSizeMB) * cleanupPercentage
		excessSizeMB := stats.TotalSizeMB - targetSizeMB

		if excessSizeMB > 0 {
			cutoffTime := s.calculateCutoffTimeForSize(excessSizeMB, stats)
			if err := logs_core.GetLogCoreRepository().DeleteOldLogs(projectID, cutoffTime); err != nil {
				s.logger.Error("Failed to delete old logs for size quota",
					slog.String("projectId", projectID.String()),
					slog.String("error", err.Error()))
				quotaViolated = true
			}
		}
	}

	if quotaViolated {
		return fmt.Errorf("quota enforcement failed for project %s", projectID.String())
	}

	return nil
}

func (s *LogCleanupBackgroundService) enforceLogRetention(projectID uuid.UUID, maxLifeDays int) error {
	if maxLifeDays <= 0 {
		return nil
	}

	cutoffTime := time.Now().UTC().AddDate(0, 0, -maxLifeDays)

	for _, repository := range logs_core.GetAllRepositories() {
		if err := repository.DeleteOldLogs(projectID, cutoffTime); err != nil {
			return fmt.Errorf("failed to delete old logs: %w", err)
		}
	}

	return nil
}

// calculateCutoffTimeForLogCount returns an error rather than guess a cutoff:
// DeleteOldLogs deletes everything older than it, so a fallback of time.Now()
// once wiped whole projects on a transient query error. Callers skip deletion
// and retry next pass.
func (s *LogCleanupBackgroundService) calculateCutoffTimeForLogCount(
	projectID uuid.UUID,
	logsToDelete int64,
	stats *logs_core.LogsStatsDTO,
) (time.Time, error) {
	if stats.TotalLogs == 0 {
		return time.Time{}, fmt.Errorf("no logs to compute a cutoff from for project %s", projectID)
	}

	if logsToDelete <= 0 {
		return stats.OldestLogTime, nil
	}

	if logsToDelete >= stats.TotalLogs {
		return stats.NewestLogTime, nil
	}

	queryRequest := &logs_core.LogQueryRequestDTO{
		Limit:          2,
		Offset:         int(logsToDelete - 1),
		SortBy:         "timestamp",
		SortOrder:      "asc",
		TimestampsOnly: true,
	}

	response, err := logs_core.GetLogCoreRepository().ExecuteQueryForProject(projectID, queryRequest)
	if err != nil {
		return time.Time{}, fmt.Errorf(
			"failed to query log at offset %d for project %s: %w",
			logsToDelete-1, projectID, err,
		)
	}

	if len(response.Logs) < 2 {
		if len(response.Logs) == 1 {
			return response.Logs[0].Timestamp.Add(1 * time.Nanosecond), nil
		}
		return time.Time{}, fmt.Errorf(
			"expected at least 1 log at offset %d for project %s, found 0",
			logsToDelete-1, projectID,
		)
	}

	lastLogToDelete := response.Logs[0]
	firstLogToKeep := response.Logs[1]
	timeDiff := firstLogToKeep.Timestamp.Sub(lastLogToDelete.Timestamp)
	cutoffTime := lastLogToDelete.Timestamp.Add(timeDiff / 2)

	return cutoffTime, nil
}

func (s *LogCleanupBackgroundService) calculateCutoffTimeForSize(
	sizeMBToDelete float64,
	stats *logs_core.LogsStatsDTO,
) time.Time {
	if stats.TotalSizeMB == 0 {
		return time.Now().UTC()
	}

	logLifespan := stats.NewestLogTime.Sub(stats.OldestLogTime)
	if logLifespan <= 0 {
		return time.Now().UTC().Add(-24 * time.Hour)
	}

	percentageToDelete := sizeMBToDelete / stats.TotalSizeMB
	timeToDelete := time.Duration(float64(logLifespan) * percentageToDelete)

	return stats.OldestLogTime.Add(timeToDelete)
}

func (s *LogCleanupBackgroundService) calculateCleanupPercentage(quotaSizeMB int) float64 {
	switch {
	case quotaSizeMB <= 10:
		return 0.85 // Small quotas: 85% target (more aggressive cleanup)
	case quotaSizeMB <= 100:
		return 0.90 // Medium quotas: 90% target (current behavior)
	case quotaSizeMB <= 500:
		return 0.95 // Large quotas: 95% target (gentler cleanup)
	default:
		return 0.98 // Very large quotas: 98% target (minimal deletion)
	}
}

func ownedProject(projectID uuid.UUID) bool {
	owns, _ := broker.OwnsProject(broker.SignalLogs, projectID)
	return owns
}
