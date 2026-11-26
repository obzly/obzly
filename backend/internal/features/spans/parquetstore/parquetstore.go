// Package spans_parquetstore implements spans_core.SpanRepository on the
// Parquet storage engine; it lives outside spans_core to avoid an import
// cycle.
package spans_parquetstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	spans_compactor "obzly/internal/features/spans/compactor"
	spans_core "obzly/internal/features/spans/core"
	spans_ingester "obzly/internal/features/spans/ingester"
	spans_querier "obzly/internal/features/spans/querier"
	spans_uploader "obzly/internal/features/spans/uploader"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
)

const (
	uploadBatchLimit = 50
	uploadTimeout    = 5 * time.Minute
	streamSpans      = "spans"
)

type SpanParquetStore struct {
	dataDir   string
	ingester  *spans_ingester.SpanIngester
	querier   *spans_querier.SpanQuerier
	compactor *spans_compactor.SpanCompactor
	// uploader is nil unless object storage is configured.
	uploader *spans_uploader.SpanUploader
	// rollupUploaders upload the span rollups (nil without object storage).
	rollupUploaders []*spans_uploader.SpanUploader
	segmentRepo     *segments.SegmentRepository
}

type Option func(*options)

type options struct {
	objectStore *segments.ObjectStore
	cacheDir    string
	tail        segments.TailSource
	memoryLimit string
	threads     int
}

// WithQuerierMemoryLimit caps the querier's DuckDB (spans_querier.WithMemoryLimit).
func WithQuerierMemoryLimit(limit string) Option {
	return func(o *options) {
		o.memoryLimit = limit
	}
}

