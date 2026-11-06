package spans_core

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// SpanRepository is the spans stream's storage abstraction; the
// implementation (spans/parquetstore) lives outside this package to avoid
// an import cycle.
type SpanRepository interface {
	StoreSpansBatch(entries map[uuid.UUID][]*Span) error
	ListTraces(ctx context.Context, projectID uuid.UUID, request *ListTracesRequestDTO) (*ListTracesResponseDTO, error)
	GetTrace(projectID uuid.UUID, traceID string, timeRange *TimeRangeDTO) (*GetTraceResponseDTO, error)
	// GetTraces is GetTrace for several traces in one scan, keyed by trace
	// id; scanBudget (0: none) refuses a range planned to read more.
	GetTraces(
		ctx context.Context, projectID uuid.UUID, traceIDs []string, timeRange *TimeRangeDTO, scanBudget int64,
	) (map[string][]SpanDTO, error)
	ListServices(projectID uuid.UUID, timeRange *TimeRangeDTO) (*ListServicesResponseDTO, error)
	ListOperations(projectID uuid.UUID, serviceName string, timeRange *TimeRangeDTO) (*ListOperationsResponseDTO, error)
	ExecuteAggregation(projectID uuid.UUID, request *AggregationRequestDTO) (*AggregationResponseDTO, error)
	// ServiceCallEdges aggregates spans into parent-service -> child-service
	// call pairs for the Service Map.
	ServiceCallEdges(projectID uuid.UUID, timeRange *TimeRangeDTO) ([]ServiceCallEdge, error)
	// ServiceK8sMetadata resolves each service's k8s namespace, deployment
	// and node from span resource attributes.
	ServiceK8sMetadata(projectID uuid.UUID, timeRange *TimeRangeDTO) (map[string]K8sResourceInfo, error)
	QueryLLMCalls(ctx context.Context, projectID uuid.UUID, request *LLMCallsQueryDTO) (*LLMCallsResultDTO, error)
	GetProjectSpanStats(projectID uuid.UUID) (*SpansStatsDTO, error)
	GetSystemSpanStats() (*SpansStatsDTO, error)
	DeleteSpansByProject(projectID uuid.UUID) error
	DeleteOldSpans(projectID uuid.UUID, olderThan time.Time) error
	// CompactSmallSegments merges a project's small segment files into
	// fewer, larger ones.
	CompactSmallSegments(projectID uuid.UUID, sampling SpanSampling) error
	UploadPendingSegments() error
	ReconcileSegments() error
	ForceFlush() error
	TestConnection() error
}

// SpanSampling is a project's trace tail sampling, applied when a day's spans merge into cold files: a trace is kept if any
// of its spans that day is an error or at least SlowMs long, or if its id
// hashes under Rate. Rate 1 keeps everything.
type SpanSampling struct {
	Rate   float64
	SlowMs int
}

func (s SpanSampling) Active() bool {
	return s.Rate < 1
}
