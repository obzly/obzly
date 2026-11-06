package segments_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"obzly/internal/storage/segments"
	"obzly/internal/telemetry"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func Test_WriterFlush_RecordsFlushAndEmptiesBufferGauges(t *testing.T) {
	schema := testSchema()
	schema.Stream = uniqueStream()
	writer, err := segments.NewWriter(t.TempDir(), schema, segments.WithMaxRows(1000))
	require.NoError(t, err)
	defer writer.Close()

	projectID := uuid.New()
	for i := 0; i < 3; i++ {
		_, err := writer.Append(projectID, []any{"a", time.Now(), int64(i)}, 10, time.Now())
		require.NoError(t, err)
	}

	require.Equal(t, 3.0, testutil.ToFloat64(telemetry.WriterBufferedRows.WithLabelValues(schema.Stream)))
	require.Equal(t, 30.0, testutil.ToFloat64(telemetry.WriterBufferedBytes.WithLabelValues(schema.Stream)))
	require.Equal(t, 3.0, testutil.ToFloat64(telemetry.WriterAppendedRows.WithLabelValues(schema.Stream)))

	_, err = writer.FlushAll()
	require.NoError(t, err)

	require.Equal(t, 0.0, testutil.ToFloat64(telemetry.WriterBufferedRows.WithLabelValues(schema.Stream)))
	require.Equal(t, 0.0, testutil.ToFloat64(telemetry.WriterBufferedBytes.WithLabelValues(schema.Stream)))
	require.Equal(t, 1.0, testutil.ToFloat64(
		telemetry.SegmentFlushes.WithLabelValues(schema.Stream, telemetry.ResultOK)))
	require.Equal(t, 3.0, testutil.ToFloat64(
		telemetry.SegmentFlushedRows.WithLabelValues(schema.Stream, telemetry.ResultOK)))
	require.Equal(t, 0.0, testutil.ToFloat64(
		telemetry.SegmentFlushes.WithLabelValues(schema.Stream, telemetry.ResultError)))
}

func Test_WriterFlush_WhenWriteFails_CountsLostRows(t *testing.T) {
	schema := testSchema()
	schema.Stream = uniqueStream()
	dataDir := t.TempDir()
	writer, err := segments.NewWriter(dataDir, schema, segments.WithMaxRows(1000))
	require.NoError(t, err)
	defer writer.Close()

	projectID := uuid.New()
	_, err = writer.Append(projectID, []any{"a", time.Now(), int64(1)}, 10, time.Now())
	require.NoError(t, err)

	// A regular file where the project's segment directory should go makes
	// the flush fail after the rows have left the buffer.
	projectDir := filepath.Join(dataDir, schema.Stream, projectID.String())
	require.NoError(t, os.WriteFile(projectDir, []byte("not a directory"), 0o644))

	_, err = writer.FlushProject(projectID)
	require.Error(t, err)

	require.Equal(t, 1.0, testutil.ToFloat64(
		telemetry.SegmentFlushes.WithLabelValues(schema.Stream, telemetry.ResultError)))
	require.Equal(t, 1.0, testutil.ToFloat64(
		telemetry.SegmentFlushedRows.WithLabelValues(schema.Stream, telemetry.ResultError)))
	require.Equal(t, 0.0, testutil.ToFloat64(telemetry.WriterBufferedRows.WithLabelValues(schema.Stream)))
}

