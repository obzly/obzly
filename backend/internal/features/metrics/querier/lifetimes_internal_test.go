package metrics_querier

import (
	"context"
	"fmt"
	"testing"
	"time"

	metrics_core "obzly/internal/features/metrics/core"
	metrics_ingester "obzly/internal/features/metrics/ingester"
	"obzly/internal/storage"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_ListSeriesLifetimes_WhenLabelsAreReadInSlices_ListsEachActiveSeriesOnce(t *testing.T) {
	previous := labelsChunkSeries
	labelsChunkSeries = 3
	t.Cleanup(func() { labelsChunkSeries = previous })

	now := time.Now().UTC()
	ingester, err := metrics_ingester.NewMetricIngester(t.TempDir(), segments.WithMaxRows(1_000_000))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ingester.Close() })
	projectID := uuid.New()
	var samples []*metrics_core.MetricSample
	for i := range 10 {
		pod := map[string]string{"k8s_pod_name": fmt.Sprintf("web-%d", i)}
		for _, at := range []time.Time{now.Add(-50 * time.Minute), now.Add(-time.Duration(i+1) * time.Minute)} {
			samples = append(samples, &metrics_core.MetricSample{ID: uuid.New(), ProjectID: projectID,
				MetricName: "k8s_pod_phase", Labels: pod, Value: 1, Timestamp: at, Type: metrics_core.MetricTypeGauge})
		}
	}
	// Quiet for two hours: not active, so left out.
	samples = append(samples, &metrics_core.MetricSample{ID: uuid.New(), ProjectID: projectID,
		MetricName: "k8s_pod_phase", Labels: map[string]string{"k8s_pod_name": "gone"}, Value: 1,
		Timestamp: now.Add(-2 * time.Hour), Type: metrics_core.MetricTypeGauge})
	require.NoError(t, ingester.StoreSamplesBatch(map[uuid.UUID][]*metrics_core.MetricSample{projectID: samples}))
	_, err = ingester.FlushAll()
	require.NoError(t, err)
	require.NoError(t, storage.GetDb().Exec("UPDATE metric_series_coverage SET since = now() WHERE project_id = ?", projectID).Error)
	querier, err := NewMetricQuerier(WithQueryDir(t.TempDir()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = querier.Close() })
	request := &metrics_core.SeriesLifetimesRequestDTO{MetricNames: []string{"k8s_pod_phase"},
		ActiveSince: now.Add(-time.Hour), FirstSeenSince: now.Add(-30 * 24 * time.Hour)}

	for _, source := range []string{"samples", "series stream"} {
		if source == "series stream" {
			require.NoError(t, storage.GetDb().Exec("UPDATE metric_series_coverage SET since = ? WHERE project_id = ?",
				now.Add(-48*time.Hour), projectID).Error)
		}
		lifetimes, err := querier.ListSeriesLifetimes(context.Background(), projectID, request)
		require.NoError(t, err)

		byPod := map[string]metrics_core.SeriesLifetimeDTO{}
		for _, lifetime := range lifetimes {
			byPod[lifetime.Labels["k8s_pod_name"]] = lifetime
		}
		require.Len(t, lifetimes, 10, source)
		require.Len(t, byPod, 10, source)
		for i := range 10 {
			lifetime := byPod[fmt.Sprintf("web-%d", i)]
			assert.WithinDuration(t, now.Add(-50*time.Minute), lifetime.FirstSeen, time.Second, source)
			assert.WithinDuration(t, now.Add(-time.Duration(i+1)*time.Minute), lifetime.LastSeen, time.Second, source)
		}
	}
}
