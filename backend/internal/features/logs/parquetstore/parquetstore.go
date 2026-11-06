// Package logs_parquetstore implements logs_core.LogRepository on the Parquet
// storage engine (ingester, querier, compactor). It is a separate package
// because those import logs_core, so logs_core sees it only via the interface.
package logs_parquetstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	logs_compactor "obzly/internal/features/logs/compactor"
	logs_core "obzly/internal/features/logs/core"
	logs_ingester "obzly/internal/features/logs/ingester"
	logs_querier "obzly/internal/features/logs/querier"
	logs_uploader "obzly/internal/features/logs/uploader"
	"obzly/internal/query/partcache"
	"obzly/internal/storage"
	"obzly/internal/storage/segments"
	"obzly/internal/util/logger"

	"github.com/google/uuid"
)

// uploadBatchLimit/uploadTimeout bound one UploadPendingSegments call, so a
// large backlog uploads across passes instead of blocking the worker's tick.
const (
	uploadBatchLimit = 50
	uploadTimeout    = 5 * time.Minute
)

type LogParquetStore struct {
	dataDir     string
	stream      string
	ingester    *logs_ingester.LogIngester
	querier     *logs_querier.LogQuerier
	compactor   *logs_compactor.LogCompactor
	segmentRepo *segments.SegmentRepository
	// uploader is nil without object storage (WithObjectStore); uploads are then
	// a no-op.
	uploader *logs_uploader.LogUploader
	// rollupUploader uploads the logs stream's per-minute counts (logs_1m,
	// built by the compactor); nil for other streams.
	rollupUploader *logs_uploader.LogUploader
}

type Option func(*options)

type options struct {
	objectStore *segments.ObjectStore
	cacheDir    string
	stream      string
	tail        segments.TailSource
	memoryLimit string
	partitions  *partcache.Cache
}

// WithPartitionCache caches time-bucketed aggregations per partition
// (logs_querier.WithPartitionCache).
func WithPartitionCache(cache *partcache.Cache) Option {
	return func(o *options) {
		o.partitions = cache
	}
}

// WithQuerierMemoryLimit caps the querier's DuckDB (logs_querier.WithMemoryLimit).
func WithQuerierMemoryLimit(limit string) Option {
	return func(o *options) {
		o.memoryLimit = limit
	}
}

// WithStream stores this repository's segments under another stream name
// with the same logs schema (logs_core.StreamAILogs).
func WithStream(stream string) Option {
	return func(o *options) {
		o.stream = stream
	}
}

// WithTail makes queries include rows still pending in JetStream.
func WithTail(tail segments.TailSource) Option {
	return func(o *options) {
		o.tail = tail
	}
}

// WithObjectStore enables the cold-tier read path: a segment missing locally
// is downloaded into cacheDir on demand instead of failing the query.
func WithObjectStore(store *segments.ObjectStore, cacheDir string) Option {
	return func(o *options) {
		o.objectStore = store
		o.cacheDir = cacheDir
	}
}

