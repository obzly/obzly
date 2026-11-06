// Package prometheus_remotewrite is the Prometheus remote_write ingestion
// endpoint, the second metrics path beside OTLP. Only Remote Write 1.0
// (snappy-compressed prompb.WriteRequest) is supported; 2.0 is a different
// wire schema and is rejected rather than mis-parsed.
package prometheus_remotewrite

import (
	"time"

	metrics_core "obzly/internal/features/metrics/core"

	"github.com/google/uuid"

	"github.com/prometheus/prometheus/prompb"
)

func timestampFromMillis(ms int64) time.Time {
	return time.UnixMilli(ms).UTC()
}

// ConvertTimeSeries turns time series into one MetricSample per sample. The
// type comes from metadata, else typeHeuristic (many agents send no metadata).
func ConvertTimeSeries(
	projectID uuid.UUID,
	series []prompb.TimeSeries,
	metadata map[string]metrics_core.MetricType,
) []*metrics_core.MetricSample {
	var result []*metrics_core.MetricSample

	for _, ts := range series {
		name, labels := splitLabels(ts.Labels)
		if name == "" {
			// No __name__: invalid, and no query could select it.
			continue
		}

		metricType, ok := metadata[name]
		if !ok {
			metricType = typeHeuristic(name)
		}

		for _, sample := range ts.Samples {
			result = append(result, &metrics_core.MetricSample{
				ID:         uuid.New(),
				ProjectID:  projectID,
				MetricName: name,
				Labels:     labels,
				Value:      sample.Value,
				Timestamp:  timestampFromMillis(sample.Timestamp),
				Type:       metricType,
			})
		}
	}

	return result
}

// MetadataTypes maps metric family names to MetricType for ConvertTimeSeries.
func MetadataTypes(metadata []prompb.MetricMetadata) map[string]metrics_core.MetricType {
	types := make(map[string]metrics_core.MetricType, len(metadata))
	for _, m := range metadata {
		if t, ok := promTypeToMetricType(m.Type); ok {
			types[m.MetricFamilyName] = t
		}
	}
	return types
}

func promTypeToMetricType(t prompb.MetricMetadata_MetricType) (metrics_core.MetricType, bool) {
	switch t {
	case prompb.MetricMetadata_COUNTER:
		return metrics_core.MetricTypeCounter, true
	case prompb.MetricMetadata_GAUGE:
		return metrics_core.MetricTypeGauge, true
	default:
		// Histograms and summaries arrive as several plain series
		// (_sum/_count/_bucket), so each gets the name heuristic.
		return "", false
	}
}

// typeHeuristic follows Prometheus naming: counters end in "_total", and
// histogram/summary series in _sum, _count or _bucket.
func typeHeuristic(name string) metrics_core.MetricType {
	for _, suffix := range []string{"_total", "_sum", "_count", "_bucket"} {
		if len(name) > len(suffix) && name[len(name)-len(suffix):] == suffix {
			return metrics_core.MetricTypeCounter
		}
	}
	return metrics_core.MetricTypeGauge
}

func splitLabels(promLabels []prompb.Label) (name string, labels map[string]string) {
	labels = make(map[string]string, len(promLabels))
	for _, label := range promLabels {
		if label.Name == "__name__" {
			name = label.Value
			continue
		}
		labels[label.Name] = label.Value
	}
	return name, labels
}
