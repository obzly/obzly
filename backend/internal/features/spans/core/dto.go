package spans_core

import "time"

// TimeRangeDTO duplicates logs_core.TimeRangeDTO so signal packages stay
// independent of each other.
type TimeRangeDTO struct {
	From *time.Time `json:"from,omitempty"`
	To   *time.Time `json:"to,omitempty"`
}

// SpanDTO is a span on the read path, with DurationMs precomputed for the
// waterfall.
type SpanDTO struct {
	ID                 string         `json:"id"`
	TraceID            string         `json:"traceId"`
	SpanID             string         `json:"spanId"`
	ParentSpanID       string         `json:"parentSpanId,omitempty"`
	Name               string         `json:"name"`
	ServiceName        string         `json:"serviceName"`
	Kind               string         `json:"kind"`
	StartTime          time.Time      `json:"startTime"`
	EndTime            time.Time      `json:"endTime"`
	DurationMs         float64        `json:"durationMs"`
	StatusCode         string         `json:"statusCode"`
	StatusMessage      string         `json:"statusMessage,omitempty"`
	Attributes         map[string]any `json:"attributes,omitempty"`
	ResourceAttributes map[string]any `json:"resourceAttributes,omitempty"`
	Events             []SpanEventDTO `json:"events,omitempty"`
}

