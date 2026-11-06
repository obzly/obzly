package metrics_core

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// MetricRepository is the metrics stream's storage abstraction. It exposes
// only primitive reads; metrics_promql adapts them to storage.Queryable, so
// metrics_core never imports the PromQL packages.
type MetricRepository interface {
	StoreSamplesBatch(entries map[uuid.UUID][]*MetricSample) error
	// The read methods take a ctx because PromQL reaches them with
	// caller-shaped matchers and no row LIMIT: the caller's deadline is the
	// only bound on a runaway query.
	SelectSeries(
		ctx context.Context, projectID uuid.UUID, request *SelectSeriesRequestDTO,
	) (*SelectSeriesResponseDTO, error)
	// EvaluateRangeFunction answers request where samples are, or returns
	// false when it can't do so exactly (the engine then reads samples).
	EvaluateRangeFunction(
		ctx context.Context, projectID uuid.UUID, request *RangeFunctionRequestDTO,
	) (*RangeFunctionResponseDTO, bool, error)
	ListMetricNames(
		ctx context.Context, projectID uuid.UUID, timeRange *TimeRangeDTO,
	) (*ListMetricNamesResponseDTO, error)
	ListLabelNames(
		ctx context.Context, projectID uuid.UUID, request *ListLabelNamesRequestDTO,
	) (*ListLabelNamesResponseDTO, error)
	ListLabelValues(
		ctx context.Context, projectID uuid.UUID, request *ListLabelValuesRequestDTO,
	) (*ListLabelValuesResponseDTO, error)
	// ListSeriesLifetimes is each matching series' labels and first/last
	// seen (the infrastructure inventory's discovery pass).
	ListSeriesLifetimes(
		ctx context.Context, projectID uuid.UUID, request *SeriesLifetimesRequestDTO,
	) ([]SeriesLifetimeDTO, error)
	GetProjectMetricStats(projectID uuid.UUID) (*MetricsStatsDTO, error)
	GetSystemMetricStats() (*MetricsStatsDTO, error)
	DeleteMetricsByProject(projectID uuid.UUID) error
	// DeleteOldMetrics retires raw samples older than rawBefore, and
	// rollups, series and label index entries older than rollupsBefore
	// (the same time when rollups don't outlive samples).
	DeleteOldMetrics(projectID uuid.UUID, rawBefore, rollupsBefore time.Time) error
	CompactSmallSegments(projectID uuid.UUID) error
	UploadPendingSegments() error
	ReconcileSegments() error
	ForceFlush() error
	TestConnection() error
}
