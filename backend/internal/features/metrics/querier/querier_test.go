package metrics_querier_test

import (
	"context"
	"testing"
	"time"

	metrics_core "obzly/internal/features/metrics/core"
	metrics_ingester "obzly/internal/features/metrics/ingester"
	metrics_querier "obzly/internal/features/metrics/querier"
	"obzly/internal/storage"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedProject ingests through the real ingester, so tests exercise
// write->catalog->read end to end.
func seedProject(t *testing.T, samples []*metrics_core.MetricSample) (uuid.UUID, *metrics_querier.MetricQuerier) {
	t.Helper()

	ingester, err := metrics_ingester.NewMetricIngester(t.TempDir(), segments.WithMaxRows(1_000_000))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ingester.Close() })

	projectID := uuid.New()
	for _, sample := range samples {
		sample.ProjectID = projectID
		if sample.ID == uuid.Nil {
			sample.ID = uuid.New()
		}
	}

	require.NoError(t, ingester.StoreSamplesBatch(map[uuid.UUID][]*metrics_core.MetricSample{projectID: samples}))
	_, err = ingester.FlushAll()
	require.NoError(t, err)

	querier, err := metrics_querier.NewMetricQuerier()
	require.NoError(t, err)
	t.Cleanup(func() { _ = querier.Close() })

	return projectID, querier
}

func sample(
	name string,
	labels map[string]string,
	value float64,
	ts time.Time,
	typ metrics_core.MetricType,
) *metrics_core.MetricSample {
	return &metrics_core.MetricSample{MetricName: name, Labels: labels, Value: value, Timestamp: ts, Type: typ}
}

func Test_SelectSeries_ReturnsOneSeriesPerDistinctLabelSet(t *testing.T) {
	base := time.Now().UTC().Add(-time.Hour)
	projectID, querier := seedProject(t, []*metrics_core.MetricSample{
		sample(
			"http_requests_total",
			map[string]string{"service": "api", "code": "200"},
			10,
			base,
			metrics_core.MetricTypeCounter,
		),
		sample(
			"http_requests_total",
			map[string]string{"service": "api", "code": "200"},
			20,
			base.Add(time.Minute),
			metrics_core.MetricTypeCounter,
		),
		sample(
			"http_requests_total",
			map[string]string{"service": "worker", "code": "500"},
			1,
			base,
			metrics_core.MetricTypeCounter,
		),
	})

	response, err := querier.SelectSeries(context.Background(), projectID, &metrics_core.SelectSeriesRequestDTO{
		Matchers: []metrics_core.MatcherDTO{
			{Name: "__name__", Type: metrics_core.MatchEqual, Value: "http_requests_total"},
		},
	})
	require.NoError(t, err)
	require.Len(t, response.Series, 2)

	byService := map[string]metrics_core.SeriesDTO{}
	for _, s := range response.Series {
		byService[s.Labels["service"]] = s
	}

	apiSeries := byService["api"]
	require.Len(t, apiSeries.Samples, 2)
	assert.Equal(t, float64(10), apiSeries.Samples[0].Value)
	assert.Equal(t, float64(20), apiSeries.Samples[1].Value)

	workerSeries := byService["worker"]
	require.Len(t, workerSeries.Samples, 1)
	assert.Equal(t, float64(1), workerSeries.Samples[0].Value)
}

func Test_SelectSeries_FiltersByLabelEquality(t *testing.T) {
	base := time.Now().UTC().Add(-time.Hour)
	projectID, querier := seedProject(t, []*metrics_core.MetricSample{
		sample("cpu_usage", map[string]string{"env": "prod"}, 1, base, metrics_core.MetricTypeGauge),
		sample("cpu_usage", map[string]string{"env": "staging"}, 2, base, metrics_core.MetricTypeGauge),
	})

	response, err := querier.SelectSeries(context.Background(), projectID, &metrics_core.SelectSeriesRequestDTO{
		Matchers: []metrics_core.MatcherDTO{
			{Name: "__name__", Type: metrics_core.MatchEqual, Value: "cpu_usage"},
			{Name: "env", Type: metrics_core.MatchEqual, Value: "prod"},
		},
	})
	require.NoError(t, err)
	require.Len(t, response.Series, 1)
	assert.Equal(t, "prod", response.Series[0].Labels["env"])
}

