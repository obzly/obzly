package otlp_metrics

import (
	"testing"
	"time"

	metrics_core "obzly/internal/features/metrics/core"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
)

func float64Ptr(v float64) *float64 { return &v }

func stringAttr(key, value string) *commonv1.KeyValue {
	return &commonv1.KeyValue{
		Key:   key,
		Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: value}},
	}
}

func Test_ConvertResourceMetrics_Gauge_OneSamplePerDataPoint(t *testing.T) {
	projectID := uuid.New()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	resourceMetrics := []*metricsv1.ResourceMetrics{
		{
			Resource: &resourcev1.Resource{Attributes: []*commonv1.KeyValue{stringAttr("service.name", "checkout")}},
			ScopeMetrics: []*metricsv1.ScopeMetrics{
				{
					Metrics: []*metricsv1.Metric{
						{
							Name: "cpu_usage",
							Data: &metricsv1.Metric_Gauge{Gauge: &metricsv1.Gauge{
								DataPoints: []*metricsv1.NumberDataPoint{
									{
										TimeUnixNano: uint64(now.UnixNano()),
										Value:        &metricsv1.NumberDataPoint_AsDouble{AsDouble: 0.75},
										Attributes:   []*commonv1.KeyValue{stringAttr("core", "0")},
									},
								},
							}},
						},
					},
				},
			},
		},
	}

	samples := ConvertResourceMetrics(projectID, resourceMetrics).Samples
	require.Len(t, samples, 1)

	s := samples[0]
	assert.Equal(t, projectID, s.ProjectID)
	assert.Equal(t, "cpu_usage", s.MetricName)
	assert.Equal(t, metrics_core.MetricTypeGauge, s.Type)
	assert.InDelta(t, 0.75, s.Value, 0.0001)
	assert.Equal(t, "checkout", s.Labels["service_name"])
	assert.Equal(t, "0", s.Labels["core"])
	assert.True(t, s.Timestamp.Equal(now))
}

func Test_ConvertResourceMetrics_Sum_ProducesCounterType(t *testing.T) {
	projectID := uuid.New()
	now := time.Now().UTC()

	resourceMetrics := []*metricsv1.ResourceMetrics{
		{
			ScopeMetrics: []*metricsv1.ScopeMetrics{
				{
					Metrics: []*metricsv1.Metric{
						{
							Name: "http_requests_total",
							Data: &metricsv1.Metric_Sum{Sum: &metricsv1.Sum{
								IsMonotonic: true,
								DataPoints: []*metricsv1.NumberDataPoint{
									{
										TimeUnixNano: uint64(now.UnixNano()),
										Value:        &metricsv1.NumberDataPoint_AsInt{AsInt: 42},
									},
								},
							}},
						},
					},
				},
			},
		},
	}

	samples := ConvertResourceMetrics(projectID, resourceMetrics).Samples
	require.Len(t, samples, 1)
	assert.Equal(t, metrics_core.MetricTypeCounter, samples[0].Type)
	assert.InDelta(t, 42, samples[0].Value, 0.0001)
}

// OTel per-bucket counts become Prometheus cumulative buckets, plus le="+Inf".
func Test_ConvertResourceMetrics_Histogram_DecomposesIntoSumCountAndCumulativeBuckets(t *testing.T) {
	projectID := uuid.New()
	now := time.Now().UTC()

	resourceMetrics := []*metricsv1.ResourceMetrics{
		{
			ScopeMetrics: []*metricsv1.ScopeMetrics{
				{
					Metrics: []*metricsv1.Metric{
						{
							Name: "request_duration_ms",
							Data: &metricsv1.Metric_Histogram{Histogram: &metricsv1.Histogram{
								DataPoints: []*metricsv1.HistogramDataPoint{
									{
										TimeUnixNano:   uint64(now.UnixNano()),
										Count:          10,
										Sum:            float64Ptr(550),
										ExplicitBounds: []float64{10, 50, 100},
										// buckets: (-inf,10]=2, (10,50]=5, (50,100]=2, (100,+inf]=1
										BucketCounts: []uint64{2, 5, 2, 1},
									},
								},
							}},
						},
					},
				},
			},
		},
	}

	samples := ConvertResourceMetrics(projectID, resourceMetrics).Samples

	byName := map[string][]*metrics_core.MetricSample{}
	for _, s := range samples {
		byName[s.MetricName] = append(byName[s.MetricName], s)
	}

	require.Len(t, byName["request_duration_ms_sum"], 1)
	assert.InDelta(t, 550, byName["request_duration_ms_sum"][0].Value, 0.0001)

	require.Len(t, byName["request_duration_ms_count"], 1)
	assert.InDelta(t, 10, byName["request_duration_ms_count"][0].Value, 0.0001)

	buckets := byName["request_duration_ms_bucket"]
	require.Len(t, buckets, 4) // 3 explicit bounds + the +Inf bucket

	byLE := map[string]float64{}
	for _, b := range buckets {
		byLE[b.Labels["le"]] = b.Value
	}
	assert.InDelta(t, 2, byLE["10"], 0.0001)
	assert.InDelta(t, 7, byLE["50"], 0.0001)
	assert.InDelta(t, 9, byLE["100"], 0.0001)
	assert.InDelta(t, 10, byLE["+Inf"], 0.0001)
}

func Test_ConvertResourceMetrics_ExponentialHistogramAndSummary_Skipped(t *testing.T) {
	projectID := uuid.New()

	resourceMetrics := []*metricsv1.ResourceMetrics{
		{
			ScopeMetrics: []*metricsv1.ScopeMetrics{
				{
					Metrics: []*metricsv1.Metric{
						{Name: "exp_histo", Data: &metricsv1.Metric_ExponentialHistogram{
							ExponentialHistogram: &metricsv1.ExponentialHistogram{},
						}},
						{Name: "summary", Data: &metricsv1.Metric_Summary{Summary: &metricsv1.Summary{}}},
					},
				},
			},
		},
	}

	samples := ConvertResourceMetrics(projectID, resourceMetrics).Samples
	assert.Empty(t, samples)
}