func New(dataDir string, opts ...Option) (*LogParquetStore, error) {
	cfg := options{stream: logs_core.StreamLogs}
	for _, opt := range opts {
		opt(&cfg)
	}

	// Direct writes: these rows exist nowhere else, so a failed flush keeps
	// them (the JetStream DurableWriter doesn't, see durable.go).
	ingester, err := logs_ingester.NewLogIngesterForStream(dataDir, cfg.stream, segments.WithRequeueOnFlushFailure())
	if err != nil {
		return nil, fmt.Errorf("failed to create log ingester: %w", err)
	}

	compactorOpts := []logs_compactor.Option{logs_compactor.WithStream(cfg.stream)}
	if cfg.objectStore != nil {
		compactorOpts = append(compactorOpts, logs_compactor.WithObjectStore(cfg.objectStore))
	}
	compactor, err := logs_compactor.NewLogCompactor(dataDir, compactorOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create log compactor: %w", err)
	}

	querierOpts := []logs_querier.QuerierOption{
		logs_querier.WithStream(cfg.stream),
		logs_querier.WithQueryDir(filepath.Join(dataDir, "query", cfg.stream)),
	}
	if cfg.memoryLimit != "" {
		querierOpts = append(querierOpts, logs_querier.WithMemoryLimit(cfg.memoryLimit))
	}
	if cfg.tail != nil {
		querierOpts = append(querierOpts, logs_querier.WithTail(cfg.tail))
	}
	if cfg.objectStore != nil {
		querierOpts = append(querierOpts, logs_querier.WithObjectStore(cfg.objectStore, cfg.cacheDir))
	}
	if cfg.partitions != nil {
		querierOpts = append(querierOpts, logs_querier.WithPartitionCache(cfg.partitions))
	}
	querier, err := logs_querier.NewLogQuerier(querierOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create log querier: %w", err)
	}

	var uploader, rollupUploader *logs_uploader.LogUploader
	if cfg.objectStore != nil {
		uploader = logs_uploader.NewLogUploaderForStream(cfg.objectStore, cfg.stream)
		if cfg.stream == logs_core.StreamLogs {
			rollupUploader = logs_uploader.NewLogUploaderForStream(cfg.objectStore, logs_core.StreamLogsRollup)
		}
		if _, ok := segments.TokenBloomColumns[cfg.stream]; ok {
			blooms, err := segments.NewTokenBlooms(filepath.Join(dataDir, "spill", "token-bloom-"+cfg.stream))
			if err != nil {
				return nil, fmt.Errorf("failed to set up token filters: %w", err)
			}
			uploader.SetTokenBlooms(blooms)
		}
	}

	return &LogParquetStore{
		dataDir:     dataDir,
		stream:      cfg.stream,
		ingester:    ingester,
		querier:     querier,
		compactor:   compactor,
		segmentRepo: &segments.SegmentRepository{},
		uploader:    uploader,

		rollupUploader: rollupUploader,
	}, nil
}

func (s *LogParquetStore) StoreLogsBatch(entries map[uuid.UUID][]*logs_core.LogItem) error {
	return s.ingester.StoreLogsBatch(entries)
}

func (s *LogParquetStore) ExecuteQueryForProject(
	projectID uuid.UUID,
	request *logs_core.LogQueryRequestDTO,
) (*logs_core.LogQueryResponseDTO, error) {
	return s.querier.ExecuteQuery(projectID, request)
}

func (s *LogParquetStore) StreamQueryForProject(
	ctx context.Context,
	projectID uuid.UUID,
	request *logs_core.LogQueryRequestDTO,
	emit logs_core.SearchEmitter,
) (*logs_core.LogQueryResponseDTO, error) {
	return s.querier.ExecuteQueryStream(ctx, projectID, request, emit)
}

func (s *LogParquetStore) ExecuteSQLQuery(
	projectID uuid.UUID,
	request *logs_core.ExecuteSQLQueryRequestDTO,
) (*logs_core.SQLQueryResponseDTO, error) {
	return s.querier.ExecuteSQLQuery(projectID, request)
}

func (s *LogParquetStore) ExecuteScopedSQL(
	projectID uuid.UUID,
	request *logs_core.ScopedSQLRequestDTO,
) ([]*logs_core.SQLQueryResponseDTO, error) {
	return s.querier.ExecuteScopedSQL(projectID, request)
}

func (s *LogParquetStore) ExecuteAggregation(
	ctx context.Context,
	projectID uuid.UUID,
	request *logs_core.AggregationRequestDTO,
) (*logs_core.AggregationResponseDTO, error) {
	return s.querier.ExecuteAggregation(ctx, projectID, request)
}

func (s *LogParquetStore) ExecutePatterns(
	projectID uuid.UUID,
	request *logs_core.PatternsRequestDTO,
) (*logs_core.PatternsResponseDTO, error) {
	return s.querier.ExecutePatterns(projectID, request)
}

func (s *LogParquetStore) ExecuteFieldValues(
	ctx context.Context,
	projectID uuid.UUID,
	request *logs_core.FieldValuesRequestDTO,
) (*logs_core.FieldValuesResponseDTO, error) {
	return s.querier.ExecuteFieldValues(ctx, projectID, request)
}

func (s *LogParquetStore) StreamAggregation(
	ctx context.Context,
	projectID uuid.UUID,
	request *logs_core.AggregationRequestDTO,
	emit logs_core.AggregationEmitter,
) (*logs_core.AggregationResponseDTO, error) {
	return s.querier.StreamAggregation(ctx, projectID, request, emit)
}

