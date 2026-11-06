package segments_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	_ "github.com/marcboeker/go-duckdb/v2"
	"github.com/stretchr/testify/require"
)

// writeTwoSegments flushes two small segments for projectID through a real
// Writer.
func writeTwoSegments(t *testing.T, dataDir string, projectID uuid.UUID) []segments.Segment {
	t.Helper()

	writer, err := segments.NewWriter(dataDir, testSchema(), segments.WithMaxRows(1))
	require.NoError(t, err)
	defer writer.Close()

	base := time.Now().UTC().Truncate(time.Second)

	first, err := writer.Append(projectID, []any{"a", base, int64(1)}, 16, base)
	require.NoError(t, err)
	require.NotNil(t, first, "maxRows=1 should flush immediately")

	second, err := writer.Append(projectID, []any{"b", base.Add(time.Minute), int64(2)}, 16, base.Add(time.Minute))
	require.NoError(t, err)
	require.NotNil(t, second)

	return []segments.Segment{*first, *second}
}

func toRecords(segs []segments.Segment) []*segments.SegmentRecord {
	records := make([]*segments.SegmentRecord, len(segs))
	for i, s := range segs {
		records[i] = segments.FromSegment(s)
	}
	return records
}

func Test_MergeSegments_CombinesRowsAndSpansFullTimeRange(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()
	segs := writeTwoSegments(t, dataDir, projectID)

	compactor, err := segments.NewCompactor()
	require.NoError(t, err)
	defer compactor.Close()

	merged, err := compactor.MergeSegments(toRecords(segs), dataDir)

	require.NoError(t, err)
	require.Equal(t, 2, merged.RowCount)
	require.Equal(t, int64(32), merged.RawSizeBytes, "should sum RawSizeBytes (16+16) across merged segments")
	require.Equal(t, projectID, merged.ProjectID)
	require.Equal(t, "test_stream", merged.Stream)
	require.True(t, merged.MinTime.Equal(segs[0].MinTime))
	require.True(t, merged.MaxTime.Equal(segs[1].MaxTime))
	require.FileExists(t, merged.Path)

	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer db.Close()

	var count int64
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM read_parquet(?)`, merged.Path).Scan(&count))
	require.Equal(t, int64(2), count)
}

func Test_MergeSegments_EmptySlice_ReturnsError(t *testing.T) {
	compactor, err := segments.NewCompactor()
	require.NoError(t, err)
	defer compactor.Close()

	_, err = compactor.MergeSegments(nil, t.TempDir())

	require.Error(t, err)
}

func Test_MergeSegments_MismatchedProject_ReturnsError(t *testing.T) {
	dataDir := t.TempDir()
	segs := writeTwoSegments(t, dataDir, uuid.New())
	records := toRecords(segs)
	records[1].ProjectID = uuid.New() // force a mismatch

	compactor, err := segments.NewCompactor()
	require.NoError(t, err)
	defer compactor.Close()

	_, err = compactor.MergeSegments(records, dataDir)

	require.Error(t, err)
}

func Test_MergeSegmentsWhere_WhenCopyFailsMidMerge_LeavesNoFileBehind(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()
	segs := writeTwoSegments(t, dataDir, projectID)

	compactor, err := segments.NewCompactor()
	require.NoError(t, err)
	defer compactor.Close()

	_, err = compactor.MergeSegmentsWhere(toRecords(segs), dataDir, 0,
		"CASE WHEN n > 1 THEN error('disk went away') ELSE true END", nil)

	require.Error(t, err)
	entries, err := os.ReadDir(filepath.Dir(segs[0].Path))
	require.NoError(t, err)
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	require.ElementsMatch(t, []string{filepath.Base(segs[0].Path), filepath.Base(segs[1].Path)}, names,
		"a failed merge leaves neither a segment-named file nor a temp file")
}

func Test_DeleteSegmentFiles_RemovesFilesFromDisk(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()
	segs := writeTwoSegments(t, dataDir, projectID)
	records := toRecords(segs)

	compactor, err := segments.NewCompactor()
	require.NoError(t, err)
	defer compactor.Close()

	require.NoError(t, compactor.DeleteSegmentFiles(records))

	require.NoFileExists(t, segs[0].Path)
	require.NoFileExists(t, segs[1].Path)
}

func Test_DeleteSegmentFiles_ToleratesAlreadyMissingFile(t *testing.T) {
	compactor, err := segments.NewCompactor()
	require.NoError(t, err)
	defer compactor.Close()

	missing := &segments.SegmentRecord{Path: "/nonexistent/path/does-not-exist.parquet"}

	require.NoError(t, compactor.DeleteSegmentFiles([]*segments.SegmentRecord{missing}))
}
