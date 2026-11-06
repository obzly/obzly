package report

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	collectormetricsv1 "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"

	"obzly/internal/synthetics/spec"
)

// Gauge is one point of a named gauge with its labels, for senders other
// than check runs (cloud inventory).
type Gauge struct {
	Name   string
	Value  float64
	Labels map[string]string
}

// maxGaugesPerRequest keeps each export well under ingest's body limit.
const maxGaugesPerRequest = 5000

// SendGauges exports points, all stamped at, in batches.
func (s *Sender) SendGauges(
	ctx context.Context,
	destination spec.Destination,
	probe spec.ProbeInfo,
	at time.Time,
	points []Gauge,
) error {
	base := strings.TrimRight(destination.OTLPURL, "/")
	resource := s.metricsResource(probe)
	for start := 0; start < len(points); start += maxGaugesPerRequest {
		end := min(start+maxGaugesPerRequest, len(points))
		request := &collectormetricsv1.ExportMetricsServiceRequest{ResourceMetrics: []*metricsv1.ResourceMetrics{{
			Resource:     resource,
			ScopeMetrics: []*metricsv1.ScopeMetrics{{Metrics: gaugeMetrics(points[start:end], at)}},
		}}}
		if err := s.post(ctx, base+"/v1/metrics", destination.APIKey, request); err != nil {
			return fmt.Errorf("metrics: %w", err)
		}
	}
	return nil
}

func gaugeMetrics(points []Gauge, at time.Time) []*metricsv1.Metric {
	byName := map[string][]*metricsv1.NumberDataPoint{}
	for _, point := range points {
		keys := make([]string, 0, len(point.Labels))
		for key := range point.Labels {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		labels := make([]*commonv1.KeyValue, 0, len(keys))
		for _, key := range keys {
			labels = append(labels, stringAttribute(key, point.Labels[key]))
		}
		byName[point.Name] = append(byName[point.Name], &metricsv1.NumberDataPoint{
			TimeUnixNano: uint64(at.UnixNano()),
			Value:        &metricsv1.NumberDataPoint_AsDouble{AsDouble: point.Value},
			Attributes:   labels,
		})
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	metrics := make([]*metricsv1.Metric, 0, len(names))
	for _, name := range names {
		metrics = append(metrics, &metricsv1.Metric{
			Name: name,
			Data: &metricsv1.Metric_Gauge{Gauge: &metricsv1.Gauge{DataPoints: byName[name]}},
		})
	}
	return metrics
}
