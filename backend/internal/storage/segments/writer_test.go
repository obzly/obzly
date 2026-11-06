package segments_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	_ "github.com/marcboeker/go-duckdb/v2"
	"github.com/stretchr/testify/require"
)

func testSchema() segments.Schema {
	return segments.Schema{
		Stream: "test_stream",
		Columns: []segments.ColumnDef{
			{Name: "id", Type: "VARCHAR"},
			{Name: "ts", Type: "TIMESTAMP"},
			{Name: "n", Type: "BIGINT"},
		},
	}
}

func rowCountIn(t *testing.T, path string) int64 {
	t.Helper()

	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer db.Close()

	var count int64
	err = db.QueryRow("SELECT count(*) FROM read_parquet(?)", path).Scan(&count)
	require.NoError(t, err)
	return count
}

func Test_Append_BelowThreshold_DoesNotAutoFlush(t *testing.T) {
	writer, err := segments.NewWriter(t.TempDir(), testSchema(), segments.WithMaxRows(1000))
	require.NoError(t, err)
	defer writer.Close()

	projectID := uuid.New()
	segment, err := writer.Append(projectID, []any{"a", time.Now(), int64(1)}, 32, time.Now())

	require.NoError(t, err)
	require.Nil(t, segment, "should not auto-flush below the row threshold")
}

func Test_Append_CrossingRowThreshold_AutoFlushesAndWritesReadableParquet(t *testing.T) {
	writer, err := segments.NewWriter(t.TempDir(), testSchema(), segments.WithMaxRows(3))
	require.NoError(t, err)
	defer writer.Close()

	projectID := uuid.New()
	now := time.Now()

	var lastSegment *segments.Segment
	for i := range 3 {
		segment, err := writer.Append(
			projectID,
			[]any{uuid.NewString(), now.Add(time.Duration(i) * time.Second), int64(i)},
			32,
			now,
		)
		require.NoError(t, err)
		lastSegment = segment
	}

	require.NotNil(t, lastSegment, "third append should cross the row threshold and trigger a flush")
	require.Equal(t, 3, lastSegment.RowCount)
	require.Equal(t, projectID, lastSegment.ProjectID)
	require.Equal(t, "test_stream", lastSegment.Stream)
	require.Positive(t, lastSegment.SizeBytes)
	require.FileExists(t, lastSegment.Path)
	require.Equal(t, int64(3), rowCountIn(t, lastSegment.Path))
}

func Test_Append_RawSizeBytes_SumsRowBytesAcrossAppends(t *testing.T) {
	writer, err := segments.NewWriter(t.TempDir(), testSchema(), segments.WithMaxRows(3))
	require.NoError(t, err)
	defer writer.Close()

	projectID := uuid.New()
	now := time.Now()

	var lastSegment *segments.Segment
	for i := range 3 {
		segment, err := writer.Append(
			projectID,
			[]any{uuid.NewString(), now.Add(time.Duration(i) * time.Second), int64(i)},
			100,
			now,
		)
		require.NoError(t, err)
		lastSegment = segment
	}

	require.NotNil(t, lastSegment)
	require.Equal(t, int64(300), lastSegment.RawSizeBytes, "should sum rowBytes across all 3 appends")
	// RawSizeBytes (uncompressed accounting) and SizeBytes (compressed
	// Parquet file size) measure different things and must not be conflated.
	require.NotEqual(t, lastSegment.RawSizeBytes, lastSegment.SizeBytes)
}

func Test_Append_CrossingByteThreshold_AutoFlushes(t *testing.T) {
	writer, err := segments.NewWriter(
		t.TempDir(),
		testSchema(),
		segments.WithMaxRows(1_000_000),
		segments.WithMaxBytes(100),
	)
	require.NoError(t, err)
	defer writer.Close()

	projectID := uuid.New()
	now := time.Now()

	var lastSegment *segments.Segment
	for range 5 {
		// 30 bytes/row * 5 = 150 > 100 byte threshold, so this should flush before the row cap.
		segment, err := writer.Append(projectID, []any{uuid.NewString(), now, int64(1)}, 30, now)
		require.NoError(t, err)
		if segment != nil {
			lastSegment = segment
		}
	}

	require.NotNil(t, lastSegment, "byte threshold should trigger an auto-flush before the row cap does")
}

func Test_FlushProject_EmptyBuffer_ReturnsNil(t *testing.T) {
	writer, err := segments.NewWriter(t.TempDir(), testSchema())
	require.NoError(t, err)
	defer writer.Close()

	segment, err := writer.FlushProject(uuid.New())

	require.NoError(t, err)
	require.Nil(t, segment)
}

