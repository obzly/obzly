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
	// ListServices, ListOperations and the Service* stats below take the
	// caller's context (a request gone away stops them) and a scan budget
	// in bytes (0: none), as the trace searches do.
	ListServices(
		ctx context.Context, projectID uuid.UUID, timeRange *TimeRangeDTO, scanBudget int64,
	) (*ListServicesResponseDTO, error)
	ListOperations(
		ctx context.Context, projectID uuid.UUID, serviceName string, timeRange *TimeRangeDTO, scanBudget int64,
	) (*ListOperationsResponseDTO, error)
	ExecuteAggregation(projectID uuid.UUID, request *AggregationRequestDTO) (*AggregationResponseDTO, error)
	// ServiceCallEdges aggregates spans into parent-service -> child-service
	// call pairs for the Service Map.
	ServiceCallEdges(
		ctx context.Context, projectID uuid.UUID, timeRange *TimeRangeDTO, scanBudget int64,
	) ([]ServiceCallEdge, error)
	// ServiceK8sMetadata resolves each service's k8s namespace, deployment
	// and node from span resource attributes.
	ServiceK8sMetadata(
		ctx context.Context, projectID uuid.UUID, timeRange *TimeRangeDTO, scanBudget int64,
	) (map[string]K8sResourceInfo, error)
	// ServiceNodeStats is the Service Map's per-node RED numbers and the
	// untraced peers (databases, caches, brokers, external APIs) seen from
	// client spans.
	ServiceNodeStats(
		ctx context.Context, projectID uuid.UUID, timeRange *TimeRangeDTO, scanBudget int64,
	) (*ServiceNodeStats, error)
	// TraceSummary is the Traces page's summary tiles (current and previous
	// period, bucketed) and its capped count of matching traces.
	TraceSummary(ctx context.Context, projectID uuid.UUID, request *TraceSummaryQuery) (*TraceSummaryResult, error)
	// ServiceErrorRates is each service's entry spans and errors in a period
	// and the one before it, most errors first.
	ServiceErrorRates(
		ctx context.Context,
		projectID uuid.UUID,
		request *ServiceErrorRatesQuery,
	) ([]ServiceErrorRate, error)
	// ServiceErrorSpanCounts counts error spans per service in the range, for
	// services plus the services that own spanIDs.
	ServiceErrorSpanCounts(
		projectID uuid.UUID, timeRange *TimeRangeDTO, services []string, spanIDs []string,
	) (map[string]int64, error)
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
