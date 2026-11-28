package logs_parquetstore_test

import (
	"context"
	"os"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"
	logs_parquetstore "obzly/internal/features/logs/parquetstore"
	"obzly/internal/storage"
	"obzly/internal/storage/segments"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	_ "gocloud.dev/blob/memblob"
)

// A rollup left on a replaced worker's disk kept its catalog row, so its
// source counted as rolled up forever and the histogram undercounted.
func Test_ReconcileSegments_RollupOfAGoneWriter_IsRetiredAndRebuilt(t *testing.T) {
	store, projectID := compactedProjectWithRollup(t)
	repo := &segments.SegmentRepository{}
	rollups, err := repo.ListByProject(projectID, logs_core.StreamLogsRollup)
	require.NoError(t, err)
	require.Len(t, rollups, 1)
	lost := rollups[0]
	gone := "gone-" + uuid.NewString()
	require.NoError(t, segments.HeartbeatWriter(gone, time.Now().UTC().Add(-time.Hour)))
	require.NoError(t, storage.GetDb().Exec(`UPDATE stream_segments SET uploaded_at = NULL, writer_id = ?,
		created_at = now() - interval '2 hours' WHERE id = ?`, gone, lost.ID).Error)
	require.NoError(t, os.Remove(lost.Path))

	_ = store.ReconcileSegments()
	require.NoError(t, store.CompactSmallSegments(projectID))

	rollups, err = repo.ListByProject(projectID, logs_core.StreamLogsRollup)
	require.NoError(t, err)
	require.Len(t, rollups, 1)
	require.NotEqual(t, lost.ID, rollups[0].ID, "the lost rollup is rebuilt")
}

// A segment that fails to upload no longer keeps the pass's rollups on this
// worker's disk.
func Test_UploadPendingSegments_SegmentFails_StillUploadsRollups(t *testing.T) {
	store, projectID := compactedProjectWithRollup(t)
	repo := &segments.SegmentRepository{}
	rollups, err := repo.ListByProject(projectID, logs_core.StreamLogsRollup)
	require.NoError(t, err)
	require.NoError(t, storage.GetDb().Exec("UPDATE stream_segments SET uploaded_at = NULL WHERE id = ?",
		rollups[0].ID).Error)
	now := time.Now().UTC()
	require.NoError(t, repo.Create(&segments.SegmentRecord{ProjectID: projectID, Stream: logs_core.StreamLogs,
		Path: t.TempDir() + "/gone.parquet", MinTime: now, MaxTime: now,
		SchemaVersion: segments.CurrentSchemaVersion}))

	err = store.UploadPendingSegments()

	require.Error(t, err, "the broken segment is reported")
	rollups, err = repo.ListByProject(projectID, logs_core.StreamLogsRollup)
	require.NoError(t, err)
	require.NotNil(t, rollups[0].UploadedAt)
}

// compactedProjectWithRollup stores three flushes, uploads and compacts
// them: one uploaded L1 segment with its uploaded logs_1m rollup.
func compactedProjectWithRollup(t *testing.T) (*logs_parquetstore.LogParquetStore, uuid.UUID) {
	t.Helper()
	// Uploads logs segments: hold the lock the uploader tests take before
	// resetting every uploaded logs segment, or they delete this test's.
	test_utils.AcquireExclusiveGlobalQueue(t, "logs-uploaded-queue")
	test_utils.UseIsolatedSegmentWriter(t)
	dataDir := t.TempDir()
	objects, err := segments.NewObjectStore(context.Background(), "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = objects.Close() })
	store, err := logs_parquetstore.New(dataDir, logs_parquetstore.WithObjectStore(objects, dataDir+"/cache"))
	require.NoError(t, err)
	projectID := uuid.New()
	// Recent rows: an ended day's L1 would be rewritten cold by the next pass.
	start := time.Now().UTC().Add(-10 * time.Minute)
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
	require.NoError(t, store.UploadPendingSegments())
	return store, projectID
}
