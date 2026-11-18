package ingestion

import (
	"strconv"

	logs_core "obzly/internal/features/logs/core"
	metrics_core "obzly/internal/features/metrics/core"
	"obzly/internal/util/attrnames"

	"github.com/google/uuid"

	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
)

// Signals.
const (
	SignalLogs    = "logs"
	SignalTraces  = "traces"
	SignalMetrics = "metrics"
)

// The Observe* methods are called by the receivers once a request is
// accepted. They read only resource-level values, once per record for logs
// and Prometheus samples (no per-record resource there) and once per OTLP
// resource otherwise.

// ObserveLogs counts accepted log items; fields are normalized.
func (t *Tracker) ObserveLogs(projectID uuid.UUID, items []*logs_core.LogItem) {
	sources := batch{}
	for _, item := range items {
		fields := item.Fields
		get := func(name string) string {
			value, ok := fields[name]
			if !ok || value == nil {
				return ""
			}
			if text, isText := value.(string); isText {
				return text
			}
			return logs_core.StringifyFieldValue(value)
		}
		sources.add(get, 1, func() kindHint { return detectKind(get, get(attrScopeName), "") })
	}
	t.record(projectID, SignalLogs, sources)
}

// ObserveResourceSpans counts accepted OTLP spans per resource.
func (t *Tracker) ObserveResourceSpans(projectID uuid.UUID, resourceSpans []*tracev1.ResourceSpans) {
	sources := batch{}
	for _, resource := range resourceSpans {
		var count int64
		scope := ""
		for _, scopeSpans := range resource.GetScopeSpans() {
			count += int64(len(scopeSpans.GetSpans()))
			if scope == "" {
				scope = scopeSpans.GetScope().GetName()
			}
		}
		if count == 0 {
			continue
		}
		get := resourceGetter(resource.GetResource().GetAttributes())
		sources.add(get, count, func() kindHint { return detectKind(get, scope, "") })
	}
	t.record(projectID, SignalTraces, sources)
}

// ObserveResourceMetrics counts accepted OTLP data points per resource.
func (t *Tracker) ObserveResourceMetrics(projectID uuid.UUID, resourceMetrics []*metricsv1.ResourceMetrics) {
	sources := batch{}
	for _, resource := range resourceMetrics {
		get := resourceGetter(resource.GetResource().GetAttributes())
		for _, scopeMetrics := range resource.GetScopeMetrics() {
			scope := scopeMetrics.GetScope().GetName()
			for _, metric := range scopeMetrics.GetMetrics() {
				count := int64(dataPoints(metric))
				if count == 0 {
					continue
				}
				name := attrnames.NormalizeMetricName(metric.GetName())
				sources.add(get, count, func() kindHint { return detectKind(get, scope, name) })
			}
		}
	}
	t.record(projectID, SignalMetrics, sources)
}

// ObserveSamples counts accepted Prometheus remote-write samples. Without
// OTel resource attributes, job and instance stand in for service and host.
func (t *Tracker) ObserveSamples(projectID uuid.UUID, samples []*metrics_core.MetricSample) {
	sources := batch{}
	for _, sample := range samples {
		labels := sample.Labels
		get := func(name string) string {
			if value := labels[name]; value != "" {
				return value
			}
			switch name {
			case attrServiceName:
				return labels["job"]
			case attrHostName:
				return labels["instance"]
			}
			return ""
		}
		metricName := sample.MetricName
		sources.add(get, 1, func() kindHint {
			hint := detectKind(get, "", metricName)
			if hint.rank == 0 {
				return kindHint{"prometheus", rankRuntime}
			}
			return hint
		})
	}
	t.record(projectID, SignalMetrics, sources)
}

// resourceGetter reads OTLP resource attributes by normalized name.
func resourceGetter(attributes []*commonv1.KeyValue) attributeGetter {
	values := make(map[string]string, len(attributes))
	for _, attribute := range attributes {
		if value := stringValue(attribute.GetValue()); value != "" {
			values[attrnames.Normalize(attribute.GetKey())] = value
		}
	}
	return func(name string) string { return values[name] }
}

func stringValue(value *commonv1.AnyValue) string {
	switch typed := value.GetValue().(type) {
	case *commonv1.AnyValue_StringValue:
		return typed.StringValue
	case *commonv1.AnyValue_IntValue:
		return strconv.FormatInt(typed.IntValue, 10)
	case *commonv1.AnyValue_BoolValue:
		return strconv.FormatBool(typed.BoolValue)
	case *commonv1.AnyValue_DoubleValue:
		return strconv.FormatFloat(typed.DoubleValue, 'f', -1, 64)
	}
	return ""
}

func dataPoints(metric *metricsv1.Metric) int {
	switch {
	case metric.GetGauge() != nil:
		return len(metric.GetGauge().GetDataPoints())
	case metric.GetSum() != nil:
		return len(metric.GetSum().GetDataPoints())
	case metric.GetHistogram() != nil:
		return len(metric.GetHistogram().GetDataPoints())
	case metric.GetExponentialHistogram() != nil:
		return len(metric.GetExponentialHistogram().GetDataPoints())
	case metric.GetSummary() != nil:
		return len(metric.GetSummary().GetDataPoints())
	}
	return 0
}