func Test_SelectSeries_FiltersByRegexNotMatch(t *testing.T) {
	base := time.Now().UTC().Add(-time.Hour)
	projectID, querier := seedProject(t, []*metrics_core.MetricSample{
		sample("cpu_usage", map[string]string{"env": "prod"}, 1, base, metrics_core.MetricTypeGauge),
		sample("cpu_usage", map[string]string{"env": "staging"}, 2, base, metrics_core.MetricTypeGauge),
		sample("cpu_usage", map[string]string{"env": "dev"}, 3, base, metrics_core.MetricTypeGauge),
	})

	response, err := querier.SelectSeries(context.Background(), projectID, &metrics_core.SelectSeriesRequestDTO{
		Matchers: []metrics_core.MatcherDTO{
			{Name: "__name__", Type: metrics_core.MatchEqual, Value: "cpu_usage"},
			{Name: "env", Type: metrics_core.MatchNotRegexp, Value: "prod|staging"},
		},
	})
	require.NoError(t, err)
	require.Len(t, response.Series, 1)
	assert.Equal(t, "dev", response.Series[0].Labels["env"])
}

func Test_SelectSeries_AbsentLabelTreatedAsEmptyString(t *testing.T) {
	base := time.Now().UTC().Add(-time.Hour)
	projectID, querier := seedProject(t, []*metrics_core.MetricSample{
		sample("up", map[string]string{"service": "api"}, 1, base, metrics_core.MetricTypeGauge),
		sample("up", nil, 1, base, metrics_core.MetricTypeGauge),
	})

	response, err := querier.SelectSeries(context.Background(), projectID, &metrics_core.SelectSeriesRequestDTO{
		Matchers: []metrics_core.MatcherDTO{
			{Name: "__name__", Type: metrics_core.MatchEqual, Value: "up"},
			{Name: "service", Type: metrics_core.MatchNotEqual, Value: "api"},
		},
	})
	require.NoError(t, err)
	require.Len(t, response.Series, 1)
	assert.Empty(t, response.Series[0].Labels["service"])
}

func Test_ListMetricNames_ReturnsDistinctNamesWithType(t *testing.T) {
	base := time.Now().UTC().Add(-time.Hour)
	projectID, querier := seedProject(t, []*metrics_core.MetricSample{
		sample("http_requests_total", nil, 1, base, metrics_core.MetricTypeCounter),
		sample("cpu_usage", nil, 1, base, metrics_core.MetricTypeGauge),
	})

	response, err := querier.ListMetricNames(context.Background(), projectID, nil)
	require.NoError(t, err)
	require.Len(t, response.Metrics, 2)

	byName := map[string]metrics_core.MetricType{}
	for _, m := range response.Metrics {
		byName[m.Name] = m.Type
	}
	assert.Equal(t, metrics_core.MetricTypeCounter, byName["http_requests_total"])
	assert.Equal(t, metrics_core.MetricTypeGauge, byName["cpu_usage"])
}

func Test_ListLabelNames_IncludesNameAndEveryLabelKey(t *testing.T) {
	base := time.Now().UTC().Add(-time.Hour)
	projectID, querier := seedProject(t, []*metrics_core.MetricSample{
		sample(
			"http_requests_total",
			map[string]string{"service": "api", "code": "200"},
			1,
			base,
			metrics_core.MetricTypeCounter,
		),
	})

	response, err := querier.ListLabelNames(context.Background(), projectID, &metrics_core.ListLabelNamesRequestDTO{})
	require.NoError(t, err)
	assert.Contains(t, response.Names, "__name__")
	assert.Contains(t, response.Names, "service")
	assert.Contains(t, response.Names, "code")
}

