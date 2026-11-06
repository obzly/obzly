package metrics_promql_test

import (
	"context"
	"testing"
	"time"

	metrics_core "obzly/internal/features/metrics/core"
	metrics_promql "obzly/internal/features/metrics/promql"
	metrics_testsupport "obzly/internal/features/metrics/testsupport"

	"github.com/google/uuid"
	"github.com/prometheus/prometheus/promql"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	metrics_testsupport.SetupRepository()
	m.Run()
}

// seedSamples stores and flushes samples for a fresh project through the
// real ingester and segment catalog.
func seedSamples(t *testing.T, samples []*metrics_core.MetricSample) uuid.UUID {
	t.Helper()

	projectID := uuid.New()
	for _, s := range samples {
		s.ProjectID = projectID
		if s.ID == uuid.Nil {
			s.ID = uuid.New()
		}
	}

	require.NoError(t, metrics_core.GetMetricCoreRepository().StoreSamplesBatch(
		map[uuid.UUID][]*metrics_core.MetricSample{projectID: samples},
	))
	require.NoError(t, metrics_core.GetMetricCoreRepository().ForceFlush())

	return projectID
}

// Test_PromqlEngine_SumByAndRate runs an instant aggregation and a rate()
// range query through the engine, adapter and real storage.
func Test_PromqlEngine_SumByAndRate(t *testing.T) {
	base := time.Now().UTC().Add(-10 * time.Minute)
	var samples []*metrics_core.MetricSample
	for i := 0; i < 5; i++ {
		ts := base.Add(time.Duration(i) * time.Minute)
		samples = append(samples,
			sampleAt("http_requests_total", map[string]string{"service": "api"}, float64(10*(i+1)), ts),
			sampleAt("http_requests_total", map[string]string{"service": "worker"}, float64(i+1), ts),
		)
	}
	projectID := seedSamples(t, samples)

	queryable := metrics_promql.NewQueryable(projectID)
	engine := metrics_promql.GetEngine()
	evalTime := base.Add(4 * time.Minute)

	instant, err := engine.NewInstantQuery(
		context.Background(), queryable, nil, `sum by (service) (http_requests_total)`, evalTime,
	)
	require.NoError(t, err)
	t.Cleanup(instant.Close)

	result := instant.Exec(context.Background())
	require.NoError(t, result.Err)

	vector, ok := result.Value.(promql.Vector)
	require.True(t, ok)
	require.Len(t, vector, 2)

	values := map[string]float64{}
	for _, sample := range vector {
		values[sample.Metric.Get("service")] = sample.F
	}
	require.InDelta(t, 40, values["api"], 0.001)
	require.InDelta(t, 4, values["worker"], 0.001)

	rangeQuery, err := engine.NewRangeQuery(
		context.Background(), queryable, nil,
		`rate(http_requests_total{service="api"}[5m])`, base, evalTime, time.Minute,
	)
	require.NoError(t, err)
	t.Cleanup(rangeQuery.Close)

	rangeResult := rangeQuery.Exec(context.Background())
	require.NoError(t, rangeResult.Err)

	matrix, ok := rangeResult.Value.(promql.Matrix)
	require.True(t, ok)
	require.Len(t, matrix, 1)
	require.NotEmpty(t, matrix[0].Floats)
}

func Test_PromqlEngine_LabelValues(t *testing.T) {
	base := time.Now().UTC().Add(-time.Minute)
	projectID := seedSamples(t, []*metrics_core.MetricSample{
		sampleAt("up", map[string]string{"service": "api"}, 1, base),
		sampleAt("up", map[string]string{"service": "worker"}, 1, base),
	})

	queryable := metrics_promql.NewQueryable(projectID)
	querier, err := queryable.Querier(base.Add(-time.Minute).UnixMilli(), time.Now().UTC().UnixMilli())
	require.NoError(t, err)
	t.Cleanup(func() { _ = querier.Close() })

	values, _, err := querier.LabelValues(context.Background(), "service", nil)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"api", "worker"}, values)
}

func sampleAt(name string, labels map[string]string, value float64, ts time.Time) *metrics_core.MetricSample {
	return &metrics_core.MetricSample{
		MetricName: name,
		Labels:     labels,
		Value:      value,
		Timestamp:  ts,
		Type:       metrics_core.MetricTypeCounter,
	}
}
