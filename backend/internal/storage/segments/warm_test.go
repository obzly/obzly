package segments_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	_ "gocloud.dev/blob/memblob"
)

func Test_WarmCache_DownloadsNewestFirstUpToTheBudget_ThenFindsThemCached(t *testing.T) {
	store, err := segments.NewObjectStore(context.Background(), "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	source := t.TempDir()
	var records []*segments.SegmentRecord
	for range 4 {
		record := &segments.SegmentRecord{ID: uuid.New(), ProjectID: uuid.New(), Stream: "logs",
			Path: filepath.Join(source, "missing.parquet"), SizeBytes: 100}
		record.ObjectKey = "logs/" + record.ID.String() + ".parquet"
		local := filepath.Join(source, record.ID.String())
		require.NoError(t, os.WriteFile(local, make([]byte, 100), 0o644))
		require.NoError(t, store.Upload(context.Background(), record.ObjectKey, local))
		records = append(records, record)
	}
	cacheDir := t.TempDir()

	first := segments.WarmCache(context.Background(), store, cacheDir, records, 250)
	second := segments.WarmCache(context.Background(), store, cacheDir, records, 250)

	require.Equal(t, segments.WarmResult{Downloaded: 2, Bytes: 200}, first)
	require.Equal(t, segments.WarmResult{Cached: 2, Bytes: 200}, second)
	for i, record := range records {
		_, err := os.Stat(filepath.Join(cacheDir, "logs", record.ProjectID.String(), record.ID.String()+".parquet"))
		require.Equal(t, i < 2, err == nil, "segment %d", i)
	}
}

// Regression: eviction ordered by download time deleted the newest data
// first (warmed first at start-up) and files a running query had just
// resolved. A cache hit now counts as use.
func Test_EvictColdCache_FileReadSinceDownload_OutlivesNewerUnreadOne(t *testing.T) {
	store, err := segments.NewObjectStore(context.Background(), "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	source, cacheDir := t.TempDir(), t.TempDir()
	projectID := uuid.New()
	var records []*segments.SegmentRecord
	var cached []string
	for i := range 2 {
		record := &segments.SegmentRecord{ID: uuid.New(), ProjectID: projectID, Stream: "logs",
			Path: filepath.Join(source, "missing.parquet"), SizeBytes: 100}
		record.ObjectKey = "logs/" + record.ID.String() + ".parquet"
		local := filepath.Join(source, record.ID.String())
		require.NoError(t, os.WriteFile(local, make([]byte, 100), 0o644))
		require.NoError(t, store.Upload(context.Background(), record.ObjectKey, local))
		records = append(records, record)
		path := filepath.Join(cacheDir, "logs", projectID.String(), record.ID.String()+".parquet")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, make([]byte, 100), 0o644))
		downloaded := time.Now().Add(-time.Duration(2-i) * time.Hour)
		require.NoError(t, os.Chtimes(path, downloaded, downloaded))
		cached = append(cached, path)
	}

	// The older download is read again (a warm-up or query hit).
	require.Equal(t, segments.WarmResult{Cached: 1, Bytes: 100},
		segments.WarmCache(context.Background(), store, cacheDir, records[:1], 100))
	require.NoError(t, segments.EvictColdCache(cacheDir, 150))

	require.FileExists(t, cached[0], "the file read just now must survive")
	require.NoFileExists(t, cached[1], "the file unread for an hour goes first")
}