func Test_ListLabelValues_ScopedByMatchers(t *testing.T) {
	base := time.Now().UTC().Add(-time.Hour)
	projectID, querier := seedProject(t, []*metrics_core.MetricSample{
		sample("http_requests_total", map[string]string{"service": "api"}, 1, base, metrics_core.MetricTypeCounter),
		sample("http_requests_total", map[string]string{"service": "worker"}, 1, base, metrics_core.MetricTypeCounter),
		sample("cpu_usage", map[string]string{"service": "batch"}, 1, base, metrics_core.MetricTypeGauge),
	})

	response, err := querier.ListLabelValues(context.Background(), projectID, &metrics_core.ListLabelValuesRequestDTO{
		LabelName: "service",
		Matchers: []metrics_core.MatcherDTO{
			{Name: "__name__", Type: metrics_core.MatchEqual, Value: "http_requests_total"},
		},
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"api", "worker"}, response.Values)
}

func Test_SelectSeries_NoSegments_ReturnsEmpty(t *testing.T) {
	querier, err := metrics_querier.NewMetricQuerier()
	require.NoError(t, err)
	t.Cleanup(func() { _ = querier.Close() })

	response, err := querier.SelectSeries(context.Background(), uuid.New(), &metrics_core.SelectSeriesRequestDTO{})
	require.NoError(t, err)
	assert.Empty(t, response.Series)
}

func Test_ListSeriesLifetimes_ActiveSeries_ReturnFirstAndLastSeen(t *testing.T) {
	now := time.Now().UTC()
	pod := map[string]string{"k8s_pod_name": "web-1"}
	gone := map[string]string{"k8s_pod_name": "web-0"}
	projectID, querier := seedProject(t, []*metrics_core.MetricSample{
		sample("k8s_pod_memory_working_set", pod, 1, now.Add(-3*time.Hour), metrics_core.MetricTypeGauge),
		sample("k8s_pod_memory_working_set", pod, 1, now.Add(-30*time.Minute), metrics_core.MetricTypeGauge),
		sample("k8s_pod_memory_working_set", pod, 1, now.Add(-5*time.Minute), metrics_core.MetricTypeGauge),
		sample("k8s_pod_memory_working_set", gone, 1, now.Add(-2*time.Hour), metrics_core.MetricTypeGauge),
		sample("http_requests_total", pod, 1, now.Add(-time.Minute), metrics_core.MetricTypeCounter),
	})
	request := &metrics_core.SeriesLifetimesRequestDTO{
		MetricNames: []string{"k8s_pod_memory_working_set"}, ActiveSince: now.Add(-time.Hour),
		FirstSeenSince: now.Add(-30 * 24 * time.Hour),
	}

	// Series tracking started just now: the samples of the last hour answer.
	fromSamples, err := querier.ListSeriesLifetimes(context.Background(), projectID, request)
	require.NoError(t, err)
	require.Len(t, fromSamples, 1, "only series seen in the last hour, of the asked metrics")
	assert.Equal(t, "web-1", fromSamples[0].Labels["k8s_pod_name"])
	assert.WithinDuration(t, now.Add(-30*time.Minute), fromSamples[0].FirstSeen, time.Second)
	assert.WithinDuration(t, now.Add(-5*time.Minute), fromSamples[0].LastSeen, time.Second)

	// Once the series stream covers the window it gives the real first seen.
	require.NoError(t, storage.GetDb().Exec("UPDATE metric_series_coverage SET since = ? WHERE project_id = ?",
		now.Add(-48*time.Hour), projectID).Error)
	fromSeries, err := querier.ListSeriesLifetimes(context.Background(), projectID, request)
	require.NoError(t, err)
	require.Len(t, fromSeries, 1)
	assert.WithinDuration(t, now.Add(-3*time.Hour), fromSeries[0].FirstSeen, time.Second)
	assert.WithinDuration(t, now.Add(-5*time.Minute), fromSeries[0].LastSeen, time.Second)
}
