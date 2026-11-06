package logs_uploader_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"
	logs_ingester "obzly/internal/features/logs/ingester"
	logs_uploader "obzly/internal/features/logs/uploader"
	"obzly/internal/storage/segments"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	_ "gocloud.dev/blob/memblob"
)

// resetUploadedLogsSegments clears uploaded rows ReconcileSegments would list: other tests'
// rows point at gone temp dirs and discarded mem:// buckets and would look like data loss.
func resetUploadedLogsSegments(t *testing.T) {
	t.Helper()

	repo := &segments.SegmentRepository{}
	uploaded, err := repo.ListUploaded("logs", 10_000)
	require.NoError(t, err)
	if len(uploaded) == 0 {
		return
	}

	ids := make([]uuid.UUID, len(uploaded))
	for i, record := range uploaded {
		ids[i] = record.ID
	}
	require.NoError(t, repo.DeleteByIDs(ids))
}

// seedOneSegment ingests one log item that flushes to its own segment (maxRows=1) and returns
// the project ID.
func seedOneSegment(t *testing.T, dataDir string) uuid.UUID {
	t.Helper()
	test_utils.UseIsolatedSegmentWriter(t)

	ingester, err := logs_ingester.NewLogIngester(dataDir, segments.WithMaxRows(1))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ingester.Close() })

	projectID := uuid.New()
	item := &logs_core.LogItem{
		ID:        uuid.New(),
		ProjectID: projectID,
		Timestamp: time.Now().UTC(),
		Level:     logs_core.LogLevelInfo,
		Message:   "msg",
	}
	require.NoError(t, ingester.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{projectID: {item}}))

	return projectID
}