func Test_FlushStale_FlushesOnlyBuffersOlderThanMaxAge(t *testing.T) {
	writer, err := segments.NewWriter(t.TempDir(), testSchema(), segments.WithMaxRows(1_000_000))
	require.NoError(t, err)
	defer writer.Close()

	projectID := uuid.New()
	now := time.Now()

	_, err = writer.Append(projectID, []any{"a", now, int64(1)}, 16, now)
	require.NoError(t, err)

	// Nothing is stale yet with a generous maxAge.
	flushed, err := writer.FlushStale(time.Hour)
	require.NoError(t, err)
	require.Empty(t, flushed)

	// Once maxAge has passed, it's stale (maxAge <= 0 means the configured
	// default, not "everything").
	time.Sleep(5 * time.Millisecond)
	flushed, err = writer.FlushStale(time.Millisecond)
	require.NoError(t, err)
	require.Len(t, flushed, 1)
	require.Equal(t, projectID, flushed[0].ProjectID)
}

func Test_TwoProjects_BufferIndependently(t *testing.T) {
	writer, err := segments.NewWriter(t.TempDir(), testSchema(), segments.WithMaxRows(1))
	require.NoError(t, err)
	defer writer.Close()

	projectA := uuid.New()
	projectB := uuid.New()
	now := time.Now()

	segmentA, err := writer.Append(projectA, []any{"a", now, int64(1)}, 16, now)
	require.NoError(t, err)
	require.NotNil(t, segmentA)
	require.Equal(t, projectA, segmentA.ProjectID)

	segmentB, err := writer.Append(projectB, []any{"b", now, int64(2)}, 16, now)
	require.NoError(t, err)
	require.NotNil(t, segmentB)
	require.Equal(t, projectB, segmentB.ProjectID)

	require.NotEqual(t, segmentA.Path, segmentB.Path)
}

func Test_Append_WrongColumnCount_ReturnsError(t *testing.T) {
	writer, err := segments.NewWriter(t.TempDir(), testSchema())
	require.NoError(t, err)
	defer writer.Close()

	_, err = writer.Append(uuid.New(), []any{"only one value"}, 16, time.Now())

	require.Error(t, err)
}

// A crash must never leave a truncated file at a segment's final path, and a
// leftover temp file is swept at the next startup.

func Test_Append_CrossingThreshold_LeavesNoTempFileBehind(t *testing.T) {
	writer, err := segments.NewWriter(t.TempDir(), testSchema(), segments.WithMaxRows(1))
	require.NoError(t, err)
	defer writer.Close()

	now := time.Now()
	segment, err := writer.Append(uuid.New(), []any{"a", now, int64(1)}, 16, now)
	require.NoError(t, err)
	require.NotNil(t, segment)

	require.FileExists(t, segment.Path, "the real segment file must exist at its final path")
	require.NoFileExists(t, segment.Path+".tmp", "no temp file should survive a successful flush")
}

func Test_CleanupStaleTempFiles_RemovesOrphanedTempFiles_LeavesRealSegmentsAlone(t *testing.T) {
	dataDir := t.TempDir()
	writer, err := segments.NewWriter(dataDir, testSchema(), segments.WithMaxRows(1))
	require.NoError(t, err)
	defer writer.Close()

	now := time.Now()
	realSegment, err := writer.Append(uuid.New(), []any{"a", now, int64(1)}, 16, now)
	require.NoError(t, err)
	require.NotNil(t, realSegment)

	// Simulate a process killed mid-COPY: only the ".tmp" file exists.
	staleTempPath := filepath.Join(filepath.Dir(realSegment.Path), "orphaned_by_a_crash.parquet.tmp")
	require.NoError(t, os.WriteFile(staleTempPath, []byte("truncated mid-write"), 0o644))

	removed, err := writer.CleanupStaleTempFiles()
	require.NoError(t, err)
	require.Equal(t, 1, removed)

	require.NoFileExists(t, staleTempPath, "the orphaned temp file must be removed")
	require.FileExists(t, realSegment.Path, "cleanup must never touch a real, already-finalized segment")
	require.Equal(t, int64(1), rowCountIn(t, realSegment.Path), "the real segment's data must be untouched")
}

