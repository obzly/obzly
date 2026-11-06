package logs_parquetstore_test

import (
	"context"
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

// A failing reconcile pass (a segment gone from disk and object storage)
// still backfills token filters.
func Test_ReconcileSegments_ReconcileFails_StillBackfillsTokenFilters(t *testing.T) {
	test_utils.UseIsolatedSegmentWriter(t)
	dataDir := t.TempDir()
	objects, err := segments.NewObjectStore(context.Background(), "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = objects.Close() })
	store, err := logs_parquetstore.New(dataDir, logs_parquetstore.WithObjectStore(objects, dataDir+"/cache"))
	require.NoError(t, err)
	projectID := uuid.New()
	require.NoError(t, store.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{projectID: {{
		ID: uuid.New(), ProjectID: projectID, Timestamp: time.Now().UTC(), Level: logs_core.LogLevelInfo,
		Message: "purchaseId=6ac2e65bcf50f676fdd207d8",
	}}}))
	require.NoError(t, store.ForceFlush())
	require.NoError(t, store.UploadPendingSegments())
	repo := &segments.SegmentRepository{}
	records, err := repo.ListByProject(projectID, "logs")
	require.NoError(t, err)
	require.Len(t, records, 1)
	// As if uploaded before filters existed.
	require.NoError(t, storage.GetDb().Exec(
		"UPDATE stream_segments SET stats = stats - 'token_bloom' WHERE id = ?", records[0].ID).Error)
	now := time.Now().UTC()
	broken := &segments.SegmentRecord{ID: uuid.New(), ProjectID: projectID, Stream: "logs",
		Path: dataDir + "/gone.parquet", ObjectKey: "logs/gone.parquet", UploadedAt: &now,
		MinTime: now, MaxTime: now, SchemaVersion: segments.CurrentSchemaVersion}
	require.NoError(t, storage.GetDb().Create(broken).Error)

	err = store.ReconcileSegments()

	require.Error(t, err, "the broken segment is still reported")
	records, err = repo.ListByProject(projectID, "logs")
	require.NoError(t, err)
	for _, record := range records {
		if record.ID != broken.ID {
			require.True(t, record.Stats != nil && record.Stats.TokenBloom, "backfilled despite the failure")
		}
	}
}
