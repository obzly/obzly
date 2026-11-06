// Package otlp_metrics is the OTLP metrics receiver (HTTP POST /v1/metrics and
// gRPC). It flattens OTLP metrics into one-value samples, following the OTel
// Collector's Prometheus exporter convention:
//   - Gauge: one gauge sample per data point.
//   - Sum: one counter sample per data point, monotonic or not.
//   - Histogram: Prometheus classic histogram series `_sum`, `_count` and
//     cumulative `_bucket` with an `le` label, plus `le="+Inf"`.
//   - ExponentialHistogram and Summary are skipped: no lossless mapping.
//
// DELTA Sum and Histogram points become cumulative before they are stored,
// with a running total per series in Valkey (delta.go).
package otlp_metrics

import (
	"math"
	"strconv"
	"time"

	metrics_core "obzly/internal/features/metrics/core"
	"obzly/internal/util/attrnames"

	"github.com/google/uuid"

	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// Conversion is what ConvertResourceMetrics produces.
type Conversion struct {
	Samples []*metrics_core.MetricSample
	// Delta holds the Samples whose Value is still a per-interval delta;
	// toCumulative replaces it with the series' running total.
	Delta []*metrics_core.MetricSample
	// Rejected counts delta samples dropped because a NaN or infinite delta
	// can't be added to a running total.
	Rejected int
}

// ConvertResourceMetrics flattens OTLP ResourceMetrics into samples, with each
// data point's attributes and the resource attributes as labels.
func ConvertResourceMetrics(
	projectID uuid.UUID,
	resourceMetricsList []*metricsv1.ResourceMetrics,
) *Conversion {
	result := &Conversion{}

	for _, resourceMetrics := range resourceMetricsList {
		resourceLabels := map[string]string{}
		if resource := resourceMetrics.GetResource(); resource != nil {
			for _, attribute := range resource.GetAttributes() {
				resourceLabels[attribute.GetKey()] = stringifyAnyValue(attribute.GetValue())
			}
		}

		for _, scopeMetrics := range resourceMetrics.GetScopeMetrics() {
			for _, metric := range scopeMetrics.GetMetrics() {
				samples, delta := convertMetric(projectID, metric, resourceLabels)
				result.add(samples, delta)
			}
		}
	}

	return result
}

func (c *Conversion) add(samples []*metrics_core.MetricSample, delta bool) {
	if !delta {
		c.Samples = append(c.Samples, samples...)
		return
	}
	for _, sample := range samples {
		if math.IsNaN(sample.Value) || math.IsInf(sample.Value, 0) {
			c.Rejected++
			continue
		}
		c.Samples = append(c.Samples, sample)
		c.Delta = append(c.Delta, sample)
	}
}

func convertMetric(
	projectID uuid.UUID,
	metric *metricsv1.Metric,
	resourceLabels map[string]string,
) (samples []*metrics_core.MetricSample, delta bool) {
	switch {
	case metric.GetGauge() != nil:
		return convertNumberDataPoints(
			projectID,
			attrnames.NormalizeMetricName(metric.GetName()),
			metric.GetGauge().GetDataPoints(),
			resourceLabels,
			metrics_core.MetricTypeGauge,
		), false
	case metric.GetSum() != nil:
		return convertNumberDataPoints(
			projectID,
			attrnames.NormalizeMetricName(metric.GetName()),
			metric.GetSum().GetDataPoints(),
			resourceLabels,
			metrics_core.MetricTypeCounter,
		), isDelta(metric.GetSum().GetAggregationTemporality())
	case metric.GetHistogram() != nil:
		return convertHistogramDataPoints(
			projectID,
			attrnames.NormalizeMetricName(metric.GetName()),
			metric.GetHistogram().GetDataPoints(),
			resourceLabels,
		), isDelta(metric.GetHistogram().GetAggregationTemporality())
	default:
		// ExponentialHistogram, Summary or no data: skipped.
		return nil, false
	}
}

func isDelta(temporality metricsv1.AggregationTemporality) bool {
	return temporality == metricsv1.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA
}

func convertNumberDataPoints(
	projectID uuid.UUID,
	name string,
	dataPoints []*metricsv1.NumberDataPoint,
	resourceLabels map[string]string,
	metricType metrics_core.MetricType,
) []*metrics_core.MetricSample {
	samples := make([]*metrics_core.MetricSample, 0, len(dataPoints))
	for _, dp := range dataPoints {
		labels := mergeLabels(resourceLabels, dp.GetAttributes())
		samples = append(samples, &metrics_core.MetricSample{
			ID:         uuid.New(),
			ProjectID:  projectID,
			MetricName: name,
			Labels:     labels,
			Value:      numberValue(dp),
			Timestamp:  timeFromUnixNano(dp.GetTimeUnixNano()),
			Type:       metricType,
		})
	}
	return samples
}

func numberValue(dp *metricsv1.NumberDataPoint) float64 {
	if _, ok := dp.GetValue().(*metricsv1.NumberDataPoint_AsInt); ok {
		return float64(dp.GetAsInt())
	}
	return dp.GetAsDouble()
}

// convertHistogramDataPoints turns each HistogramDataPoint into classic
// `_sum`/`_count`/`_bucket` series.
func convertHistogramDataPoints(
	projectID uuid.UUID,
	name string,
	dataPoints []*metricsv1.HistogramDataPoint,
	resourceLabels map[string]string,
) []*metrics_core.MetricSample {
	var samples []*metrics_core.MetricSample

	for _, dp := range dataPoints {
		labels := mergeLabels(resourceLabels, dp.GetAttributes())
		ts := timeFromUnixNano(dp.GetTimeUnixNano())

		samples = append(samples,
			&metrics_core.MetricSample{
				ID: uuid.New(), ProjectID: projectID, MetricName: name + "_sum",
				Labels: labels, Value: dp.GetSum(), Timestamp: ts, Type: metrics_core.MetricTypeCounter,
			},
			&metrics_core.MetricSample{
				ID: uuid.New(), ProjectID: projectID, MetricName: name + "_count",
				Labels: labels, Value: float64(dp.GetCount()), Timestamp: ts, Type: metrics_core.MetricTypeCounter,
			},
		)

		bucketCounts := dp.GetBucketCounts()
		bounds := dp.GetExplicitBounds()

		var cumulative uint64
		for i, bound := range bounds {
			if i < len(bucketCounts) {
				cumulative += bucketCounts[i]
			}
			bucketLabels := withLabel(labels, "le", formatBound(bound))
			samples = append(samples, &metrics_core.MetricSample{
				ID: uuid.New(), ProjectID: projectID, MetricName: name + "_bucket",
				Labels: bucketLabels, Value: float64(cumulative), Timestamp: ts, Type: metrics_core.MetricTypeCounter,
			})
		}

		// The overflow bucket brings the cumulative total to the full count: the
		// mandatory `le="+Inf"` bucket.
		if len(bucketCounts) > len(bounds) {
			cumulative += bucketCounts[len(bounds)]
		}
		infLabels := withLabel(labels, "le", "+Inf")
		samples = append(samples, &metrics_core.MetricSample{
			ID: uuid.New(), ProjectID: projectID, MetricName: name + "_bucket",
			Labels: infLabels, Value: float64(cumulative), Timestamp: ts, Type: metrics_core.MetricTypeCounter,
		})
	}

	return samples
}

func formatBound(bound float64) string {
	return strconv.FormatFloat(bound, 'g', -1, 64)
}

func withLabel(labels map[string]string, key, value string) map[string]string {
	merged := make(map[string]string, len(labels)+1)
	for k, v := range labels {
		merged[k] = v
	}
	merged[key] = value
	return merged
}

func mergeLabels(resourceLabels map[string]string, attributes []*commonv1.KeyValue) map[string]string {
	// Label names follow attrnames; a data point's own attribute wins over
	// a resource label of the same name.
	labels := make(map[string]string, len(resourceLabels)+len(attributes))
	for k, v := range resourceLabels {
		labels[attrnames.Normalize(k)] = v
	}
	for _, attribute := range attributes {
		labels[attrnames.Normalize(attribute.GetKey())] = stringifyAnyValue(attribute.GetValue())
	}
	return labels
}

func timeFromUnixNano(nanos uint64) time.Time {
	return time.Unix(0, int64(nanos)).UTC()
}

// stringifyAnyValue renders an attribute as a string, since PromQL label
// values are always strings.
func stringifyAnyValue(value *commonv1.AnyValue) string {
	if value == nil {
		return ""
	}

	switch v := value.GetValue().(type) {
	case *commonv1.AnyValue_StringValue:
		return v.StringValue
	case *commonv1.AnyValue_BoolValue:
		return strconv.FormatBool(v.BoolValue)
	case *commonv1.AnyValue_IntValue:
		return strconv.FormatInt(v.IntValue, 10)
	case *commonv1.AnyValue_DoubleValue:
		return strconv.FormatFloat(v.DoubleValue, 'g', -1, 64)
	default:
		// Bytes, arrays and maps make no useful label value: dropped.
		return ""
	}
}