func newMemStore(t *testing.T) *segments.ObjectStore {
	t.Helper()
	store, err := segments.NewObjectStore(context.Background(), "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func Test_UploadPending_UploadsAndMarksSegmentUploaded(t *testing.T) {
	dataDir := t.TempDir()
	projectID := seedOneSegment(t, dataDir)

	uploader := logs_uploader.NewLogUploader(newMemStore(t))

	// ListPendingUpload is global, so another package's test can add a pending segment in the
	// same window: assert on this test's segment, not the total UploadPending reports.
	count, err := uploader.UploadPending(context.Background(), 10)

	require.NoError(t, err)
	require.GreaterOrEqual(t, count, 1)

	repo := &segments.SegmentRepository{}
	records, err := repo.ListByProject(projectID, "logs")
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.NotNil(t, records[0].UploadedAt)
	require.NotEmpty(t, records[0].ObjectKey)
	require.FileExists(t, records[0].Path, "DeleteLocalAfterUpload defaults to false")
}

func Test_UploadPending_DeleteLocalAfterUpload_RemovesLocalFile(t *testing.T) {
	dataDir := t.TempDir()
	projectID := seedOneSegment(t, dataDir)

	uploader := logs_uploader.NewLogUploader(newMemStore(t))
	uploader.DeleteLocalAfterUpload = true

	_, err := uploader.UploadPending(context.Background(), 10)
	require.NoError(t, err)

	repo := &segments.SegmentRepository{}
	records, err := repo.ListByProject(projectID, "logs")
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.NoFileExists(t, records[0].Path)
}

func Test_UploadPending_AlreadyUploadedSegments_AreNotReUploaded(t *testing.T) {
	dataDir := t.TempDir()
	projectID := seedOneSegment(t, dataDir)
	repo := &segments.SegmentRepository{}

	uploader := logs_uploader.NewLogUploader(newMemStore(t))

	_, err := uploader.UploadPending(context.Background(), 10)
	require.NoError(t, err)

	records, err := repo.ListByProject(projectID, "logs")
	require.NoError(t, err)
	require.Len(t, records, 1)
	firstUploadedAt := records[0].UploadedAt
	require.NotNil(t, firstUploadedAt)

	// Check this test's own segment, not the global count (see above).
	_, err = uploader.UploadPending(context.Background(), 10)
	require.NoError(t, err)

	records, err = repo.ListByProject(projectID, "logs")
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(
		t, firstUploadedAt.UnixNano(), records[0].UploadedAt.UnixNano(),
		"already-uploaded segments shouldn't be re-uploaded (uploaded_at should be unchanged)",
	)
}

func Test_UploadPending_NoSegments_ReturnsZero(t *testing.T) {
	test_utils.UseIsolatedSegmentWriter(t)
	uploader := logs_uploader.NewLogUploader(newMemStore(t))

	count, err := uploader.UploadPending(context.Background(), 10)

	require.NoError(t, err)
	require.Equal(t, 0, count)
}

func Test_PendingCount_ReflectsUnuploadedSegments(t *testing.T) {
	dataDir := t.TempDir()
	projectID := seedOneSegment(t, dataDir)

	uploader := logs_uploader.NewLogUploader(newMemStore(t))

	// PendingCount/UploadPending scan globally, so check this test's own segment, not the counts.
	before, err := uploader.PendingCount(10)
	require.NoError(t, err)
	require.GreaterOrEqual(t, before, 1)

	_, err = uploader.UploadPending(context.Background(), 10)
	require.NoError(t, err)

	repo := &segments.SegmentRepository{}
	records, err := repo.ListByProject(projectID, "logs")
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.NotNil(t, records[0].UploadedAt, "this test's own segment should no longer be pending")
}

func Test_ReconcileSegments_MissingLocalCopy_VerifiesRemoteWithoutDownloading(t *testing.T) {
	// ListUploaded is global: clear other tests' uploaded rows first so reconcile sees only this
	// segment. Locked for the whole test, since the reset deletes rows other packages use.
	test_utils.AcquireExclusiveGlobalQueue(t, "logs-uploaded-queue")
	resetUploadedLogsSegments(t)

	dataDir := t.TempDir()
	projectID := seedOneSegment(t, dataDir)

	uploader := logs_uploader.NewLogUploader(newMemStore(t))
	_, err := uploader.UploadPending(context.Background(), 10)
	require.NoError(t, err)

	repo := &segments.SegmentRepository{}
	records, err := repo.ListByProject(projectID, "logs")
	require.NoError(t, err)
	require.Len(t, records, 1)
	localPath := records[0].Path

	require.NoError(t, os.Remove(localPath))
	require.NoFileExists(t, localPath)

	result, err := uploader.ReconcileSegments(context.Background(), 10)
	require.NoError(t, err)
	require.GreaterOrEqual(t, result.Verified, 1)
	require.Equal(t, 0, result.Unrecoverable)
	require.NoFileExists(t, localPath, "compaction downloads what it merges; reconcile must not")
}

// An api replica's segments must still upload when a worker meets 50 other replicas'
// rows ahead of them in the oldest-first queue.
func Test_UploadPending_WithLocalWriter_SkipsOtherReplicasRowsAndUploadsOwn(t *testing.T) {
	repo := &segments.SegmentRepository{}
	stream := "writer_test_" + uuid.NewString()[:8]
	projectID := uuid.New()
	old := time.Now().UTC().Add(-time.Hour)
	t.Cleanup(func() {
		segments.ResetLocalWriter()
		_, _ = repo.DeleteByProject(projectID, stream)
	})

	segments.SetLocalWriter("replica-b", false)
	for i := 0; i < 60; i++ {
		require.NoError(t, repo.Create(&segments.SegmentRecord{
			ProjectID: projectID, Stream: stream, Path: "/on/another/replica.parquet",
			MinTime: old, MaxTime: old, CreatedAt: old, SchemaVersion: segments.CurrentSchemaVersion,
		}))
	}

	segments.SetLocalWriter("replica-a", false)
	ownPath := filepath.Join(t.TempDir(), "own.parquet")
	require.NoError(t, os.WriteFile(ownPath, []byte("segment bytes"), 0o644))
	own := &segments.SegmentRecord{
		ProjectID: projectID, Stream: stream, Path: ownPath,
		MinTime: old, MaxTime: old, SchemaVersion: segments.CurrentSchemaVersion,
	}
	require.NoError(t, repo.Create(own))

	uploader := logs_uploader.NewLogUploaderForStream(newMemStore(t), stream)
	uploaded, err := uploader.UploadPending(context.Background(), 50)

	require.NoError(t, err, "other replicas' rows are not this replica's to upload")
	require.Equal(t, 1, uploaded)
	pending, err := repo.ListPendingUpload(stream, 100)
	require.NoError(t, err)
	require.Len(t, pending, 60, "only replica-b's rows remain pending")
	for _, record := range pending {
		require.NotEqual(t, own.ID, record.ID)
	}
}

// With token filters on, a segment goes up with its filter and the catalog
// says so - an ID search can then skip it.
func Test_Upload_WithTokenBlooms_UploadsTheFilterAndMarksTheSegment(t *testing.T) {
	dataDir := t.TempDir()
	projectID := seedOneSegment(t, dataDir)
	store := newMemStore(t)
	uploader := logs_uploader.NewLogUploader(store)
	blooms, err := segments.NewTokenBlooms(filepath.Join(dataDir, "spill"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = blooms.Close() })
	uploader.SetTokenBlooms(blooms)
	repo := &segments.SegmentRepository{}
	records, err := repo.ListByProject(projectID, "logs")
	require.NoError(t, err)

	require.NoError(t, uploader.Upload(context.Background(), records[0]))

	records, err = repo.ListByProject(projectID, "logs")
	require.NoError(t, err)
	require.NotNil(t, records[0].Stats)
	require.True(t, records[0].Stats.TokenBloom)
	exists, err := store.Exists(context.Background(), records[0].ObjectKey+segments.BloomSuffix)
	require.NoError(t, err)
	require.True(t, exists)
	require.FileExists(t, segments.BloomPath(records[0].Path))
}

// A segment uploaded before token filters existed gets one from the
// backfill - from object storage when its file is gone locally.
func Test_BackfillTokenBlooms_GivesAnUploadedSegmentWithoutAFilterOne(t *testing.T) {
	dataDir := t.TempDir()
	projectID := seedOneSegment(t, dataDir)
	store := newMemStore(t)
	plain := logs_uploader.NewLogUploader(store)
	repo := &segments.SegmentRepository{}
	records, err := repo.ListByProject(projectID, "logs")
	require.NoError(t, err)
	require.NoError(t, plain.Upload(context.Background(), records[0]))
	require.NoError(t, os.Remove(records[0].Path))
	withBlooms := logs_uploader.NewLogUploader(store)
	blooms, err := segments.NewTokenBlooms(filepath.Join(dataDir, "spill"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = blooms.Close() })
	withBlooms.SetTokenBlooms(blooms)

	// Other packages' tests share the catalog: backfill until this
	// project's segment is done.
	require.Eventually(t, func() bool {
		_, err := withBlooms.BackfillTokenBlooms(context.Background(), 50, t.TempDir())
		require.NoError(t, err)
		records, err = repo.ListByProject(projectID, "logs")
		require.NoError(t, err)
		return records[0].Stats != nil && records[0].Stats.TokenBloom
	}, 30*time.Second, 100*time.Millisecond)
	exists, err := store.Exists(context.Background(), records[0].ObjectKey+segments.BloomSuffix)
	require.NoError(t, err)
	require.True(t, exists)
}
