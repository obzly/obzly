package otlp_metrics

import (
	"context"
	"math"
	"testing"
	"time"

	"obzly/internal/cache"
	metrics_core "obzly/internal/features/metrics/core"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"
)

const (
	delta      = metricsv1.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA
	cumulative = metricsv1.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE
)

type memoryDeltaTotals struct {
	totals map[string]float64
	calls  int
}

func (m *memoryDeltaTotals) Add(_ context.Context, increments []DeltaIncrement) ([]float64, error) {
	m.calls++
	result := make([]float64, len(increments))
	for i, increment := range increments {
		m.totals[increment.Key] += increment.Value
		result[i] = m.totals[increment.Key]
	}
	return result, nil
}

type timedValue struct {
	at    time.Time
	value float64
}

func Test_ToCumulative_DeltaSumInTwoRequests_StoresRunningTotals(t *testing.T) {
	projectID, totals := uuid.New(), newMemoryDeltaTotals()
	now := time.Now().UTC()

	first := convertAndAccumulate(t, totals, projectID, sumMetric("tokens", delta, timedValue{now, 3}))
	second := convertAndAccumulate(t, totals, projectID, sumMetric("tokens", delta, timedValue{now.Add(time.Second), 5}))

	assert.Equal(t, []float64{3}, values(first))
	assert.Equal(t, []float64{8}, values(second))
}

func Test_ToCumulative_DeltaHistogramInTwoRequests_AccumulatesSumCountAndEveryBucket(t *testing.T) {
	projectID, totals := uuid.New(), newMemoryDeltaTotals()
	now := time.Now().UTC()

	convertAndAccumulate(t, totals, projectID, histogramMetric("latency", delta, now, 3, 30, []uint64{1, 1, 1}))
	second := convertAndAccumulate(t, totals, projectID,
		histogramMetric("latency", delta, now.Add(time.Second), 4, 70, []uint64{0, 2, 2}))

	byName := map[string]float64{}
	for _, sample := range second {
		byName[sample.MetricName+"/"+sample.Labels["le"]] = sample.Value
	}
	assert.Equal(t, map[string]float64{
		"latency_sum/":      100,
		"latency_count/":    7,
		"latency_bucket/10": 1,
		"latency_bucket/50": 4,
		// +Inf is the running total count.
		"latency_bucket/+Inf": 7,
	}, byName)
}

func Test_ToCumulative_MixedTemporality_OnlyDeltaPointsAccumulate(t *testing.T) {
	projectID, totals := uuid.New(), newMemoryDeltaTotals()
	now := time.Now().UTC()

	for i, wantCost := range []float64{2, 4} {
		at := now.Add(time.Duration(i) * time.Second)
		conversion := ConvertResourceMetrics(projectID, resourceMetrics(
			sumMetric("requests", cumulative, timedValue{at, 100}),
			sumMetric("cost", delta, timedValue{at, 2}),
		))
		require.Len(t, conversion.Delta, 1)
		_, err := toCumulative(context.Background(), totals, projectID, conversion.Delta)
		require.NoError(t, err)

		byName := map[string]float64{}
		for _, sample := range conversion.Samples {
			byName[sample.MetricName] = sample.Value
		}
		assert.InDelta(t, 100, byName["requests"], 0)
		assert.InDelta(t, wantCost, byName["cost"], 0)
	}
}

func Test_ToCumulative_OutOfOrderPointsInOneRequest_AddsOldestFirst(t *testing.T) {
	projectID, totals := uuid.New(), newMemoryDeltaTotals()
	now := time.Now().UTC()

	samples := convertAndAccumulate(t, totals, projectID, sumMetric("tokens", delta,
		timedValue{now.Add(2 * time.Second), 5},
		timedValue{now, 1},
		timedValue{now.Add(time.Second), 2},
	))

	byTime := map[time.Time]float64{}
	for _, sample := range samples {
		byTime[sample.Timestamp] = sample.Value
	}
	assert.InDelta(t, 1, byTime[now], 0)
	assert.InDelta(t, 3, byTime[now.Add(time.Second)], 0)
	assert.InDelta(t, 8, byTime[now.Add(2*time.Second)], 0)
}

