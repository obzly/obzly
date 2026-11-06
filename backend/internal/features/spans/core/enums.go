package spans_core

// SpanKind is the OTel span kind (trace.v1.Span.SpanKind), stored as text.
type SpanKind string

const (
	SpanKindUnspecified SpanKind = "UNSPECIFIED"
	SpanKindInternal    SpanKind = "INTERNAL"
	SpanKindServer      SpanKind = "SERVER"
	SpanKindClient      SpanKind = "CLIENT"
	SpanKindProducer    SpanKind = "PRODUCER"
	SpanKindConsumer    SpanKind = "CONSUMER"
)

// StatusCode is the OTel span status code (trace.v1.Status.StatusCode).
type StatusCode string

const (
	StatusCodeUnset StatusCode = "UNSET"
	StatusCodeOK    StatusCode = "OK"
	StatusCodeError StatusCode = "ERROR"
)

// AggregationMetric selects what ExecuteAggregation computes per group:
// unlike logs, span panels need latency and errors, not just counts.
type AggregationMetric string

const (
	AggregationMetricCount       AggregationMetric = "count"
	AggregationMetricErrorCount  AggregationMetric = "error_count"
	AggregationMetricAvgDuration AggregationMetric = "avg_duration_ms"
	AggregationMetricP50Duration AggregationMetric = "p50_duration_ms"
	AggregationMetricP95Duration AggregationMetric = "p95_duration_ms"
	AggregationMetricP99Duration AggregationMetric = "p99_duration_ms"
)

func (m AggregationMetric) IsValid() bool {
	switch m {
	case AggregationMetricCount, AggregationMetricErrorCount,
		AggregationMetricAvgDuration, AggregationMetricP50Duration,
		AggregationMetricP95Duration, AggregationMetricP99Duration:
		return true
	default:
		return false
	}
}

// GroupField is a closed set of well-known span properties; unlike logs,
// span panels cannot group by arbitrary attributes.
type GroupField string

const (
	GroupFieldServiceName GroupField = "service_name"
	GroupFieldName        GroupField = "name"
	GroupFieldStatusCode  GroupField = "status_code"
	GroupFieldKind        GroupField = "kind"
)

func (f GroupField) IsValid() bool {
	switch f {
	case GroupFieldServiceName, GroupFieldName, GroupFieldStatusCode, GroupFieldKind:
		return true
	default:
		return false
	}
}