func Test_CleanupStaleTempFiles_NoTempFiles_NoopWithoutError(t *testing.T) {
	writer, err := segments.NewWriter(t.TempDir(), testSchema())
	require.NoError(t, err)
	defer writer.Close()

	// The stream's data directory doesn't exist yet; cleanup must not error.
	removed, err := writer.CleanupStaleTempFiles()
	require.NoError(t, err)
	require.Equal(t, 0, removed)
}

func Test_FlushProject_WithRequeueWhenWriteFails_KeepsRowsForNextFlush(t *testing.T) {
	dataDir := t.TempDir()
	writer, err := segments.NewWriter(dataDir, testSchema(), segments.WithRequeueOnFlushFailure())
	require.NoError(t, err)
	defer writer.Close()

	projectID := uuid.New()
	base := time.Now().UTC().Truncate(time.Second)
	for i := range 3 {
		ts := base.Add(time.Duration(i) * time.Minute)
		_, err := writer.Append(projectID, []any{uuid.NewString(), ts, int64(i)}, 16, ts)
		require.NoError(t, err)
	}
	unblock := blockSegmentDir(t, dataDir, projectID)

	_, err = writer.FlushProject(projectID)
	require.Error(t, err)

	unblock()
	segment, err := writer.FlushProject(projectID)
	require.NoError(t, err)
	require.NotNil(t, segment, "the failed flush's rows must still be buffered")
	require.Equal(t, 3, segment.RowCount)
	require.Equal(t, int64(48), segment.RawSizeBytes)
	require.True(t, segment.MinTime.Equal(base))
	require.True(t, segment.MaxTime.Equal(base.Add(2*time.Minute)))
	require.Equal(t, int64(3), rowCountIn(t, segment.Path))
}

func Test_FlushProject_WithoutRequeueWhenWriteFails_DropsRows(t *testing.T) {
	dataDir := t.TempDir()
	// The JetStream path: an unacked batch is redelivered, so keeping its
	// rows would write them twice.
	writer, err := segments.NewWriter(dataDir, testSchema())
	require.NoError(t, err)
	defer writer.Close()

	projectID := uuid.New()
	now := time.Now()
	_, err = writer.Append(projectID, []any{"a", now, int64(1)}, 16, now)
	require.NoError(t, err)
	unblock := blockSegmentDir(t, dataDir, projectID)

	_, err = writer.FlushProject(projectID)
	require.Error(t, err)

	unblock()
	segment, err := writer.FlushProject(projectID)
	require.NoError(t, err)
	require.Nil(t, segment, "nothing may be requeued without the option")
}

func Test_Append_WithRequeueWhenFlushesKeepFailing_KeepsNewestRowsUpToTheLimit(t *testing.T) {
	dataDir := t.TempDir()
	writer, err := segments.NewWriter(dataDir, testSchema(),
		segments.WithMaxRows(2), segments.WithRequeueOnFlushFailure())
	require.NoError(t, err)
	defer writer.Close()

	projectID := uuid.New()
	unblock := blockSegmentDir(t, dataDir, projectID)
	now := time.Now()
	for i := range 10 {
		segment, err := writer.Append(projectID, []any{strconv.Itoa(i), now, int64(i)}, 16, now)
		require.NoError(t, err, "a row kept for the next flush is accepted")
		require.Nil(t, segment)
	}

	_, err = writer.FlushProject(projectID)
	require.Error(t, err)

	unblock()
	segment, err := writer.FlushProject(projectID)
	require.NoError(t, err)
	require.NotNil(t, segment)
	require.Equal(t, 8, segment.RowCount, "requeued rows are capped at 4x maxRows")
	require.Equal(t, int64(8*16), segment.RawSizeBytes)
	require.Equal(t, []string{"2", "3", "4", "5", "6", "7", "8", "9"}, idsIn(t, segment.Path),
		"the oldest rows are the ones dropped")
}

// blockSegmentDir puts a file where the project's segment directory goes,
// so writeSegment fails (even as root, unlike a read-only directory).
func blockSegmentDir(t *testing.T, dataDir string, projectID uuid.UUID) (unblock func()) {
	t.Helper()

	streamDir := filepath.Join(dataDir, testSchema().Stream)
	require.NoError(t, os.MkdirAll(streamDir, 0o755))
	blocker := filepath.Join(streamDir, projectID.String())
	require.NoError(t, os.WriteFile(blocker, nil, 0o644))
	return func() { require.NoError(t, os.Remove(blocker)) }
}

func idsIn(t *testing.T, path string) []string {
	t.Helper()

	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer db.Close()

	rows, err := db.Query("SELECT id FROM read_parquet(?) ORDER BY n", path)
	require.NoError(t, err)
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	return ids
}