// WithQuerierThreads caps the querier's DuckDB threads (spans_querier.WithThreads).
func WithQuerierThreads(threads int) Option {
	return func(o *options) {
		o.threads = threads
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

func New(dataDir string, opts ...Option) (*SpanParquetStore, error) {
	var cfg options
	for _, opt := range opts {
		opt(&cfg)
	}

	// Direct writes: these rows exist nowhere else, so a failed flush keeps
	// them (the JetStream DurableWriter doesn't, see durable.go).
	ingester, err := spans_ingester.NewSpanIngester(dataDir, segments.WithRequeueOnFlushFailure())
	if err != nil {
		return nil, fmt.Errorf("failed to create span ingester: %w", err)
	}

	var compactorOpts []spans_compactor.Option
	if cfg.objectStore != nil {
		compactorOpts = append(compactorOpts, spans_compactor.WithObjectStore(cfg.objectStore))
	}
	compactor, err := spans_compactor.NewSpanCompactor(dataDir, compactorOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create span compactor: %w", err)
	}

	querierOpts := []spans_querier.QuerierOption{
		spans_querier.WithQueryDir(filepath.Join(dataDir, "query", "spans")),
		spans_querier.WithMemoryLimit(cfg.memoryLimit),
		spans_querier.WithThreads(cfg.threads),
	}
	if cfg.objectStore != nil {
		querierOpts = append(querierOpts, spans_querier.WithObjectStore(cfg.objectStore, cfg.cacheDir))
	}
	if cfg.tail != nil {
		querierOpts = append(querierOpts, spans_querier.WithTail(cfg.tail))
	}
	querier, err := spans_querier.NewSpanQuerier(querierOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create span querier: %w", err)
	}

	var uploader *spans_uploader.SpanUploader
	var rollupUploaders []*spans_uploader.SpanUploader
	if cfg.objectStore != nil {
		uploader = spans_uploader.NewSpanUploader(cfg.objectStore)
		compactor.SetPublisher(uploader)
		for _, stream := range spans_core.SpanRollupStreams {
			rollupUploaders = append(rollupUploaders, spans_uploader.NewSpanUploaderForStream(cfg.objectStore, stream))
		}
	}

	return &SpanParquetStore{
		dataDir:         dataDir,
		ingester:        ingester,
		querier:         querier,
		compactor:       compactor,
		uploader:        uploader,
		rollupUploaders: rollupUploaders,
		segmentRepo:     &segments.SegmentRepository{},
	}, nil
}

func (s *SpanParquetStore) StoreSpansBatch(entries map[uuid.UUID][]*spans_core.Span) error {
	return s.ingester.StoreSpansBatch(entries)
}

func (s *SpanParquetStore) ListTraces(
	ctx context.Context,
	projectID uuid.UUID,
	request *spans_core.ListTracesRequestDTO,
) (*spans_core.ListTracesResponseDTO, error) {
	return s.querier.ListTraces(ctx, projectID, request)
}

func (s *SpanParquetStore) GetTraces(
	ctx context.Context,
	projectID uuid.UUID,
	traceIDs []string,
	timeRange *spans_core.TimeRangeDTO,
	scanBudget int64,
) (map[string][]spans_core.SpanDTO, error) {
	return s.querier.GetTraces(ctx, projectID, traceIDs, timeRange, scanBudget)
}

func (s *SpanParquetStore) GetTrace(
	projectID uuid.UUID,
	traceID string,
	timeRange *spans_core.TimeRangeDTO,
) (*spans_core.GetTraceResponseDTO, error) {
	return s.querier.GetTrace(projectID, traceID, timeRange)
}

func (s *SpanParquetStore) ListServices(
	ctx context.Context,
	projectID uuid.UUID,
	timeRange *spans_core.TimeRangeDTO,
	scanBudget int64,
) (*spans_core.ListServicesResponseDTO, error) {
	return s.querier.ListServices(ctx, projectID, timeRange, scanBudget)
}

func (s *SpanParquetStore) ListOperations(
	ctx context.Context,
	projectID uuid.UUID,
	serviceName string,
	timeRange *spans_core.TimeRangeDTO,
	scanBudget int64,
) (*spans_core.ListOperationsResponseDTO, error) {
	return s.querier.ListOperations(ctx, projectID, serviceName, timeRange, scanBudget)
}

func (s *SpanParquetStore) ExecuteAggregation(
	projectID uuid.UUID,
	request *spans_core.AggregationRequestDTO,
) (*spans_core.AggregationResponseDTO, error) {
	return s.querier.ExecuteAggregation(projectID, request)
}

func (s *SpanParquetStore) ServiceCallEdges(
	ctx context.Context,
	projectID uuid.UUID,
	timeRange *spans_core.TimeRangeDTO,
	scanBudget int64,
) ([]spans_core.ServiceCallEdge, error) {
	return s.querier.ServiceCallEdges(ctx, projectID, timeRange, scanBudget)
}

func (s *SpanParquetStore) ServiceK8sMetadata(
	ctx context.Context,
	projectID uuid.UUID,
	timeRange *spans_core.TimeRangeDTO,
	scanBudget int64,
) (map[string]spans_core.K8sResourceInfo, error) {
	return s.querier.ServiceK8sMetadata(ctx, projectID, timeRange, scanBudget)
}

func (s *SpanParquetStore) ServiceNodeStats(
	ctx context.Context,
	projectID uuid.UUID,
	timeRange *spans_core.TimeRangeDTO,
	scanBudget int64,
) (*spans_core.ServiceNodeStats, error) {
	return s.querier.ServiceNodeStats(ctx, projectID, timeRange, scanBudget)
}

func (s *SpanParquetStore) TraceSummary(
	ctx context.Context,
	projectID uuid.UUID,
	request *spans_core.TraceSummaryQuery,
) (*spans_core.TraceSummaryResult, error) {
	return s.querier.TraceSummary(ctx, projectID, request)
}

func (s *SpanParquetStore) ServiceErrorRates(
	ctx context.Context,
	projectID uuid.UUID,
	request *spans_core.ServiceErrorRatesQuery,
) ([]spans_core.ServiceErrorRate, error) {
	return s.querier.ServiceErrorRates(ctx, projectID, request)
}

func (s *SpanParquetStore) ServiceErrorSpanCounts(
	projectID uuid.UUID,
	timeRange *spans_core.TimeRangeDTO,
	services []string,
	spanIDs []string,
) (map[string]int64, error) {
	return s.querier.ServiceErrorSpanCounts(projectID, timeRange, services, spanIDs)
}

func (s *SpanParquetStore) QueryLLMCalls(
	ctx context.Context,
	projectID uuid.UUID,
	request *spans_core.LLMCallsQueryDTO,
) (*spans_core.LLMCallsResultDTO, error) {
	return s.querier.QueryLLMCalls(ctx, projectID, request)
}

func (s *SpanParquetStore) GetProjectSpanStats(projectID uuid.UUID) (*spans_core.SpansStatsDTO, error) {
	stats, err := s.segmentRepo.AggregateStatsForProject(projectID, streamSpans)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate project span stats: %w", err)
	}
	return toSpansStatsDTO(stats), nil
}

func (s *SpanParquetStore) GetSystemSpanStats() (*spans_core.SpansStatsDTO, error) {
	stats, err := s.segmentRepo.AggregateStats(streamSpans)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate system span stats: %w", err)
	}
	return toSpansStatsDTO(stats), nil
}

