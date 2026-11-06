package metrics_querier_test

import (
	"context"
	"testing"
	"time"

	metrics_core "obzly/internal/features/metrics/core"
	metrics_ingester "obzly/internal/features/metrics/ingester"
	metrics_querier "obzly/internal/features/metrics/querier"
	"obzly/internal/storage/segments"
	"obzly/internal/telemetry"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func Test_SelectSeries_MetricNameEquality_SkipsSegmentsWithoutThatMetric(t *testing.T) {
	base := time.Now().UTC().Add(-time.Hour)
	ingester, err := metrics_ingester.NewMetricIngester(t.TempDir(), segments.WithMaxRows(1_000_000))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ingester.Close() })

	projectID := uuid.New()
	for _, name := range []string{"cpu_usage", "memory_bytes"} {
		batch := []*metrics_core.MetricSample{{
			ID: uuid.New(), ProjectID: projectID, MetricName: name,
			Labels: map[string]string{"env": "prod"}, Value: 1, Timestamp: base, Type: metrics_core.MetricTypeGauge,
		}}
		require.NoError(t, ingester.StoreSamplesBatch(map[uuid.UUID][]*metrics_core.MetricSample{projectID: batch}))
		_, err := ingester.FlushAll()
		require.NoError(t, err)
	}
	querier, err := metrics_querier.NewMetricQuerier()
	require.NoError(t, err)
	t.Cleanup(func() { _ = querier.Close() })
	pruned := func() float64 { return testutil.ToFloat64(telemetry.QuerySegmentsPruned.WithLabelValues("metrics")) }
	before := pruned()

	response, err := querier.SelectSeries(context.Background(), projectID, &metrics_core.SelectSeriesRequestDTO{
		Matchers: []metrics_core.MatcherDTO{{Name: "__name__", Type: metrics_core.MatchEqual, Value: "cpu_usage"}},
	})

	require.NoError(t, err)
	require.Len(t, response.Series, 1)
	require.Equal(t, "cpu_usage", response.Series[0].MetricName)
	require.Equal(t, before+1, pruned(), "memory_bytes' segment is never opened")
}
