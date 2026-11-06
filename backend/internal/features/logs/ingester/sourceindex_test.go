package logs_ingester_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"
	logs_ingester "obzly/internal/features/logs/ingester"
	"obzly/internal/storage"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type indexedSource struct {
	Attributes string
	FirstSeen  time.Time
	LastSeen   time.Time
}

func Test_StoreLogs_ThenFlush_RecordsEachSourceInIndex(t *testing.T) {
	ingester := newTestIngester(t)
	projectID := uuid.New()
	// A minute back, so "a second later" isn't a future timestamp (clamped).
	now := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)

	podFields := func(pod string) map[string]any {
		return map[string]any{
			"resource.service.name":        "checkout",
			"resource.k8s.namespace.name":  "shop",
			"resource.k8s.deployment.name": "checkout",
			"resource.k8s.pod.name":        pod,
			"user_id":                      "42",
		}
	}
	require.NoError(t, ingester.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{projectID: {
		logItem(projectID, now.Add(-time.Minute), podFields("checkout-7d9f8b6c5-abcde")),
		logItem(projectID, now, podFields("checkout-7d9f8b6c5-abcde")),
		logItem(projectID, now, podFields("checkout-7d9f8b6c5-fghij")),
		logItem(projectID, now, map[string]any{"user_id": "7"}),
	}}))
	_, err := ingester.FlushAll()
	require.NoError(t, err)

	sources := readSources(t, projectID)
	require.Len(t, sources, 2, "one row per pod; a log without source fields records nothing")
	pod := sourceFor(t, sources, "checkout-7d9f8b6c5-abcde")
	assert.WithinDuration(t, now.Add(-time.Minute), pod.FirstSeen, time.Millisecond)
	assert.WithinDuration(t, now, pod.LastSeen, time.Millisecond)
	var attributes map[string]string
	require.NoError(t, json.Unmarshal([]byte(pod.Attributes), &attributes))
	assert.Equal(t, map[string]string{
		"service_name":        "checkout",
		"k8s_namespace_name":  "shop",
		"k8s_deployment_name": "checkout",
		"k8s_pod_name":        "checkout-7d9f8b6c5-abcde",
	}, attributes)

	// A later batch widens the sighting both ways.
	require.NoError(t, ingester.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{projectID: {
		logItem(projectID, now.Add(-time.Hour), podFields("checkout-7d9f8b6c5-abcde")),
		logItem(projectID, now.Add(time.Second), podFields("checkout-7d9f8b6c5-abcde")),
	}}))
	_, err = ingester.FlushAll()
	require.NoError(t, err)

	sources = readSources(t, projectID)
	require.Len(t, sources, 2)
	pod = sourceFor(t, sources, "checkout-7d9f8b6c5-abcde")
	assert.WithinDuration(t, now.Add(-time.Hour), pod.FirstSeen, time.Millisecond)
	assert.WithinDuration(t, now.Add(time.Second), pod.LastSeen, time.Millisecond)

	// A clock-skewed sender's future timestamp counts as seen now.
	require.NoError(t, ingester.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{projectID: {
		logItem(projectID, now.Add(24*time.Hour), podFields("checkout-7d9f8b6c5-fghij")),
	}}))
	_, err = ingester.FlushAll()
	require.NoError(t, err)
	pod = sourceFor(t, readSources(t, projectID), "checkout-7d9f8b6c5-fghij")
	assert.WithinDuration(t, time.Now().UTC(), pod.LastSeen, 5*time.Second)
}

func Test_StoreLogs_WhenNotTheLogsStreamOrNotRecorded_RecordsNoSources(t *testing.T) {
	now := time.Now().UTC()
	fields := map[string]any{"service_name": "checkout"}

	t.Run("ai_logs", func(t *testing.T) {
		ingester, err := logs_ingester.NewLogIngesterForStream(t.TempDir(), logs_core.StreamAILogs)
		require.NoError(t, err)
		t.Cleanup(func() { _ = ingester.Close() })
		projectID := uuid.New()

		require.NoError(t, ingester.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{
			projectID: {logItem(projectID, now, fields)},
		}))
		_, err = ingester.FlushAll()
		require.NoError(t, err)
		assert.Empty(t, readSources(t, projectID))
	})

	t.Run("uncatalogued file", func(t *testing.T) {
		ingester := newTestIngester(t)
		projectID := uuid.New()

		require.NoError(t, ingester.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{
			projectID: {logItem(projectID, now, fields)},
		}))
		_, err := ingester.WriteFile(projectID)
		require.NoError(t, err)
		_, err = ingester.FlushAll()
		require.NoError(t, err)
		assert.Empty(t, readSources(t, projectID))
	})
}

// Ingest with and without the source index (the tail's writer has it off):
// the difference is its per-row cost.
func Benchmark_StoreLogsBatch_SourceIndex(b *testing.B) {
	now := time.Now().UTC()
	projectID := uuid.New()
	items := make([]*logs_core.LogItem, 1000)
	for i := range items {
		items[i] = logItem(projectID, now, map[string]any{
			"service_name":        "checkout",
			"k8s_cluster_name":    "contoso-prod",
			"k8s_namespace_name":  "shop",
			"k8s_deployment_name": "checkout",
			"k8s_container_name":  "app",
			"k8s_pod_name":        fmt.Sprintf("checkout-7d9f8b6c5-%05d", i%50),
			"k8s_node_name":       "node-1",
			"http_status":         "200",
		})
	}
	for _, enabled := range []bool{false, true} {
		b.Run(fmt.Sprintf("index=%v", enabled), func(b *testing.B) {
			ingester, err := logs_ingester.NewLogIngester(b.TempDir(), segments.WithMaxRows(1<<40))
			require.NoError(b, err)
			b.Cleanup(func() { _ = ingester.Close() })
			if !enabled {
				ingester.DisableSourceIndex()
			}
			batch := map[uuid.UUID][]*logs_core.LogItem{projectID: items}
			b.ResetTimer()
			for b.Loop() {
				if err := ingester.StoreLogsBatch(batch); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func logItem(projectID uuid.UUID, at time.Time, fields map[string]any) *logs_core.LogItem {
	return &logs_core.LogItem{
		ID:        uuid.New(),
		ProjectID: projectID,
		Timestamp: at,
		Level:     logs_core.LogLevelInfo,
		Message:   "request handled",
		Fields:    fields,
	}
}

func readSources(t *testing.T, projectID uuid.UUID) []indexedSource {
	t.Helper()
	var sources []indexedSource
	require.NoError(t, storage.GetDb().Raw(
		`SELECT attributes::text AS attributes, first_seen, last_seen FROM log_source_index WHERE project_id = ?`,
		projectID,
	).Scan(&sources).Error)
	return sources
}

func sourceFor(t *testing.T, sources []indexedSource, pod string) indexedSource {
	t.Helper()
	for _, source := range sources {
		var attributes map[string]string
		require.NoError(t, json.Unmarshal([]byte(source.Attributes), &attributes))
		if attributes["k8s_pod_name"] == pod {
			return source
		}
	}
	t.Fatalf("no source for pod %s", pod)
	return indexedSource{}
}