func Test_ResolveFiles_RecordsWhereEachSegmentWasFound(t *testing.T) {
	ctx := context.Background()
	store := newMemObjectStore(t)
	stream := uniqueStream()
	projectID := uuid.New()

	localPath := writeLocalFile(t, "local segment")
	local := &segments.SegmentRecord{
		ID: uuid.New(), ProjectID: projectID, Stream: stream, Path: localPath, SizeBytes: 100,
	}

	coldPath := writeLocalFile(t, "cold segment")
	cold := &segments.SegmentRecord{
		ID: uuid.New(), ProjectID: projectID, Stream: stream, Path: "/gone/cold.parquet",
		ObjectKey: stream + "/cold.parquet", SizeBytes: int64(len("cold segment")),
	}
	require.NoError(t, store.Upload(ctx, cold.ObjectKey, coldPath))

	// Written on another replica and not uploaded yet.
	pending := &segments.SegmentRecord{
		ID: uuid.New(), ProjectID: projectID, Stream: stream, Path: "/gone/pending.parquet", SizeBytes: 400,
	}

	cacheDir := t.TempDir()
	files := segments.ResolveFiles(ctx, store, cacheDir, []*segments.SegmentRecord{local, cold, pending})
	require.Len(t, files, 2, "the pending segment is skipped, not fatal")

	// Second read of the cold segment comes from the local cache.
	files = segments.ResolveFiles(ctx, store, cacheDir, []*segments.SegmentRecord{cold})
	require.Len(t, files, 1)

	reads := func(source string) float64 {
		return testutil.ToFloat64(telemetry.QuerySegmentReads.WithLabelValues(stream, source))
	}
	require.Equal(t, 1.0, reads(telemetry.SourceLocal))
	require.Equal(t, 1.0, reads(telemetry.SourceObjectStore))
	require.Equal(t, 1.0, reads(telemetry.SourceCache))
	require.Equal(t, 1.0, reads(telemetry.SourcePending))
	require.Equal(t, 124.0, testutil.ToFloat64(telemetry.QueryScannedBytes.WithLabelValues(stream)),
		"scanned bytes count only segments actually opened")
}

func Test_ObjectStoreOps_CountRequestsByOperationAndResult(t *testing.T) {
	ctx := context.Background()
	store := newMemObjectStore(t)
	requests := func(op, result string) float64 {
		return testutil.ToFloat64(telemetry.ObjectStoreRequests.WithLabelValues(op, result))
	}
	uploadOK := requests("upload", telemetry.ResultOK)
	uploadBytes := testutil.ToFloat64(telemetry.ObjectStoreBytes.WithLabelValues("upload"))
	downloadErr := requests("download", telemetry.ResultError)
	deleteOK := requests("delete", telemetry.ResultOK)

	contents := "twelve bytes"
	require.NoError(t, store.Upload(ctx, "k/seg.parquet", writeLocalFile(t, contents)))
	require.Error(t, store.Download(ctx, "k/absent.parquet", filepath.Join(t.TempDir(), "x.parquet")))
	require.NoError(t, store.Delete(ctx, "k/absent.parquet"), "deleting a missing key is not an error")

	require.Equal(t, uploadOK+1, requests("upload", telemetry.ResultOK))
	require.Equal(t, uploadBytes+float64(len(contents)),
		testutil.ToFloat64(telemetry.ObjectStoreBytes.WithLabelValues("upload")))
	require.Equal(t, downloadErr+1, requests("download", telemetry.ResultError))
	require.Equal(t, deleteOK+1, requests("delete", telemetry.ResultOK))
}

func uniqueStream() string {
	return "telemetry_" + uuid.NewString()[:8]
}

// A cached copy of the wrong size (cut short by a restart mid-download)
// is fetched again instead of failing every query that reads it.
func Test_ResolveFiles_TruncatedCacheCopy_IsDownloadedAgain(t *testing.T) {
	ctx := context.Background()
	store := newMemObjectStore(t)
	stream := uniqueStream()
	content := "the whole cold segment"
	record := &segments.SegmentRecord{
		ID: uuid.New(), ProjectID: uuid.New(), Stream: stream, Path: "/gone/cold.parquet",
		ObjectKey: stream + "/cold.parquet", SizeBytes: int64(len(content)),
	}
	require.NoError(t, store.Upload(ctx, record.ObjectKey, writeLocalFile(t, content)))
	cacheDir := t.TempDir()
	cachePath := filepath.Join(cacheDir, stream, record.ProjectID.String(), record.ID.String()+".parquet")
	require.NoError(t, os.MkdirAll(filepath.Dir(cachePath), 0o755))
	require.NoError(t, os.WriteFile(cachePath, []byte(content[:5]), 0o644))

	files := segments.ResolveFiles(ctx, store, cacheDir, []*segments.SegmentRecord{record})
	require.Len(t, files, 1)
	got, err := os.ReadFile(files[0].Path)
	require.NoError(t, err)
	require.Equal(t, content, string(got))
	require.Equal(
		t,
		1.0,
		testutil.ToFloat64(telemetry.QuerySegmentReads.WithLabelValues(stream, telemetry.SourceObjectStore)),
	)
}