func Test_ToCumulative_SameNameDifferentLabels_KeepsSeparateTotals(t *testing.T) {
	projectID, totals := uuid.New(), newMemoryDeltaTotals()
	now := time.Now().UTC()
	metric := sumMetric("tokens", delta, timedValue{now, 1}, timedValue{now, 10})
	metric.GetSum().DataPoints[1].Attributes = []*commonv1.KeyValue{stringAttr("type", "output")}

	samples := convertAndAccumulate(t, totals, projectID, metric)

	assert.ElementsMatch(t, []float64{1, 10}, values(samples))
}

func Test_ToCumulative_SameSeriesInAnotherProject_KeepsSeparateTotals(t *testing.T) {
	totals := newMemoryDeltaTotals()
	now := time.Now().UTC()

	convertAndAccumulate(t, totals, uuid.New(), sumMetric("tokens", delta, timedValue{now, 5}))
	other := convertAndAccumulate(t, totals, uuid.New(), sumMetric("tokens", delta, timedValue{now, 1}))

	assert.Equal(t, []float64{1}, values(other))
}

func Test_ConvertResourceMetrics_NonFiniteDelta_RejectedAndNotStored(t *testing.T) {
	now := time.Now().UTC()

	conversion := ConvertResourceMetrics(uuid.New(), resourceMetrics(
		sumMetric("tokens", delta, timedValue{now, math.NaN()}, timedValue{now, 2}),
	))

	assert.Equal(t, 1, conversion.Rejected)
	require.Len(t, conversion.Samples, 1)
	assert.Equal(t, conversion.Samples, conversion.Delta)
}

func Test_ConvertResourceMetrics_CumulativeOnly_HasNoDeltaSamples(t *testing.T) {
	now := time.Now().UTC()

	conversion := ConvertResourceMetrics(uuid.New(), resourceMetrics(
		sumMetric("requests", cumulative, timedValue{now, 7}),
		histogramMetric("latency", cumulative, now, 1, 5, []uint64{1, 0, 0}),
	))

	assert.Len(t, conversion.Samples, 6)
	assert.Empty(t, conversion.Delta)
}

func Test_ValkeyDeltaTotals_AddsInOrderAndRefreshesTTL(t *testing.T) {
	client := cache.GetCache()
	totals := NewValkeyDeltaTotals(client)
	a, b := "test:"+uuid.NewString(), "test:"+uuid.NewString()
	t.Cleanup(func() {
		client.Do(context.Background(), client.B().Del().Key(deltaKeyPrefix+a, deltaKeyPrefix+b).Build())
	})

	result, err := totals.Add(context.Background(), []DeltaIncrement{{a, 1}, {b, 10}, {a, 2.5}})
	require.NoError(t, err)
	assert.Equal(t, []float64{1, 10, 3.5}, result)

	result, err = totals.Add(context.Background(), []DeltaIncrement{{a, 4}})
	require.NoError(t, err)
	assert.Equal(t, []float64{7.5}, result)

	ttl, err := client.Do(context.Background(), client.B().Ttl().Key(deltaKeyPrefix+a).Build()).AsInt64()
	require.NoError(t, err)
	assert.InDelta(t, deltaTotalTTL.Seconds(), float64(ttl), 5)
}

