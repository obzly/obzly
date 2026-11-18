package traces_querying

import (
	"time"

	spans_core "obzly/internal/features/spans/core"
)

// TraceSummaryRequestDTO carries the trace search's filters; TimeRange needs
// both ends.
type TraceSummaryRequestDTO struct {
	TimeRange     *spans_core.TimeRangeDTO `json:"timeRange"`
	ServiceName   string                   `json:"serviceName,omitempty"`
	OperationName string                   `json:"operationName,omitempty"`
	MinDurationMs *float64                 `json:"minDurationMs,omitempty"`
	MaxDurationMs *float64                 `json:"maxDurationMs,omitempty"`
	ErrorsOnly    bool                     `json:"errorsOnly,omitempty"`
	// CountOnly returns only the matching-traces count (the summary tiles
	// are hidden), skipping the tiles query.
	CountOnly bool `json:"countOnly,omitempty"`
}

// TraceSummaryTotalsDTO is one period's totals; a null is unknown (P50/P95
// with no spans), never 0.
type TraceSummaryTotalsDTO struct {
	Count  *int64   `json:"count"`
	Errors *int64   `json:"errors"`
	P50Ms  *float64 `json:"p50Ms"`
	P95Ms  *float64 `json:"p95Ms"`
}

// TraceSummaryResponseDTO is the Traces summary tiles plus the header's
// matching-traces count. Trends cover the current period in StepMs buckets:
// CountTrend has every bucket (0 when empty); the others only buckets with
// spans, in time order. ErrorRateTrend is in percent.
type TraceSummaryResponseDTO struct {
	Current        TraceSummaryTotalsDTO `json:"current"`
	Previous       TraceSummaryTotalsDTO `json:"previous"`
	WindowMinutes  float64               `json:"windowMinutes"`
	RootSpansOnly  bool                  `json:"rootSpansOnly"`
	StepMs         int64                 `json:"stepMs"`
	CountTrend     []float64             `json:"countTrend"`
	ErrorRateTrend []float64             `json:"errorRateTrend"`
	P50Trend       []float64             `json:"p50Trend"`
	P95Trend       []float64             `json:"p95Trend"`
	// MatchingTraces counts traces whose root span starts in the range and
	// that match the filters; past MatchingTracesCap it stops counting, says
	// MatchingTracesCapped and holds the cap.
	MatchingTraces       int64 `json:"matchingTraces"`
	MatchingTracesCapped bool  `json:"matchingTracesCapped"`
	MatchingTracesCap    int64 `json:"matchingTracesCap"`
}

// ServiceErrorsRequestDTO asks for the services with the most errors in a
// range; TimeRange needs both ends (at most maxServiceErrorsRange long).
type ServiceErrorsRequestDTO struct {
	TimeRange *spans_core.TimeRangeDTO `json:"timeRange"`
	// Limit is how many services to return (default 10, at most 50).
	Limit int `json:"limit,omitempty"`
}

// ServiceErrorsDTO is one service's entry spans (SERVER/CONSUMER spans and
// trace roots) and how many failed, in the range and the one before it.
type ServiceErrorsDTO struct {
	ServiceName      string `json:"serviceName"`
	Requests         int64  `json:"requests"`
	Errors           int64  `json:"errors"`
	PreviousRequests int64  `json:"previousRequests"`
	PreviousErrors   int64  `json:"previousErrors"`
}

// ServiceErrorsResponseDTO lists services by errors, most first. From is
// the range start after the clamp; PreviousFrom..From is the period compared with.
type ServiceErrorsResponseDTO struct {
	From         time.Time          `json:"from"`
	To           time.Time          `json:"to"`
	PreviousFrom time.Time          `json:"previousFrom"`
	Clamped      bool               `json:"clamped"`
	Services     []ServiceErrorsDTO `json:"services"`
}
