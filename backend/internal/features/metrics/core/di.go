package metrics_core

import (
	"context"
	"errors"
	"time"

	projects_services "obzly/internal/features/projects/services"

	"github.com/google/uuid"
)

// activeRepository must only be read at call time, never captured during
// package-level var init (see logs_core/di.go).
var activeRepository MetricRepository

func SetRepository(repo MetricRepository) {
	activeRepository = repo
}

func GetMetricCoreRepository() MetricRepository {
	if activeRepository == nil {
		panic("metrics_core: MetricRepository accessed before SetRepository was called")
	}
	return activeRepository
}

func GetUnavailableMetricCoreRepository() MetricRepository {
	return unavailableRepository{}
}

var metricCoreService = &MetricCoreService{}

func SetupDependencies() {
	projects_services.GetProjectService().AddProjectDeletionListener(metricCoreService)
}

var errRepositoryUnavailable = errors.New("metrics storage is unavailable")

type unavailableRepository struct{}

func (unavailableRepository) StoreSamplesBatch(map[uuid.UUID][]*MetricSample) error {
	return errRepositoryUnavailable
}

func (unavailableRepository) SelectSeries(
	context.Context, uuid.UUID, *SelectSeriesRequestDTO,
) (*SelectSeriesResponseDTO, error) {
	return nil, errRepositoryUnavailable
}

func (unavailableRepository) EvaluateRangeFunction(
	context.Context, uuid.UUID, *RangeFunctionRequestDTO,
) (*RangeFunctionResponseDTO, bool, error) {
	return nil, false, errRepositoryUnavailable
}

func (unavailableRepository) ListMetricNames(
	context.Context, uuid.UUID, *TimeRangeDTO,
) (*ListMetricNamesResponseDTO, error) {
	return nil, errRepositoryUnavailable
}

func (unavailableRepository) ListLabelNames(
	context.Context,
	uuid.UUID,
	*ListLabelNamesRequestDTO,
) (*ListLabelNamesResponseDTO, error) {
	return nil, errRepositoryUnavailable
}

func (unavailableRepository) ListSeriesLifetimes(
	context.Context, uuid.UUID, *SeriesLifetimesRequestDTO,
) ([]SeriesLifetimeDTO, error) {
	return nil, errRepositoryUnavailable
}

func (unavailableRepository) ListLabelValues(
	context.Context,
	uuid.UUID,
	*ListLabelValuesRequestDTO,
) (*ListLabelValuesResponseDTO, error) {
	return nil, errRepositoryUnavailable
}

func (unavailableRepository) GetProjectMetricStats(uuid.UUID) (*MetricsStatsDTO, error) {
	return nil, errRepositoryUnavailable
}

func (unavailableRepository) GetSystemMetricStats() (*MetricsStatsDTO, error) {
	return nil, errRepositoryUnavailable
}

func (unavailableRepository) DeleteMetricsByProject(uuid.UUID) error {
	return errRepositoryUnavailable
}

func (unavailableRepository) DeleteOldMetrics(uuid.UUID, time.Time, time.Time) error {
	return errRepositoryUnavailable
}

func (unavailableRepository) CompactSmallSegments(uuid.UUID) error {
	return errRepositoryUnavailable
}

func (unavailableRepository) UploadPendingSegments() error {
	return errRepositoryUnavailable
}

func (unavailableRepository) ReconcileSegments() error {
	return errRepositoryUnavailable
}

func (unavailableRepository) ForceFlush() error {
	return errRepositoryUnavailable
}

func (unavailableRepository) TestConnection() error {
	return errRepositoryUnavailable
}
