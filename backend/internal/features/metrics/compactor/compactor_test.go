package metrics_compactor_test

import (
	"testing"
	"time"

	metrics_compactor "obzly/internal/features/metrics/compactor"
	metrics_core "obzly/internal/features/metrics/core"
	metrics_ingester "obzly/internal/features/metrics/ingester"
	"obzly/internal/storage/segments"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// seedTinySegments ingests one sample at a time with maxRows(1), so each
// flushes to its own segment file.
func seedTinySegments(t *testing.T, dataDir string, projectID uuid.UUID, count int, startTime time.Time) {
	t.Helper()

	ingester, err := metrics_ingester.NewMetricIngester(dataDir, segments.WithMaxRows(1))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ingester.Close() })

	for i := range count {
		start := startTime.Add(time.Duration(i) * time.Minute)
		s := &metrics_core.MetricSample{
			ID: uuid.New(), ProjectID: projectID,
			MetricName: "cpu_usage", Value: float64(i), Timestamp: start,
			Type: metrics_core.MetricTypeGauge,
		}
		require.NoError(t, ingester.StoreSamplesBatch(map[uuid.UUID][]*metrics_core.MetricSample{projectID: {s}}))
	}
}

func Test_EnforceRetention_DeletesSegmentsOlderThanCutoff(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()
	startTime := time.Now().UTC().Add(-48 * time.Hour)

	seedTinySegments(t, dataDir, projectID, 10, startTime)

	compactor, err := metrics_compactor.NewMetricCompactor(dataDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = compactor.Close() })

	deleted, err := compactor.EnforceRetention(projectID, 1)
	require.NoError(t, err)
	require.Equal(t, 20, deleted, "all 10 sample segments and their 10 series segments are older than 1 day")

	segmentRepo := &segments.SegmentRepository{}
	for _, stream := range []string{"metrics", "metrics_series"} {
		remaining, err := segmentRepo.ListByProject(projectID, stream)
		require.NoError(t, err)
		require.Empty(t, remaining, stream)
	}
}

func Test_EnforceRetention_ZeroMaxLifeDays_IsNoOp(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()
	seedTinySegments(t, dataDir, projectID, 3, time.Now().UTC().Add(-100*24*time.Hour))

	compactor, err := metrics_compactor.NewMetricCompactor(dataDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = compactor.Close() })

	deleted, err := compactor.EnforceRetention(projectID, 0)
	require.NoError(t, err)
	require.Equal(t, 0, deleted)
}

func Test_DeleteAllForProject_RemovesEverySegmentRegardlessOfAge(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()
	seedTinySegments(t, dataDir, projectID, 5, time.Now().UTC())

	compactor, err := metrics_compactor.NewMetricCompactor(dataDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = compactor.Close() })

	deleted, err := compactor.DeleteAllForProject(projectID)
	require.NoError(t, err)
	require.Equal(t, 10, deleted, "5 sample segments and their 5 series segments")

	segmentRepo := &segments.SegmentRepository{}
	for _, stream := range []string{"metrics", "metrics_series"} {
		remaining, err := segmentRepo.ListByProject(projectID, stream)
		require.NoError(t, err)
		require.Empty(t, remaining, stream)
	}
}

func Test_CompactSmallSegments_MergesConsecutiveSmallSegmentsIntoOne(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()
	seedTinySegments(t, dataDir, projectID, 4, time.Now().UTC())

	compactor, err := metrics_compactor.NewMetricCompactor(dataDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = compactor.Close() })

	test_utils.AgeSegments(t, projectID, "metrics", 48*time.Hour)
	result, err := compactor.CompactSmallSegments(projectID)
	require.NoError(t, err)
	require.Equal(t, 1, result.MergedGroups)
	require.Equal(t, 4, result.SegmentsRemoved)
	require.Equal(t, 1, result.SegmentsCreated)

	segmentRepo := &segments.SegmentRepository{}
	remaining, err := segmentRepo.ListByProject(projectID, "metrics")
	require.NoError(t, err)
	require.Len(t, remaining, 1)
	require.EqualValues(t, 4, remaining[0].RowCount)
}
