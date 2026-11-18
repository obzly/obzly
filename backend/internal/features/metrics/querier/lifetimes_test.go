package metrics_querier_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	metrics_core "obzly/internal/features/metrics/core"
	metrics_ingester "obzly/internal/features/metrics/ingester"
	metrics_querier "obzly/internal/features/metrics/querier"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_ListSeriesLifetimes_WhenSamplesOutgrowTheMemoryLimit_SpillsAndListsEverySeries(t *testing.T) {
	const seriesPerMetric, samplesPerSeries = 5000, 20
	names := []string{"k8s_pod_cpu_usage", "k8s_pod_memory_working_set", "k8s_container_restarts",
		"k8s_pod_network_io"}
	now := time.Now().UTC()
	ingester, err := metrics_ingester.NewMetricIngester(t.TempDir(), segments.WithMaxRows(100_000))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ingester.Close() })
	projectID := uuid.New()
	var samples []*metrics_core.MetricSample
	for s := range samplesPerSeries {
		for _, name := range names {
			for i := range seriesPerMetric {
				samples = append(samples, &metrics_core.MetricSample{ID: uuid.New(), ProjectID: projectID,
					MetricName: name, Labels: podLabels(i), Value: float64(s),
					Timestamp: now.Add(-time.Duration(samplesPerSeries-s) * time.Minute),
					Type:      metrics_core.MetricTypeGauge})
			}
		}
	}
	require.NoError(t, ingester.StoreSamplesBatch(map[uuid.UUID][]*metrics_core.MetricSample{projectID: samples}))
	_, err = ingester.FlushAll()
	require.NoError(t, err)

	// A worker's metrics handle, scaled down to the sample volume.
	querier, err := metrics_querier.NewMetricQuerier(metrics_querier.WithMemoryLimit("64MB"),
		metrics_querier.WithThreads(2), metrics_querier.WithQueryDir(t.TempDir()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = querier.Close() })

	lifetimes, err := querier.ListSeriesLifetimes(context.Background(), projectID,
		&metrics_core.SeriesLifetimesRequestDTO{MetricNames: names, ActiveSince: now.Add(-time.Hour),
			FirstSeenSince: now.Add(-30 * 24 * time.Hour)})
	require.NoError(t, err)

	assert.Len(t, lifetimes, len(names)*seriesPerMetric)
	for _, lifetime := range lifetimes {
		require.Equal(t, "prd", lifetime.Labels["k8s_cluster_name"])
		assert.WithinDuration(t, now.Add(-samplesPerSeries*time.Minute), lifetime.FirstSeen, time.Second)
		assert.WithinDuration(t, now.Add(-time.Minute), lifetime.LastSeen, time.Second)
	}
}

// podLabels is a pod's labels as a Kubernetes receiver sends them.
func podLabels(i int) map[string]string {
	return map[string]string{
		"k8s_cluster_name": "prd", "k8s_namespace_name": fmt.Sprintf("team-%d", i%40),
		"k8s_pod_name":        fmt.Sprintf("checkout-api-7d9f8b6c5d-%05d", i),
		"k8s_pod_uid":         uuid.NewSHA1(uuid.NameSpaceOID, fmt.Appendf(nil, "pod-%d", i)).String(),
		"k8s_node_name":       fmt.Sprintf("ip-10-0-%d-%d.eu-west-1.compute.internal", i%16, i%250),
		"k8s_deployment_name": "checkout-api", "k8s_replicaset_name": "checkout-api-7d9f8b6c5d",
		"container_name": "app", "service_name": "checkout-api", "cloud_region": "eu-west-1",
		"cloud_availability_zone": fmt.Sprintf("eu-west-1%c", 'a'+rune(i%3)),
		"host_name":               fmt.Sprintf("ip-10-0-%d-%d", i%16, i%250),
	}
}
