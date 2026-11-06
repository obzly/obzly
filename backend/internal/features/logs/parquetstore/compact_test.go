package logs_parquetstore_test

import (
	"context"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"
	logs_parquetstore "obzly/internal/features/logs/parquetstore"
	"obzly/internal/storage/segments"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	_ "gocloud.dev/blob/memblob"
)

// A merge is invisible to queries until uploaded, so compaction uploads it at
// once, before the slow rollup backfill.
func Test_CompactSmallSegments_UploadsTheMergeAtOnce_ThenBuildsItsCounts(t *testing.T) {
	test_utils.UseIsolatedSegmentWriter(t)
	dataDir := t.TempDir()
	objects, err := segments.NewObjectStore(context.Background(), "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = objects.Close() })
	store, err := logs_parquetstore.New(dataDir, logs_parquetstore.WithObjectStore(objects, dataDir+"/cache"))
	require.NoError(t, err)
	projectID := uuid.New()
	start := time.Now().UTC().Add(-48 * time.Hour)
	for i := range 3 {
		require.NoError(t, store.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{projectID: {{
			ID: uuid.New(), ProjectID: projectID, Timestamp: start.Add(time.Duration(i) * time.Minute),
			Level: logs_core.LogLevelInfo, Message: "payment accepted",
		}}}))
		require.NoError(t, store.ForceFlush())
	}
	require.NoError(t, store.UploadPendingSegments())
	test_utils.AgeSegments(t, projectID, logs_core.StreamLogs, 48*time.Hour)

	require.NoError(t, store.CompactSmallSegments(projectID))

	repo := &segments.SegmentRepository{}
	merged, err := repo.ListByProject(projectID, logs_core.StreamLogs)
	require.NoError(t, err)
	require.Len(t, merged, 1)
	require.Equal(t, segments.LevelCompacted, merged[0].Level)
	require.NotNil(t, merged[0].UploadedAt, "the merge is readable by every querier when compaction returns")
	rollups, err := repo.ListByProject(projectID, logs_core.StreamLogsRollup)
	require.NoError(t, err)
	require.Len(t, rollups, 1)
	require.Equal(t, merged[0].ID.String(), rollups[0].Stats.Source)
}