type SpanEventDTO struct {
	Name       string         `json:"name"`
	Timestamp  time.Time      `json:"timestamp"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// TraceSummaryDTO is one trace search result row, aggregated per trace_id.
type TraceSummaryDTO struct {
	TraceID         string    `json:"traceId"`
	RootName        string    `json:"rootName"`
	RootServiceName string    `json:"rootServiceName"`
	StartTime       time.Time `json:"startTime"`
	DurationMs      float64   `json:"durationMs"`
	SpanCount       int64     `json:"spanCount"`
	ErrorCount      int64     `json:"errorCount"`
	Services        []string  `json:"services"`
}

// ListTracesRequestDTO drives trace search for both the REST and the
// Jaeger-compatible APIs.
type ListTracesRequestDTO struct {
	TimeRange     *TimeRangeDTO `json:"timeRange,omitempty"`
	ServiceName   string        `json:"serviceName,omitempty"`
	OperationName string        `json:"operationName,omitempty"`
	MinDurationMs *float64      `json:"minDurationMs,omitempty"`
	MaxDurationMs *float64      `json:"maxDurationMs,omitempty"`
	ErrorsOnly    bool          `json:"errorsOnly,omitempty"`
	Limit         int           `json:"limit,omitempty"`
	// ScanBudget: set by the HTTP service from QUERY_MAX_SCAN_GB (never
	// from JSON); 0 for none, as for background work.
	ScanBudget int64 `json:"-"`
}

type ListTracesResponseDTO struct {
	Traces []TraceSummaryDTO `json:"traces"`
}

type GetTraceResponseDTO struct {
	TraceID string    `json:"traceId"`
	Spans   []SpanDTO `json:"spans"`
}

type ListServicesResponseDTO struct {
	Services []string `json:"services"`
}

type ListOperationsResponseDTO struct {
	Operations []string `json:"operations"`
}

// AggregationRequestDTO drives span-backed dashboard panels; GroupField and
// BucketMs are independently optional, as for logs.
type AggregationRequestDTO struct {
	TimeRange     *TimeRangeDTO `json:"timeRange,omitempty"`
	ServiceName   string        `json:"serviceName,omitempty"`
	OperationName string        `json:"operationName,omitempty"`
	// RootSpansOnly counts only spans with no parent: "requests" in the
	// RED-method sense.
	RootSpansOnly bool `json:"rootSpansOnly,omitempty"`
	// GroupField: one of the GroupField constants, or "" for no grouping.
	GroupField GroupField `json:"groupField,omitempty"`
	// BucketMs: time-bucket width in milliseconds, 0 for no time bucketing.
	BucketMs int64 `json:"bucketMs,omitempty"`
	// Metric defaults to "count".
	Metric AggregationMetric `json:"metric,omitempty"`
	Limit  int               `json:"limit,omitempty"`
	// ScanBudget: set by the HTTP service from QUERY_MAX_SCAN_GB (never
	// from JSON); 0 for none, as for background work.
	ScanBudget int64 `json:"-"`
}

type AggregationBucketDTO struct {
	Timestamp *time.Time `json:"timestamp,omitempty"`
	GroupKey  string     `json:"groupKey,omitempty"`
	Value     float64    `json:"value"`
}

type AggregationResponseDTO struct {
	Buckets []AggregationBucketDTO `json:"buckets"`
}

// ServiceCallEdge is one aggregated parent-service -> child-service call
// pair, from joining spans on parent_span_id = span_id within a trace.
type ServiceCallEdge struct {
	SrcService string
	DstService string
	CallCount  int64
	ErrorCount int64
	// AvgDurationMs is the average child-span duration for this pair.
	AvgDurationMs float64
}

// K8sResourceInfo is one service's k8s namespace, deployment and node
// resource attributes; any may be empty.
type K8sResourceInfo struct {
	Namespace  string
	Deployment string
	Node       string
}

// ServiceNodeStat is one instrumented service's RED numbers over its entry
// spans (SERVER or CONSUMER kind, or a root span). Requests is 0 for a
// service with spans but no entry span; P95Ms is then nil.
type ServiceNodeStat struct {
	ServiceName string
	Requests    int64
	Errors      int64
	P95Ms       *float64
}

// PeerCallStat is SrcService's CLIENT/PRODUCER spans to one peer that no
// span of the trace answered (an untraced database, cache, broker or API).
// DBSystem/MessagingSystem are the spans' db_system/messaging_system, "" if
// none.
type PeerCallStat struct {
	SrcService      string
	Peer            string
	DBSystem        string
	MessagingSystem string
	CallCount       int64
	ErrorCount      int64
	AvgDurationMs   float64
}

// PeerNodeStat is one peer's totals over every caller, with its own p95
// (percentiles can't be merged from PeerCallStat rows).
type PeerNodeStat struct {
	Peer            string
	DBSystem        string
	MessagingSystem string
	Requests        int64
	Errors          int64
	P95Ms           *float64
}

type ServiceNodeStats struct {
	Services  []ServiceNodeStat
	PeerEdges []PeerCallStat
	Peers     []PeerNodeStat
}

// ServiceErrorRatesQuery counts each service's entry spans (requests in the
// RED sense) in the current period [From, To] and the previous one
// [PreviousFrom, From); Limit keeps the services with the most errors.
type ServiceErrorRatesQuery struct {
	From         time.Time
	To           time.Time
	PreviousFrom time.Time
	Limit        int
	ScanBudget   int64
}

type ServiceErrorRate struct {
	ServiceName      string
	Requests         int64
	Errors           int64
	PreviousRequests int64
	PreviousErrors   int64
}

// TraceSummaryQuery drives the Traces summary: the subject spans are root
// spans (RootSpansOnly) or ServiceName's spans, narrowed by OperationName,
// the duration bounds (on each span) and ErrorsOnly. Current is [From, To],
// previous [PreviousFrom, From); StepMs buckets the current period.
type TraceSummaryQuery struct {
	From          time.Time
	To            time.Time
	PreviousFrom  time.Time
	StepMs        int64
	ServiceName   string
	OperationName string
	MinDurationMs *float64
	MaxDurationMs *float64
	ErrorsOnly    bool
	RootSpansOnly bool
	// CountOnly skips the tiles query (only MatchingTraces is computed).
	CountOnly bool
	// MatchCap stops counting matching traces past this many; the result
	// is then MatchCap+1.
	MatchCap   int
	ScanBudget int64
}

type TraceSummaryPeriod struct {
	Count  int64
	Errors int64
	P50Ms  *float64
	P95Ms  *float64
}

// TraceSummaryBucket is one current-period bucket; Index is
// floor(unix ns / step ns), so buckets align to multiples of the step.
type TraceSummaryBucket struct {
	Index  int64
	Count  int64
	Errors int64
	P50Ms  *float64
	P95Ms  *float64
}

type TraceSummaryResult struct {
	Current  TraceSummaryPeriod
	Previous TraceSummaryPeriod
	Buckets  []TraceSummaryBucket
	// MatchingTraces counts root spans in [From, To] whose trace matches the
	// filters, at most MatchCap+1.
	MatchingTraces int64
}

// SpansStatsDTO is a project's spans consumption stats (see ARCHITECTURE.md
// "Project statistics").
type SpansStatsDTO struct {
	TotalSpans int64 `json:"totalSpans"`
	// TotalSizeMB is the compressed on-disk Parquet size.
	TotalSizeMB float64 `json:"totalSizeMb"`
	// RawSizeMB is the approximate uncompressed size of the ingested spans.
	RawSizeMB float64 `json:"rawSizeMb"`
	// ObjectStorageSizeMB: the part of TotalSizeMB already in object storage
	// (0 without object storage); the rest is fresh segments on local disk.
	ObjectStorageSizeMB float64   `json:"objectStorageSizeMb"`
	OldestSpanTime      time.Time `json:"oldestSpanTime"`
	NewestSpanTime      time.Time `json:"newestSpanTime"`
}

// LLMCallsQueryDTO is one trusted, server-built statement (never user SQL)
// over the table "llm": one row per LLM call span (a span with
// gen_ai.operation.name) in [From, To], with only LLMCallColumns - message
// content is never selected.
type LLMCallsQueryDTO struct {
	From time.Time
	To   time.Time
	SQL  string
	Args []any
	// ScanBudget: 0 for none (background work).
	ScanBudget int64
}

// LLMCallColumns are the columns of "llm": ts (TIMESTAMP_NS), service,
// operation, model (request model, else the response model),
// response_model, provider (gen_ai.provider.name, else the older
// gen_ai.system), input_tokens, output_tokens, cached_tokens (DOUBLE, NULL
// when absent), is_error (BOOLEAN), error_type, finish_reasons, duration_ms
// (DOUBLE), trace_id, span_id, server_address, user_id (user.id, else
// enduser.id).
var LLMCallColumns = []string{
	"ts", "service", "operation", "model", "response_model", "provider", "input_tokens", "output_tokens",
	"cached_tokens", "is_error", "error_type", "finish_reasons", "duration_ms", "trace_id", "span_id",
	"server_address", "user_id",
}

type LLMCallsResultDTO struct {
	Rows [][]any
}
