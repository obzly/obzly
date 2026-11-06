package segments

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// A crash between COPY and rename is simulated by failing the rename and
// looking at the directory at that moment: that's what the process leaves.
func Test_MergeSegments_WhenCrashingBeforeRename_LeavesOnlyASweepableTempFile(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()
	writer, err := NewWriter(dataDir, Schema{
		Stream:  "test_stream",
		Columns: []ColumnDef{{Name: "id", Type: "VARCHAR"}, {Name: "n", Type: "BIGINT"}},
	}, WithMaxRows(1))
	require.NoError(t, err)
	defer writer.Close()
	var records []*SegmentRecord
	for i := range 2 {
		segment, err := writer.Append(projectID, []any{uuid.NewString(), int64(i)}, 16, time.Now().UTC())
		require.NoError(t, err)
		require.NotNil(t, segment)
		records = append(records, FromSegment(*segment))
	}
	segmentDir := filepath.Dir(records[0].Path)

	var atCrash []string
	var tempPath string
	original := placeSegmentFile
	t.Cleanup(func() { placeSegmentFile = original })
	placeSegmentFile = func(from, _ string) error {
		tempPath = from
		require.FileExists(t, from)
		atCrash = parquetFilesIn(t, segmentDir)
		return errors.New("killed")
	}

	compactor, err := NewCompactor()
	require.NoError(t, err)
	defer compactor.Close()
	_, err = compactor.MergeSegments(records, dataDir)

	require.Error(t, err)
	require.ElementsMatch(t, []string{records[0].Path, records[1].Path}, atCrash,
		"no segment-named file may exist before the merge is complete")
	require.True(t, strings.HasSuffix(tempPath, ".parquet.tmp"), "CleanupStaleTempFiles must cover %s", tempPath)
	require.NoFileExists(t, tempPath, "a failed merge removes its temp file")
	require.ElementsMatch(t, []string{records[0].Path, records[1].Path}, parquetFilesIn(t, segmentDir))
}

func parquetFilesIn(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var paths []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".parquet") {
			paths = append(paths, filepath.Join(dir, entry.Name()))
		}
	}
	return paths
}