func (s *LogParquetStore) ExecutePatternChanges(
	ctx context.Context,
	projectID uuid.UUID,
	request *logs_core.PatternChangesRequestDTO,
) (*logs_core.PatternChangesResponseDTO, error) {
	return s.querier.ExecutePatternChanges(ctx, projectID, request)
}

func (s *LogParquetStore) ExecuteDeploys(
	ctx context.Context,
	projectID uuid.UUID,
	request *logs_core.DeploysRequestDTO,
) (*logs_core.DeploysResponseDTO, error) {
	return s.querier.ExecuteDeploys(ctx, projectID, request)
}

func (s *LogParquetStore) ExecuteContext(
	ctx context.Context,
	projectID uuid.UUID,
	request *logs_core.LogContextRequestDTO,
) (*logs_core.LogContextResponseDTO, error) {
	return s.querier.ExecuteContext(ctx, projectID, request)
}

func (s *LogParquetStore) DiscoverFields(projectID uuid.UUID) ([]string, error) {
	return s.querier.DiscoverFields(projectID)
}

func (s *LogParquetStore) GetProjectLogStats(projectID uuid.UUID) (*logs_core.LogsStatsDTO, error) {
	stats, err := s.segmentRepo.AggregateStatsForProject(projectID, s.stream)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate project log stats: %w", err)
	}
	return toStatsDTO(stats), nil
}

func (s *LogParquetStore) GetSystemLogStats() (*logs_core.LogsStatsDTO, error) {
	stats, err := s.segmentRepo.AggregateStats(s.stream)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate system log stats: %w", err)
	}
	return toStatsDTO(stats), nil
}

func (s *LogParquetStore) DeleteLogsByProject(projectID uuid.UUID) error {
	_, err := s.compactor.DeleteAllForProject(projectID)
	if err != nil {
		return fmt.Errorf("failed to delete project logs: %w", err)
	}
	if s.stream == logs_core.StreamLogs {
		if err := storage.GetDb().
			Exec("DELETE FROM log_source_index WHERE project_id = ?", projectID).
			Error; err != nil {
			return fmt.Errorf("failed to delete project log sources: %w", err)
		}
	}
	return nil
}

func (s *LogParquetStore) DeleteOldLogs(projectID uuid.UUID, olderThan time.Time) error {
	_, err := s.compactor.DeleteSegmentsOlderThan(projectID, olderThan)
	if err != nil {
		return fmt.Errorf("failed to delete old logs: %w", err)
	}
	// The source index forgets sources none of the remaining logs came from.
	if s.stream == logs_core.StreamLogs {
		if err := storage.GetDb().Exec("DELETE FROM log_source_index WHERE project_id = ? AND last_seen < ?",
			projectID, olderThan).Error; err != nil {
			return fmt.Errorf("failed to prune log sources: %w", err)
		}
	}
	return nil
}

// CompactSmallSegments merges, uploads the merges at once, then builds
// per-minute counts. A merge lives only on this worker's disk and is invisible
// to queries until uploaded, so no slow step (rollup backfill downloads) may
// come between.
func (s *LogParquetStore) CompactSmallSegments(projectID uuid.UUID) error {
	result, err := s.compactor.CompactSmallSegments(projectID)
	if result != nil && result.SegmentsCreated > 0 {
		if uploadErr := s.UploadPendingSegments(); uploadErr != nil {
			logger.GetLogger().Warn("Failed to upload merged segments; the pass's upload retries them",
				"projectId", projectID, "error", uploadErr)
		}
	}
	if err != nil {
		err = fmt.Errorf("failed to compact segments: %w", err)
	}
	if rollupErr := s.compactor.RollupSegments(projectID); rollupErr != nil {
		err = errors.Join(err, fmt.Errorf("failed to build per-minute counts: %w", rollupErr))
	}
	return err
}

