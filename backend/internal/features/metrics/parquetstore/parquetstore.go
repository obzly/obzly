// Package metrics_parquetstore implements metrics_core.MetricRepository on
// the Parquet storage engine; it lives outside metrics_core to avoid an
// import cycle.
package metrics_parquetstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	metrics_compactor "obzly/internal/features/metrics/compactor"
	metrics_core "obzly/internal/features/metrics/core"
	metrics_ingester "obzly/internal/features/metrics/ingester"
	metrics_querier "obzly/internal/features/metrics/querier"
	metrics_uploader "obzly/internal/features/metrics/uploader"
	"obzly/internal/storage"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
)

const (
	uploadBatchLimit = 50
	uploadTimeout    = 5 * time.Minute
	streamMetrics    = "metrics"
)

type MetricParquetStore struct {
	dataDir   string
	ingester  *metrics_ingester.MetricIngester
	querier   *metrics_querier.MetricQuerier
	compactor *metrics_compactor.MetricCompactor
	// uploader is nil unless object storage is configured.
	uploader    *metrics_uploader.MetricUploader
	segmentRepo *segments.SegmentRepository
}

type Option func(*options)

type options struct {
	objectStore *segments.ObjectStore
	cacheDir    string
	tail        segments.TailSource
	memoryLimit string
}

// WithQuerierMemoryLimit caps the querier's DuckDB (metrics_querier.WithMemoryLimit).
func WithQuerierMemoryLimit(limit string) Option {
	return func(o *options) {
		o.memoryLimit = limit
	}
}

// WithTail makes queries include rows still pending in JetStream.
func WithTail(tail segments.TailSource) Option {
	return func(o *options) {
		o.tail = tail
	}
}

func WithObjectStore(store *segments.ObjectStore, cacheDir string) Option {
	return func(o *options) {
		o.objectStore = store
		o.cacheDir = cacheDir
	}
}

func New(dataDir string, opts ...Option) (*MetricParquetStore, error) {
	var cfg options
	for _, opt := range opts {
		opt(&cfg)
	}

	// Direct writes: these rows exist nowhere else, so a failed flush keeps
	// them (the JetStream DurableWriter doesn't, see durable.go).
	ingester, err := metrics_ingester.NewMetricIngester(dataDir, segments.WithRequeueOnFlushFailure())
	if err != nil {
		return nil, fmt.Errorf("failed to create metric ingester: %w", err)
	}

	var compactorOpts []metrics_compactor.Option
	if cfg.objectStore != nil {
		compactorOpts = append(compactorOpts, metrics_compactor.WithObjectStore(cfg.objectStore))
	}
	compactor, err := metrics_compactor.NewMetricCompactor(dataDir, compactorOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create metric compactor: %w", err)
	}

	querierOpts := []metrics_querier.QuerierOption{
		metrics_querier.WithQueryDir(filepath.Join(dataDir, "query", "metrics")),
		metrics_querier.WithMemoryLimit(cfg.memoryLimit),
	}
	if cfg.objectStore != nil {
		querierOpts = append(querierOpts, metrics_querier.WithObjectStore(cfg.objectStore, cfg.cacheDir))
	}
	if cfg.tail != nil {
		querierOpts = append(querierOpts, metrics_querier.WithTail(cfg.tail))
	}
	querier, err := metrics_querier.NewMetricQuerier(querierOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create metric querier: %w", err)
	}

	var uploader *metrics_uploader.MetricUploader
	if cfg.objectStore != nil {
		uploader = metrics_uploader.NewMetricUploader(cfg.objectStore)
	}

	return &MetricParquetStore{
		dataDir:     dataDir,
		ingester:    ingester,
		querier:     querier,
		compactor:   compactor,
		uploader:    uploader,
		segmentRepo: &segments.SegmentRepository{},
	}, nil
}

func (s *MetricParquetStore) StoreSamplesBatch(entries map[uuid.UUID][]*metrics_core.MetricSample) error {
	return s.ingester.StoreSamplesBatch(entries)
}

func (s *MetricParquetStore) SelectSeries(
	ctx context.Context,
	projectID uuid.UUID,
	request *metrics_core.SelectSeriesRequestDTO,
) (*metrics_core.SelectSeriesResponseDTO, error) {
	return s.querier.SelectSeries(ctx, projectID, request)
}

func (s *MetricParquetStore) EvaluateRangeFunction(
	ctx context.Context,
	projectID uuid.UUID,
	request *metrics_core.RangeFunctionRequestDTO,
) (*metrics_core.RangeFunctionResponseDTO, bool, error) {
	return s.querier.EvaluateRangeFunction(ctx, projectID, request)
}

func (s *MetricParquetStore) ListMetricNames(
	ctx context.Context,
	projectID uuid.UUID,
	timeRange *metrics_core.TimeRangeDTO,
) (*metrics_core.ListMetricNamesResponseDTO, error) {
	return s.querier.ListMetricNames(ctx, projectID, timeRange)
}

func (s *MetricParquetStore) ListLabelNames(
	ctx context.Context,
	projectID uuid.UUID,
	request *metrics_core.ListLabelNamesRequestDTO,
) (*metrics_core.ListLabelNamesResponseDTO, error) {
	return s.querier.ListLabelNames(ctx, projectID, request)
}

func (s *MetricParquetStore) ListLabelValues(
	ctx context.Context,
	projectID uuid.UUID,
	request *metrics_core.ListLabelValuesRequestDTO,
) (*metrics_core.ListLabelValuesResponseDTO, error) {
	return s.querier.ListLabelValues(ctx, projectID, request)
}

