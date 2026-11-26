package segments_test

import (
	"database/sql"
	"testing"
	"time"

	"obzly/internal/storage/segments"
	"obzly/internal/telemetry"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func Test_MergeSegments_BatchRedeliveredAfterItWasPersisted_KeepsOneRowPerID(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()
	base := time.Now().UTC().Truncate(time.Second)
	batch := [][]any{{"a", base, "first"}, {"b", base.Add(time.Second), "second"}}
	records := []*segments.SegmentRecord{
		writeLogsSegment(t, dataDir, projectID, batch),
		// The broker redelivered the batch: same ids, a second file.
		writeLogsSegment(t, dataDir, projectID, batch),
		// Rows without an id are never collapsed.
		writeLogsSegment(t, dataDir, projectID, [][]any{
			{"c", base.Add(2 * time.Second), "third"}, {nil, base, "no id"}, {nil, base, "no id"},
		}),
	}
	before := testutil.ToFloat64(telemetry.CompactionRepeatedRows.WithLabelValues("logs"))
	compactor, err := segments.NewCompactor()
	require.NoError(t, err)
	defer compactor.Close()

	merged, err := compactor.MergeSegmentsAt(records, dataDir, segments.LevelCompacted)

	require.NoError(t, err)
	require.Equal(t, 5, merged.RowCount)
	require.Equal(t, map[string]int64{"a": 1, "b": 1, "c": 1, "": 2}, idCounts(t, merged.Path))
	require.Equal(t, 2.0, testutil.ToFloat64(telemetry.CompactionRepeatedRows.WithLabelValues("logs"))-before)
}

func Test_MergeSegments_NoRepeatedIDs_KeepsEveryRow(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()
	base := time.Now().UTC().Truncate(time.Second)
	records := []*segments.SegmentRecord{
		writeLogsSegment(t, dataDir, projectID, [][]any{{"a", base, "x"}, {"b", base, "x"}}),
		writeLogsSegment(t, dataDir, projectID, [][]any{{"c", base, "x"}}),
	}
	compactor, err := segments.NewCompactor()
	require.NoError(t, err)
	defer compactor.Close()

	merged, err := compactor.MergeSegmentsAt(records, dataDir, segments.LevelCompacted)

	require.NoError(t, err)
	require.Equal(t, 3, merged.RowCount)
	require.Equal(t, map[string]int64{"a": 1, "b": 1, "c": 1}, idCounts(t, merged.Path))
}

func writeLogsSegment(t *testing.T, dataDir string, projectID uuid.UUID, rows [][]any) *segments.SegmentRecord {
	t.Helper()
	schema := segments.Schema{Stream: "logs", Columns: []segments.ColumnDef{
		{Name: "id", Type: "VARCHAR"},
		{Name: "timestamp", Type: "TIMESTAMP"},
		{Name: "message", Type: "VARCHAR"},
	}}
	writer, err := segments.NewWriter(dataDir, schema, segments.WithMaxRows(len(rows)))
	require.NoError(t, err)
	defer writer.Close()
	var flushed *segments.Segment
	for _, row := range rows {
		flushed, err = writer.Append(projectID, row, 16, row[1].(time.Time))
		require.NoError(t, err)
	}
	require.NotNil(t, flushed)
	return segments.FromSegment(*flushed)
}

func idCounts(t *testing.T, path string) map[string]int64 {
	t.Helper()
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer db.Close()
	rows, err := db.Query(`SELECT coalesce(id, ''), count(*) FROM read_parquet(?) GROUP BY 1`, path)
	require.NoError(t, err)
	defer rows.Close()
	counts := map[string]int64{}
	for rows.Next() {
		var id string
		var n int64
		require.NoError(t, rows.Scan(&id, &n))
		counts[id] = n
	}
	require.NoError(t, rows.Err())
	return counts
}
