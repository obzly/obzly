package segments_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"obzly/internal/storage/segments"
	"obzly/internal/telemetry"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	_ "gocloud.dev/blob/memblob"
	_ "gocloud.dev/blob/s3blob"
)

func newMemObjectStore(t *testing.T) *segments.ObjectStore {
	t.Helper()

	store, err := segments.NewObjectStore(context.Background(), "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	return store
}

func writeLocalFile(t *testing.T, contents string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "segment.parquet")
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o644))
	return path
}

func Test_Upload_ThenDownload_RoundTripsFileContents(t *testing.T) {
	ctx := context.Background()
	store := newMemObjectStore(t)

	localPath := writeLocalFile(t, "fake parquet bytes")

	require.NoError(t, store.Upload(ctx, "logs/proj/seg.parquet", localPath))

	downloadPath := filepath.Join(t.TempDir(), "nested", "downloaded.parquet")
	require.NoError(t, store.Download(ctx, "logs/proj/seg.parquet", downloadPath))

	contents, err := os.ReadFile(downloadPath)
	require.NoError(t, err)
	require.Equal(t, "fake parquet bytes", string(contents))
}

func Test_Exists_ReflectsUploadAndDelete(t *testing.T) {
	ctx := context.Background()
	store := newMemObjectStore(t)
	localPath := writeLocalFile(t, "x")

	exists, err := store.Exists(ctx, "some/key.parquet")
	require.NoError(t, err)
	require.False(t, exists)

	require.NoError(t, store.Upload(ctx, "some/key.parquet", localPath))

	exists, err = store.Exists(ctx, "some/key.parquet")
	require.NoError(t, err)
	require.True(t, exists)

	require.NoError(t, store.Delete(ctx, "some/key.parquet"))

	exists, err = store.Exists(ctx, "some/key.parquet")
	require.NoError(t, err)
	require.False(t, exists)
}

func Test_Download_MissingKey_ReturnsError(t *testing.T) {
	store := newMemObjectStore(t)

	err := store.Download(context.Background(), "does/not/exist.parquet", filepath.Join(t.TempDir(), "out.parquet"))

	require.Error(t, err)
}

func Test_ObjectKeyFor_DatesByTheNewestRowInUTCAndAddsTheLevel(t *testing.T) {
	projectID := uuid.New()
	segmentID := uuid.New()
	auckland := time.FixedZone("NZDT", 13*3600)
	record := &segments.SegmentRecord{
		ID: segmentID, ProjectID: projectID, Stream: "logs", Level: segments.LevelCompacted,
		MinTime: time.Date(2026, 9, 30, 9, 0, 0, 0, time.UTC),
		MaxTime: time.Date(2026, 10, 2, 1, 0, 0, 0, auckland), // 2026-10-01 12:00 UTC
	}

	key := segments.ObjectKeyFor(record)

	require.Equal(t, "logs/"+projectID.String()+"/2026/10/01/1/"+segmentID.String()+".parquet", key)
}

func Test_DeleteBatch_RemovesEveryKeyAndIgnoresMissingOnes(t *testing.T) {
	store, err := segments.NewObjectStore(context.Background(), "mem://")
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	assertDeleteBatch(t, store)
}

// The DeleteObjects path; set OBZLY_TEST_S3_URL (e.g. a MinIO bucket URL)
// and AWS credentials to run it.
func Test_DeleteBatch_OnS3_RemovesEveryKeyAndIgnoresMissingOnes(t *testing.T) {
	bucketURL := os.Getenv("OBZLY_TEST_S3_URL")
	if bucketURL == "" {
		t.Skip("OBZLY_TEST_S3_URL not set")
	}
	store, err := segments.NewObjectStore(context.Background(), bucketURL)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	batches := telemetry.ObjectStoreRequests.WithLabelValues("delete_batch", telemetry.ResultOK)
	before := testutil.ToFloat64(batches)

	assertDeleteBatch(t, store)

	require.Equal(t, before+1, testutil.ToFloat64(batches), "one DeleteObjects request, not single deletes")
}

func assertDeleteBatch(t *testing.T, store *segments.ObjectStore) {
	t.Helper()
	ctx := context.Background()
	local := filepath.Join(t.TempDir(), "file.parquet")
	require.NoError(t, os.WriteFile(local, []byte("data"), 0o644))
	prefix := "delete-batch-test/" + uuid.NewString() + "/"
	var keys []string
	for i := range 5 {
		key := fmt.Sprintf("%s%d.parquet", prefix, i)
		require.NoError(t, store.Upload(ctx, key, local))
		keys = append(keys, key)
	}

	require.NoError(t, store.DeleteBatch(ctx, append(keys, prefix+"never-uploaded.parquet")))

	for _, key := range keys {
		exists, err := store.Exists(ctx, key)
		require.NoError(t, err)
		require.False(t, exists, key)
	}
}

// A download writes a temporary file and renames it, so the destination is
// either absent or complete, and no temporary file is left behind.
func Test_Download_LeavesOnlyTheCompleteFile(t *testing.T) {
	store := newMemObjectStore(t)
	ctx := context.Background()
	require.NoError(t, store.Upload(ctx, "metrics/p/seg.parquet", writeLocalFile(t, "segment bytes")))
	dir := t.TempDir()

	require.NoError(t, store.Download(ctx, "metrics/p/seg.parquet", filepath.Join(dir, "seg.parquet")))
	require.Error(t, store.Download(ctx, "metrics/p/missing.parquet", filepath.Join(dir, "missing.parquet")))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "seg.parquet", entries[0].Name())
}