func toSpansStatsDTO(stats segments.StreamStats) *spans_core.SpansStatsDTO {
	return &spans_core.SpansStatsDTO{
		TotalSpans:          stats.RowCount,
		TotalSizeMB:         float64(stats.SizeBytes) / (1024 * 1024),
		RawSizeMB:           float64(stats.RawSizeBytes) / (1024 * 1024),
		ObjectStorageSizeMB: float64(stats.UploadedSizeBytes) / (1024 * 1024),
		OldestSpanTime:      stats.OldestTime,
		NewestSpanTime:      stats.NewestTime,
	}
}

func (s *SpanParquetStore) DeleteSpansByProject(projectID uuid.UUID) error {
	_, err := s.compactor.DeleteAllForProject(projectID)
	if err != nil {
		return fmt.Errorf("failed to delete project spans: %w", err)
	}
	return nil
}

func (s *SpanParquetStore) DeleteOldSpans(projectID uuid.UUID, olderThan time.Time) error {
	_, err := s.compactor.DeleteSegmentsOlderThan(projectID, olderThan)
	if err != nil {
		return fmt.Errorf("failed to delete old spans: %w", err)
	}
	return nil
}

// CompactSmallSegments merges (each merge is uploaded before it replaces its
// sources), then builds the span rollups.
func (s *SpanParquetStore) CompactSmallSegments(projectID uuid.UUID, sampling spans_core.SpanSampling) error {
	_, err := s.compactor.CompactSmallSegments(projectID, sampling)
	if err != nil {
		err = fmt.Errorf("failed to compact segments: %w", err)
	}
	if rollupErr := s.compactor.RollupSegments(projectID); rollupErr != nil {
		err = errors.Join(err, fmt.Errorf("failed to build span rollups: %w", rollupErr))
	}
	return err
}

// UploadPendingSegments uploads a batch of pending segments to object
// storage (see logs_parquetstore for the compact-before-upload ordering).
func (s *SpanParquetStore) UploadPendingSegments() error {
	if s.uploader == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), uploadTimeout)
	defer cancel()

	// Rollups upload even when a segment fails (see logs_parquetstore).
	var uploadErr error
	if _, err := s.uploader.UploadPending(ctx, uploadBatchLimit); err != nil {
		uploadErr = fmt.Errorf("failed to upload pending segments: %w", err)
	}
	for _, rollupUploader := range s.rollupUploaders {
		if _, err := rollupUploader.UploadPending(ctx, uploadBatchLimit); err != nil {
			uploadErr = errors.Join(uploadErr, fmt.Errorf("failed to upload pending span rollups: %w", err))
		}
	}
	return uploadErr
}

// ReconcileSegments retires orphaned catalog rows and checks uploaded
// segments against object storage.
func (s *SpanParquetStore) ReconcileSegments() error {
	if s.uploader == nil {
		return nil
	}

	// Only with object storage (s.uploader): without it nothing is ever
	// uploaded, so every row would look orphaned.
	// A lost rollup's row marks its source rolled up, so it is never rebuilt
	// and views undercount; retired, the next pass rebuilds it.
	for _, stream := range append([]string{streamSpans}, spans_core.SpanRollupStreams...) {
		if _, err := (&segments.SegmentRepository{}).MarkOrphansLost(stream, time.Now().UTC()); err != nil {
			return fmt.Errorf("failed to retire orphaned %s segments: %w", stream, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), uploadTimeout)
	defer cancel()

	if _, err := s.uploader.ReconcileSegments(ctx, uploadBatchLimit); err != nil {
		return fmt.Errorf("failed to reconcile segments: %w", err)
	}
	return nil
}

func (s *SpanParquetStore) ForceFlush() error {
	if _, err := s.ingester.FlushAll(); err != nil {
		return fmt.Errorf("failed to flush pending spans: %w", err)
	}
	return nil
}

// CleanupStaleTempFiles removes "*.parquet.tmp" files left by a crash; call
// once at startup, before ingestion resumes.
func (s *SpanParquetStore) CleanupStaleTempFiles() (int, error) {
	return s.ingester.CleanupStaleTempFiles()
}

// TestConnection checks that the data directory is writable.
func (s *SpanParquetStore) TestConnection() error {
	if err := os.MkdirAll(s.dataDir, 0o755); err != nil {
		return fmt.Errorf("spans data directory %q is not writable: %w", s.dataDir, err)
	}

	probePath := filepath.Join(s.dataDir, ".write-test")
	if err := os.WriteFile(probePath, []byte("ok"), 0o644); err != nil {
		return fmt.Errorf("spans data directory %q is not writable: %w", s.dataDir, err)
	}
	_ = os.Remove(probePath)

	return nil
}

// MigrateLegacySegment rewrites one version 1 segment with normalized names.
func (s *SpanParquetStore) MigrateLegacySegment(ctx context.Context, record *segments.SegmentRecord) error {
	return s.compactor.MigrateLegacySegment(ctx, record)
}
