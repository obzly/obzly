package metrics_uploader_test

import (
	"context"
	"os"
	"testing"
	"time"

	metrics_core "obzly/internal/features/metrics/core"
	metrics_ingester "obzly/internal/features/metrics/ingester"
	metrics_uploader "obzly/internal/features/metrics/uploader"
	"obzly/internal/storage/segments"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	_ "gocloud.dev/blob/memblob"
)

// resetUploadedMetricsSegments clears uploaded rows ReconcileSegments would list (see the logs
// uploader test).
func resetUploadedMetricsSegments(t *testing.T) {
	t.Helper()

	repo := &segments.SegmentRepository{}
	var uploaded []*segments.SegmentRecord
	for _, stream := range []string{"metrics", "metrics_series"} {
		records, err := repo.ListUploaded(stream, 10_000)
		require.NoError(t, err)
		uploaded = append(uploaded, records...)
	}
	if len(uploaded) == 0 {
		return
	}

	ids := make([]uuid.UUID, len(uploaded))
	for i, record := range uploaded {
		ids[i] = record.ID
	}
	require.NoError(t, repo.DeleteByIDs(ids))
}

// seedOneSegment ingests one metric sample that flushes to its own segment (maxRows=1) and
// returns the project ID.
func seedOneSegment(t *testing.T, dataDir string) uuid.UUID {
	t.Helper()
	test_utils.UseIsolatedSegmentWriter(t)

	ingester, err := metrics_ingester.NewMetricIngester(dataDir, segments.WithMaxRows(1))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ingester.Close() })

	projectID := uuid.New()
	sample := &metrics_core.MetricSample{
		ID:         uuid.New(),
		ProjectID:  projectID,
		MetricName: "http_requests_total",
		Value:      1,
		Timestamp:  time.Now().UTC(),
		Type:       metrics_core.MetricTypeCounter,
	}
	require.NoError(
		t,
		ingester.StoreSamplesBatch(map[uuid.UUID][]*metrics_core.MetricSample{projectID: {sample}}),
	)

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

	uploader := metrics_uploader.NewMetricUploader(newMemStore(t))

	count, err := uploader.UploadPending(context.Background(), 10)

	require.NoError(t, err)
	require.GreaterOrEqual(t, count, 1)

	repo := &segments.SegmentRepository{}
	records, err := repo.ListByProject(projectID, "metrics")
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.NotNil(t, records[0].UploadedAt)
	require.NotEmpty(t, records[0].ObjectKey)
	require.FileExists(t, records[0].Path, "DeleteLocalAfterUpload defaults to false")
}

func Test_UploadPending_DeleteLocalAfterUpload_RemovesLocalFile(t *testing.T) {
	dataDir := t.TempDir()
	projectID := seedOneSegment(t, dataDir)

	uploader := metrics_uploader.NewMetricUploader(newMemStore(t))
	uploader.DeleteLocalAfterUpload = true

	_, err := uploader.UploadPending(context.Background(), 10)
	require.NoError(t, err)

	repo := &segments.SegmentRepository{}
	records, err := repo.ListByProject(projectID, "metrics")
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.NoFileExists(t, records[0].Path)
}

func Test_UploadPending_AlreadyUploadedSegments_AreNotReUploaded(t *testing.T) {
	dataDir := t.TempDir()
	projectID := seedOneSegment(t, dataDir)
	repo := &segments.SegmentRepository{}

	uploader := metrics_uploader.NewMetricUploader(newMemStore(t))

	_, err := uploader.UploadPending(context.Background(), 10)
	require.NoError(t, err)

	records, err := repo.ListByProject(projectID, "metrics")
	require.NoError(t, err)
	require.Len(t, records, 1)
	firstUploadedAt := records[0].UploadedAt
	require.NotNil(t, firstUploadedAt)

	_, err = uploader.UploadPending(context.Background(), 10)
	require.NoError(t, err)

	records, err = repo.ListByProject(projectID, "metrics")
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(
		t, firstUploadedAt.UnixNano(), records[0].UploadedAt.UnixNano(),
		"already-uploaded segments shouldn't be re-uploaded (uploaded_at should be unchanged)",
	)
}

func Test_UploadPending_NoSegments_ReturnsZero(t *testing.T) {
	test_utils.UseIsolatedSegmentWriter(t)
	uploader := metrics_uploader.NewMetricUploader(newMemStore(t))

	count, err := uploader.UploadPending(context.Background(), 10)

	require.NoError(t, err)
	require.Equal(t, 0, count)
}

func Test_PendingCount_ReflectsUnuploadedSegments(t *testing.T) {
	dataDir := t.TempDir()
	projectID := seedOneSegment(t, dataDir)

	uploader := metrics_uploader.NewMetricUploader(newMemStore(t))

	before, err := uploader.PendingCount(10)
	require.NoError(t, err)
	require.GreaterOrEqual(t, before, 1)

	_, err = uploader.UploadPending(context.Background(), 10)
	require.NoError(t, err)

	repo := &segments.SegmentRepository{}
	records, err := repo.ListByProject(projectID, "metrics")
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.NotNil(t, records[0].UploadedAt, "this test's own segment should no longer be pending")
}

func Test_ReconcileSegments_MissingLocalCopy_VerifiesRemoteWithoutDownloading(t *testing.T) {
	// ListUploaded is global: reset it first, locked for the whole test (see the logs uploader test).
	test_utils.AcquireExclusiveGlobalQueue(t, "metrics-uploaded-queue")
	resetUploadedMetricsSegments(t)

	dataDir := t.TempDir()
	projectID := seedOneSegment(t, dataDir)

	uploader := metrics_uploader.NewMetricUploader(newMemStore(t))
	_, err := uploader.UploadPending(context.Background(), 10)
	require.NoError(t, err)

	repo := &segments.SegmentRepository{}
	records, err := repo.ListByProject(projectID, "metrics")
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
