package segments_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// Incus buckets (and GCS's S3 interop) have no DeleteObjects: compaction,
// retention and project deletion must still remove objects.
func Test_DeleteBatch_WhenTheStoreRefusesBatchDeletes_DeletesOneByOne(t *testing.T) {
	fake := newNoBatchDeleteS3(t)
	store, err := segments.NewObjectStore(context.Background(), fake.bucketURL())
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	assertDeleteBatch(t, store)
	assertDeleteBatch(t, store)

	require.Equal(t, 1, fake.batchAttempts(), "a refused DeleteObjects isn't tried again")
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

// noBatchDeleteS3 is just enough of an S3 endpoint for Upload, Exists and
// Delete, answering DeleteObjects the way an Incus bucket does.
type noBatchDeleteS3 struct {
	server  *httptest.Server
	mu      sync.Mutex
	objects map[string]bool
	batches int
}

func newNoBatchDeleteS3(t *testing.T) *noBatchDeleteS3 {
	t.Helper()
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	fake := &noBatchDeleteS3{objects: map[string]bool{}}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *noBatchDeleteS3) bucketURL() string {
	return "s3://lab-bucket?endpoint=" + url.QueryEscape(f.server.URL) +
		"&region=us-east-1&use_path_style=true&disable_https=true&hostname_immutable=true"
}

func (f *noBatchDeleteS3) batchAttempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.batches
}

func (f *noBatchDeleteS3) serve(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	key := strings.TrimPrefix(r.URL.Path, "/lab-bucket/")
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && r.URL.Query().Has("delete"):
		f.batches++
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>InvalidRequest</Code>`+
			`<Message>Bucket lifecycle is managed by the Incus API.</Message></Error>`)
	case r.Method == http.MethodPut:
		f.objects[key] = true
		w.Header().Set("ETag", `"0"`)
	case r.Method == http.MethodHead && f.objects[key]:
		w.Header().Set("Content-Length", "4")
		w.Header().Set("ETag", `"0"`)
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
	case r.Method == http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// A flat 30 s needed 33 MB/s to upload a 1 GB cold segment: slower stores
// never finished one.
func Test_TransferTimeout_GrowsWithTheObjectsSize(t *testing.T) {
	require.Equal(t, 30*time.Second, segments.TransferTimeout(0))
	require.Equal(t, 30*time.Second+1024*time.Second, segments.TransferTimeout(1<<30))
}

// Column chunks stream into the cache file at their offset rather than
// through memory.
func Test_CopyRange_WritesTheRangeAtItsOffset(t *testing.T) {
	store := newMemObjectStore(t)
	ctx := context.Background()
	require.NoError(t, store.Upload(ctx, "logs/range.parquet", writeLocalFile(t, "0123456789abcdef")))
	file, err := os.Create(filepath.Join(t.TempDir(), "partial.parquet"))
	require.NoError(t, err)
	defer func() { _ = file.Close() }()
	require.NoError(t, file.Truncate(16))

	require.NoError(t, store.CopyRange(ctx, "logs/range.parquet", 4, 6, file))

	got := make([]byte, 16)
	_, err = file.ReadAt(got, 0)
	require.NoError(t, err)
	require.Equal(t, "\x00\x00\x00\x00456789\x00\x00\x00\x00\x00\x00", string(got))
	require.Error(t, store.CopyRange(ctx, "logs/range.parquet", 12, 10, file), "a short range is an error")
}