// UploadPendingSegments pushes pending segments to object storage (no-op
// without it). Run after compaction, so object storage mostly sees merged,
// full-sized segments.
func (s *LogParquetStore) UploadPendingSegments() error {
	if s.uploader == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), uploadTimeout)
	defer cancel()

	if _, err := s.uploader.UploadPending(ctx, uploadBatchLimit); err != nil {
		return fmt.Errorf("failed to upload pending segments: %w", err)
	}
	if s.rollupUploader != nil {
		if _, err := s.rollupUploader.UploadPending(ctx, uploadBatchLimit); err != nil {
			return fmt.Errorf("failed to upload pending log rollups: %w", err)
		}
	}
	return nil
}

// ReconcileSegments retires orphaned catalog rows, checks uploaded segments
// whose local copy is gone (see LogUploader.ReconcileSegments) and backfills
// token filters. No-op without object storage.
func (s *LogParquetStore) ReconcileSegments() error {
	if s.uploader == nil {
		return nil
	}

	// Only with object storage (s.uploader): without it nothing is ever
	// uploaded, so every row would look orphaned.
	if _, err := (&segments.SegmentRepository{}).MarkOrphansLost(s.stream, time.Now().UTC()); err != nil {
		return fmt.Errorf("failed to retire orphaned segments: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), uploadTimeout)
	defer cancel()
	var reconcileErr error
	if _, err := s.uploader.ReconcileSegments(ctx, uploadBatchLimit); err != nil {
		reconcileErr = fmt.Errorf("failed to reconcile segments: %w", err)
	}

	// Filter backfill gets its own budget, so a slow or failing reconcile can't
	// starve it.
	backfillCtx, cancelBackfill := context.WithTimeout(context.Background(), tokenBloomBackfillTimeout)
	defer cancelBackfill()
	workDir := filepath.Join(s.dataDir, "spill", "token-bloom-backfill-"+s.stream)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return errors.Join(reconcileErr, err)
	}
	done, err := s.uploader.BackfillTokenBlooms(backfillCtx, tokenBloomBackfillBatch, workDir)
	if done > 0 {
		logger.GetLogger().Info("Backfilled token filters", "stream", s.stream, "segments", done)
	}
	return errors.Join(reconcileErr, err)
}

// tokenBloomBackfillBatch: segments given a token filter per reconcile pass
// (every 10 minutes), at most; tokenBloomBackfillTimeout bounds the pass
// (a large cold file takes about a minute: download and build).
const (
	tokenBloomBackfillBatch   = 20
	tokenBloomBackfillTimeout = 5 * time.Minute
)

// ForceFlush flushes every buffered project to disk now; a segment file is
// queryable as soon as it exists.
func (s *LogParquetStore) ForceFlush() error {
	if _, err := s.ingester.FlushAll(); err != nil {
		return fmt.Errorf("failed to flush pending logs: %w", err)
	}
	return nil
}

// CleanupStaleTempFiles removes "*.parquet.tmp" files left by a crash. Call
// once at startup, before ingestion resumes.
func (s *LogParquetStore) CleanupStaleTempFiles() (int, error) {
	return s.ingester.CleanupStaleTempFiles()
}

// TestConnection verifies the hot-tier data directory is writable, the
// startup pre-flight check.
func (s *LogParquetStore) TestConnection() error {
	if err := os.MkdirAll(s.dataDir, 0o755); err != nil {
		return fmt.Errorf("logs data directory %q is not writable: %w", s.dataDir, err)
	}

	probePath := filepath.Join(s.dataDir, ".write-test")
	if err := os.WriteFile(probePath, []byte("ok"), 0o644); err != nil {
		return fmt.Errorf("logs data directory %q is not writable: %w", s.dataDir, err)
	}
	_ = os.Remove(probePath)

	return nil
}

func toStatsDTO(stats segments.StreamStats) *logs_core.LogsStatsDTO {
	return &logs_core.LogsStatsDTO{
		TotalLogs:     stats.RowCount,
		TotalSizeMB:   float64(stats.SizeBytes) / (1024 * 1024),
		RawSizeMB:     float64(stats.RawSizeBytes) / (1024 * 1024),
		OldestLogTime: stats.OldestTime,
		NewestLogTime: stats.NewestTime,
	}
}

// MigrateLegacySegment rewrites one version 1 segment with normalized names.
func (s *LogParquetStore) MigrateLegacySegment(ctx context.Context, record *segments.SegmentRecord) error {
	return s.compactor.MigrateLegacySegment(ctx, record)
}