// A total Valkey forgot (TTL expiry, restart) starts again from zero, which
// PromQL reads as a counter reset.
func Test_ValkeyDeltaTotals_WhenTotalIsGone_RestartsFromZero(t *testing.T) {
	client := cache.GetCache()
	totals := NewValkeyDeltaTotals(client)
	projectID := uuid.New()
	now := time.Now().UTC()
	metric := func(value float64) *metricsv1.Metric { return sumMetric("tokens", delta, timedValue{now, value}) }

	first := convertAndAccumulate(t, totals, projectID, metric(5))
	key := deltaKeyPrefix + deltaSeriesKey(projectID, first[0])
	t.Cleanup(func() { client.Do(context.Background(), client.B().Del().Key(key).Build()) })
	require.Equal(t, []float64{10}, values(convertAndAccumulate(t, totals, projectID, metric(5))))

	require.NoError(t, client.Do(context.Background(), client.B().Del().Key(key).Build()).Error())

	assert.Equal(t, []float64{2}, values(convertAndAccumulate(t, totals, projectID, metric(2))))
}

func newMemoryDeltaTotals() *memoryDeltaTotals {
	return &memoryDeltaTotals{totals: map[string]float64{}}
}

func convertAndAccumulate(
	t *testing.T,
	totals DeltaTotals,
	projectID uuid.UUID,
	metrics ...*metricsv1.Metric,
) []*metrics_core.MetricSample {
	t.Helper()
	conversion := ConvertResourceMetrics(projectID, resourceMetrics(metrics...))
	_, err := toCumulative(context.Background(), totals, projectID, conversion.Delta)
	require.NoError(t, err)
	return conversion.Samples
}

func values(samples []*metrics_core.MetricSample) []float64 {
	result := make([]float64, len(samples))
	for i, sample := range samples {
		result[i] = sample.Value
	}
	return result
}

func resourceMetrics(metrics ...*metricsv1.Metric) []*metricsv1.ResourceMetrics {
	return []*metricsv1.ResourceMetrics{{
		ScopeMetrics: []*metricsv1.ScopeMetrics{{Metrics: metrics}},
	}}
}

func sumMetric(
	name string,
	temporality metricsv1.AggregationTemporality,
	points ...timedValue,
) *metricsv1.Metric {
	dataPoints := make([]*metricsv1.NumberDataPoint, 0, len(points))
	for _, point := range points {
		dataPoints = append(dataPoints, &metricsv1.NumberDataPoint{
			TimeUnixNano: uint64(point.at.UnixNano()),
			Value:        &metricsv1.NumberDataPoint_AsDouble{AsDouble: point.value},
		})
	}
	return &metricsv1.Metric{Name: name, Data: &metricsv1.Metric_Sum{Sum: &metricsv1.Sum{
		AggregationTemporality: temporality,
		IsMonotonic:            true,
		DataPoints:             dataPoints,
	}}}
}

// histogramMetric has bounds 10 and 50; buckets holds the three per-bucket counts.
func histogramMetric(
	name string,
	temporality metricsv1.AggregationTemporality,
	at time.Time,
	count uint64,
	sum float64,
	buckets []uint64,
) *metricsv1.Metric {
	return &metricsv1.Metric{Name: name, Data: &metricsv1.Metric_Histogram{Histogram: &metricsv1.Histogram{
		AggregationTemporality: temporality,
		DataPoints: []*metricsv1.HistogramDataPoint{{
			TimeUnixNano:   uint64(at.UnixNano()),
			Count:          count,
			Sum:            float64Ptr(sum),
			ExplicitBounds: []float64{10, 50},
			BucketCounts:   buckets,
		}},
	}}}
}

func Test_ToCumulative_UndoAfterARefusedRequest_RetryCountsOnce(t *testing.T) {
	projectID, totals := uuid.New(), newMemoryDeltaTotals()
	now := time.Now().UTC()
	refused := ConvertResourceMetrics(projectID, resourceMetrics(sumMetric("tokens", delta, timedValue{now, 3})))
	undo, err := toCumulative(context.Background(), totals, projectID, refused.Delta)
	require.NoError(t, err)

	_, err = totals.Add(context.Background(), undo)
	require.NoError(t, err)
	retried := convertAndAccumulate(t, totals, projectID, sumMetric("tokens", delta, timedValue{now, 3}))

	assert.Equal(t, []float64{3}, values(retried))
}
