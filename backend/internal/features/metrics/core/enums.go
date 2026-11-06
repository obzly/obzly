package metrics_core

// MetricType is gauge or counter. PromQL ignores it; the Overview dashboard
// uses it to pick avg() or rate(). OTLP types are flattened onto it at
// ingestion (see otlp_metrics/converter.go).
type MetricType string

const (
	MetricTypeGauge   MetricType = "gauge"
	MetricTypeCounter MetricType = "counter"
)

func (t MetricType) IsValid() bool {
	switch t {
	case MetricTypeGauge, MetricTypeCounter:
		return true
	default:
		return false
	}
}

// MatchType mirrors PromQL's label-matcher operators as strings so
// metrics_core does not import the Prometheus library; metrics_promql
// converts at its boundary.
type MatchType string

const (
	MatchEqual     MatchType = "="
	MatchNotEqual  MatchType = "!="
	MatchRegexp    MatchType = "=~"
	MatchNotRegexp MatchType = "!~"
)
