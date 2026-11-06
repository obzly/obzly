package segments_test

import (
	"database/sql"
	"testing"
	"time"

	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func Test_WriterFlush_WritesZSTDCompressedParquet(t *testing.T) {
	writer, err := segments.NewWriter(t.TempDir(), testSchema())
	require.NoError(t, err)
	defer writer.Close()

	projectID := uuid.New()
	_, err = writer.Append(projectID, []any{"a", time.Now(), int64(1)}, 32, time.Now())
	require.NoError(t, err)
	segment, err := writer.FlushProject(projectID)
	require.NoError(t, err)

	require.Equal(t, []string{"ZSTD"}, codecsOf(t, segment.Path))
}

func Test_MergeSegments_WritesRowsInStreamSortOrder(t *testing.T) {
	schema := segments.Schema{
		Stream: "logs",
		Columns: []segments.ColumnDef{
			{Name: "id", Type: "VARCHAR"},
			{Name: "service_name", Type: "VARCHAR"},
			{Name: "timestamp", Type: "TIMESTAMP_NS"},
		},
	}
	base := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	records := flushSegments(t, schema, [][]any{
		{"1", "svc-b", base.Add(3 * time.Second)},
		{"2", "svc-a", base.Add(2 * time.Second)},
	}, [][]any{
		{"3", "svc-b", base.Add(1 * time.Second)},
		{"4", "svc-a", base.Add(4 * time.Second)},
	})

	merged := mergeSegments(t, records)

	require.Equal(t, []string{"2", "4", "3", "1"}, idsInFileOrder(t, merged.Path),
		"service_name, then timestamp")
	require.Equal(t, []string{"ZSTD"}, codecsOf(t, merged.Path))
	require.Equal(t, 4, merged.RowCount)
}

// Hour first: a row group of a merged file then spans about an hour, so
// time filters skip the rest even in a multi-day file.
func Test_MergeSegments_SortsByHourBeforeService(t *testing.T) {
	schema := segments.Schema{
		Stream: "logs",
		Columns: []segments.ColumnDef{
			{Name: "id", Type: "VARCHAR"},
			{Name: "service_name", Type: "VARCHAR"},
			{Name: "timestamp", Type: "TIMESTAMP_NS"},
		},
	}
	base := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	records := flushSegments(t, schema, [][]any{
		{"a-11h", "svc-a", base.Add(time.Hour + time.Minute)},
		{"b-10h", "svc-b", base.Add(time.Minute)},
	}, [][]any{
		{"a-10h", "svc-a", base.Add(2 * time.Minute)},
		{"b-11h", "svc-b", base.Add(time.Hour)},
	})

	merged := mergeSegments(t, records)

	require.Equal(t, []string{"a-10h", "b-10h", "a-11h", "b-11h"}, idsInFileOrder(t, merged.Path))
}

func Test_MergeSegments_WhenInputsLackASortColumn_SortsByTheRest(t *testing.T) {
	// Early logs segments predate the service_name column.
	schema := segments.Schema{
		Stream: "logs",
		Columns: []segments.ColumnDef{
			{Name: "id", Type: "VARCHAR"},
			{Name: "timestamp", Type: "TIMESTAMP_NS"},
		},
	}
	base := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	records := flushSegments(t, schema,
		[][]any{{"late", base.Add(time.Minute)}},
		[][]any{{"early", base}},
	)

	merged := mergeSegments(t, records)

	require.Equal(t, []string{"early", "late"}, idsInFileOrder(t, merged.Path))
}

func Test_MergeSegments_StreamWithoutSortColumns_MergesUnsorted(t *testing.T) {
	records := flushSegments(t, testSchema(),
		[][]any{{"a", time.Now(), int64(1)}},
		[][]any{{"b", time.Now(), int64(2)}},
	)

	merged := mergeSegments(t, records)

	require.ElementsMatch(t, []string{"a", "b"}, idsInFileOrder(t, merged.Path))
}

// flushSegments writes each batch as its own segment and returns catalog
// records for them (not persisted - MergeSegments needs none).
func flushSegments(t *testing.T, schema segments.Schema, batches ...[][]any) []*segments.SegmentRecord {
	t.Helper()
	writer, err := segments.NewWriter(t.TempDir(), schema)
	require.NoError(t, err)
	t.Cleanup(func() { _ = writer.Close() })

	projectID := uuid.New()
	var records []*segments.SegmentRecord
	for _, batch := range batches {
		for _, row := range batch {
			_, err := writer.Append(projectID, row, 32, time.Now())
			require.NoError(t, err)
		}
		segment, err := writer.FlushProject(projectID)
		require.NoError(t, err)
		records = append(records, segments.FromSegment(*segment))
	}
	return records
}

func mergeSegments(t *testing.T, records []*segments.SegmentRecord) *segments.Segment {
	t.Helper()
	compactor, err := segments.NewCompactor()
	require.NoError(t, err)
	t.Cleanup(func() { _ = compactor.Close() })

	merged, err := compactor.MergeSegments(records, t.TempDir())
	require.NoError(t, err)
	return merged
}

func idsInFileOrder(t *testing.T, path string) []string {
	t.Helper()
	db := openDuckDB(t)
	rows, err := db.Query(`SELECT id FROM read_parquet(?, file_row_number = true) ORDER BY file_row_number`, path)
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

func codecsOf(t *testing.T, path string) []string {
	t.Helper()
	db := openDuckDB(t)
	rows, err := db.Query(`SELECT DISTINCT compression FROM parquet_metadata(?)`, path)
	require.NoError(t, err)
	defer rows.Close()

	var codecs []string
	for rows.Next() {
		var codec string
		require.NoError(t, rows.Scan(&codec))
		codecs = append(codecs, codec)
	}
	require.NoError(t, rows.Err())
	return codecs
}

func openDuckDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}