func (s *MetricParquetStore) ListSeriesLifetimes(
	ctx context.Context,
	projectID uuid.UUID,
	request *metrics_core.SeriesLifetimesRequestDTO,
) ([]metrics_core.SeriesLifetimeDTO, error) {
	return s.querier.ListSeriesLifetimes(ctx, projectID, request)
}

func (s *MetricParquetStore) GetProjectMetricStats(projectID uuid.UUID) (*metrics_core.MetricsStatsDTO, error) {
	stats, err := s.segmentRepo.AggregateStatsForProject(projectID, streamMetrics)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate project metric stats: %w", err)
	}
	return toMetricsStatsDTO(stats), nil
}

func (s *MetricParquetStore) GetSystemMetricStats() (*metrics_core.MetricsStatsDTO, error) {
	stats, err := s.segmentRepo.AggregateStats(streamMetrics)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate system metric stats: %w", err)
	}
	return toMetricsStatsDTO(stats), nil
}

func toMetricsStatsDTO(stats segments.StreamStats) *metrics_core.MetricsStatsDTO {
	return &metrics_core.MetricsStatsDTO{
		TotalSamples:     stats.RowCount,
		TotalSizeMB:      float64(stats.SizeBytes) / (1024 * 1024),
		RawSizeMB:        float64(stats.RawSizeBytes) / (1024 * 1024),
		OldestSampleTime: stats.OldestTime,
		NewestSampleTime: stats.NewestTime,
	}
}

func (s *MetricParquetStore) DeleteMetricsByProject(projectID uuid.UUID) error {
	_, err := s.compactor.DeleteAllForProject(projectID)
	if err != nil {
		return fmt.Errorf("failed to delete project metrics: %w", err)
	}
	for _, table := range []string{"metric_series_coverage", "metric_label_index", "metric_name_index"} {
		if err := storage.GetDb().Exec("DELETE FROM "+table+" WHERE project_id = ?", projectID).Error; err != nil {
			return fmt.Errorf("failed to delete project rows from %s: %w", table, err)
		}
	}
	return nil
}

func (s *MetricParquetStore) DeleteOldMetrics(projectID uuid.UUID, rawBefore, rollupsBefore time.Time) error {
	rollupsBefore = minTime(rollupsBefore, rawBefore)
	_, err := s.compactor.AgeOut(projectID, rawBefore, rollupsBefore)
	if err != nil {
		return fmt.Errorf("failed to delete old metrics: %w", err)
	}
	// The label index forgets what no remaining sample or rollup has.
	for _, table := range []string{"metric_label_index", "metric_name_index"} {
		if err := storage.GetDb().Exec("DELETE FROM "+table+" WHERE project_id = ? AND last_seen < ?",
			projectID, rollupsBefore).Error; err != nil {
			return fmt.Errorf("failed to prune %s: %w", table, err)
		}
	}
	return nil
}

func (s *MetricParquetStore) CompactSmallSegments(projectID uuid.UUID) error {
	_, err := s.compactor.CompactSmallSegments(projectID)
	if err != nil {
		return fmt.Errorf("failed to compact segments: %w", err)
	}
	return nil
}

// UploadPendingSegments pushes pending segments to object storage. Called
// after compaction, so object storage mostly receives full-sized segments.
func (s *MetricParquetStore) UploadPendingSegments() error {
	if s.uploader == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), uploadTimeout)
	defer cancel()

	if _, err := s.uploader.UploadPending(ctx, uploadBatchLimit); err != nil {
		return fmt.Errorf("failed to upload pending segments: %w", err)
	}
	return nil
}

func (s *MetricParquetStore) ReconcileSegments() error {
	if s.uploader == nil {
		return nil
	}

	// Only with object storage (s.uploader): without it nothing is ever
	// uploaded, so every row would look orphaned.
	for _, stream := range metrics_uploader.Streams {
		if _, err := (&segments.SegmentRepository{}).MarkOrphansLost(stream, time.Now().UTC()); err != nil {
			return fmt.Errorf("failed to retire orphaned segments: %w", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), uploadTimeout)
	defer cancel()

	if _, err := s.uploader.ReconcileSegments(ctx, uploadBatchLimit); err != nil {
		return fmt.Errorf("failed to reconcile segments: %w", err)
	}
	return nil
}

func (s *MetricParquetStore) ForceFlush() error {
	if _, err := s.ingester.FlushAll(); err != nil {
		return fmt.Errorf("failed to flush pending metrics: %w", err)
	}
	return nil
}

// CleanupStaleTempFiles removes "*.parquet.tmp" files left by a crash; call
// once at startup, before ingestion resumes.
func (s *MetricParquetStore) CleanupStaleTempFiles() (int, error) {
	return s.ingester.CleanupStaleTempFiles()
}

// TestConnection checks that the data directory is writable.
func (s *MetricParquetStore) TestConnection() error {
	if err := os.MkdirAll(s.dataDir, 0o755); err != nil {
		return fmt.Errorf("metrics data directory %q is not writable: %w", s.dataDir, err)
	}

	probePath := filepath.Join(s.dataDir, ".write-test")
	if err := os.WriteFile(probePath, []byte("ok"), 0o644); err != nil {
		return fmt.Errorf("metrics data directory %q is not writable: %w", s.dataDir, err)
	}
	_ = os.Remove(probePath)

	return nil
}

// MigrateLegacySegment rewrites one version 1 segment with normalized names.
func (s *MetricParquetStore) MigrateLegacySegment(ctx context.Context, record *segments.SegmentRecord) error {
	return s.compactor.MigrateLegacySegment(ctx, record)
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
