package spans_testsupport

import (
	"context"
	"sync"
	"testing"

	spans_core "obzly/internal/features/spans/core"

	"github.com/google/uuid"
)

// LookupCall is one call of a service lookup or a Service Map read as the
// repository got it.
type LookupCall struct {
	Method     string
	Ctx        context.Context
	TimeRange  *spans_core.TimeRangeDTO
	ScanBudget int64
}

// LookupRecorder records the repository's service lookups (ListServices,
// ListOperations) and Service Map reads (ServiceCallEdges,
// ServiceK8sMetadata, ServiceNodeStats), then answers them from the real
// repository, or with Err when set.
type LookupRecorder struct {
	spans_core.SpanRepository
	Err error

	mu    sync.Mutex
	calls []LookupCall
}

// RecordLookups puts a LookupRecorder in front of the repository until the
// test ends. Not for parallel tests: the repository is process-wide.
func RecordLookups(t testing.TB) *LookupRecorder {
	t.Helper()
	previous := spans_core.GetSpanCoreRepository()
	recorder := &LookupRecorder{SpanRepository: previous}
	spans_core.SetRepository(recorder)
	t.Cleanup(func() { spans_core.SetRepository(previous) })
	return recorder
}

// Calls are the calls recorded so far, in order.
func (r *LookupRecorder) Calls() []LookupCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]LookupCall{}, r.calls...)
}

func (r *LookupRecorder) ListServices(
	ctx context.Context, projectID uuid.UUID, timeRange *spans_core.TimeRangeDTO, scanBudget int64,
) (*spans_core.ListServicesResponseDTO, error) {
	if err := r.record("ListServices", ctx, timeRange, scanBudget); err != nil {
		return nil, err
	}
	return r.SpanRepository.ListServices(ctx, projectID, timeRange, scanBudget)
}

func (r *LookupRecorder) ListOperations(
	ctx context.Context, projectID uuid.UUID, serviceName string, timeRange *spans_core.TimeRangeDTO,
	scanBudget int64,
) (*spans_core.ListOperationsResponseDTO, error) {
	if err := r.record("ListOperations", ctx, timeRange, scanBudget); err != nil {
		return nil, err
	}
	return r.SpanRepository.ListOperations(ctx, projectID, serviceName, timeRange, scanBudget)
}

func (r *LookupRecorder) ServiceCallEdges(
	ctx context.Context, projectID uuid.UUID, timeRange *spans_core.TimeRangeDTO, scanBudget int64,
) ([]spans_core.ServiceCallEdge, error) {
	if err := r.record("ServiceCallEdges", ctx, timeRange, scanBudget); err != nil {
		return nil, err
	}
	return r.SpanRepository.ServiceCallEdges(ctx, projectID, timeRange, scanBudget)
}

func (r *LookupRecorder) ServiceK8sMetadata(
	ctx context.Context, projectID uuid.UUID, timeRange *spans_core.TimeRangeDTO, scanBudget int64,
) (map[string]spans_core.K8sResourceInfo, error) {
	if err := r.record("ServiceK8sMetadata", ctx, timeRange, scanBudget); err != nil {
		return nil, err
	}
	return r.SpanRepository.ServiceK8sMetadata(ctx, projectID, timeRange, scanBudget)
}

func (r *LookupRecorder) ServiceNodeStats(
	ctx context.Context, projectID uuid.UUID, timeRange *spans_core.TimeRangeDTO, scanBudget int64,
) (*spans_core.ServiceNodeStats, error) {
	if err := r.record("ServiceNodeStats", ctx, timeRange, scanBudget); err != nil {
		return nil, err
	}
	return r.SpanRepository.ServiceNodeStats(ctx, projectID, timeRange, scanBudget)
}

func (r *LookupRecorder) record(
	method string, ctx context.Context, timeRange *spans_core.TimeRangeDTO, scanBudget int64,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, LookupCall{Method: method, Ctx: ctx, TimeRange: timeRange, ScanBudget: scanBudget})
	return r.Err
}
