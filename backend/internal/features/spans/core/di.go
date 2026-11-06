package spans_core

import (
	"context"
	"errors"
	"time"

	projects_services "obzly/internal/features/projects/services"

	"github.com/google/uuid"
)

// activeRepository must only be read at call time, never captured during
// package-level var init (see logs_core/di.go).
var activeRepository SpanRepository

func SetRepository(repo SpanRepository) {
	activeRepository = repo
}

func GetSpanCoreRepository() SpanRepository {
	if activeRepository == nil {
		panic("spans_core: SpanRepository accessed before SetRepository was called")
	}
	return activeRepository
}

func GetUnavailableSpanCoreRepository() SpanRepository {
	return unavailableRepository{}
}

var spanCoreService = &SpanCoreService{}

func SetupDependencies() {
	projects_services.GetProjectService().AddProjectDeletionListener(spanCoreService)
}

var errRepositoryUnavailable = errors.New("spans storage is unavailable")

type unavailableRepository struct{}

func (unavailableRepository) StoreSpansBatch(map[uuid.UUID][]*Span) error {
	return errRepositoryUnavailable
}

func (unavailableRepository) ListTraces(
	context.Context, uuid.UUID, *ListTracesRequestDTO,
) (*ListTracesResponseDTO, error) {
	return nil, errRepositoryUnavailable
}

func (unavailableRepository) GetTraces(
	context.Context, uuid.UUID, []string, *TimeRangeDTO, int64,
) (map[string][]SpanDTO, error) {
	return nil, errRepositoryUnavailable
}

func (unavailableRepository) GetTrace(uuid.UUID, string, *TimeRangeDTO) (*GetTraceResponseDTO, error) {
	return nil, errRepositoryUnavailable
}

func (unavailableRepository) ListServices(uuid.UUID, *TimeRangeDTO) (*ListServicesResponseDTO, error) {
	return nil, errRepositoryUnavailable
}

func (unavailableRepository) ListOperations(
	uuid.UUID,
	string,
	*TimeRangeDTO,
) (*ListOperationsResponseDTO, error) {
	return nil, errRepositoryUnavailable
}

func (unavailableRepository) ExecuteAggregation(
	uuid.UUID,
	*AggregationRequestDTO,
) (*AggregationResponseDTO, error) {
	return nil, errRepositoryUnavailable
}

func (unavailableRepository) ServiceCallEdges(uuid.UUID, *TimeRangeDTO) ([]ServiceCallEdge, error) {
	return nil, errRepositoryUnavailable
}

func (unavailableRepository) ServiceK8sMetadata(
	uuid.UUID,
	*TimeRangeDTO,
) (map[string]K8sResourceInfo, error) {
	return nil, errRepositoryUnavailable
}

func (unavailableRepository) QueryLLMCalls(
	context.Context,
	uuid.UUID,
	*LLMCallsQueryDTO,
) (*LLMCallsResultDTO, error) {
	return nil, errRepositoryUnavailable
}

func (unavailableRepository) GetProjectSpanStats(uuid.UUID) (*SpansStatsDTO, error) {
	return nil, errRepositoryUnavailable
}

func (unavailableRepository) GetSystemSpanStats() (*SpansStatsDTO, error) {
	return nil, errRepositoryUnavailable
}

func (unavailableRepository) DeleteSpansByProject(uuid.UUID) error {
	return errRepositoryUnavailable
}

func (unavailableRepository) DeleteOldSpans(uuid.UUID, time.Time) error {
	return errRepositoryUnavailable
}

func (unavailableRepository) CompactSmallSegments(uuid.UUID, SpanSampling) error {
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
