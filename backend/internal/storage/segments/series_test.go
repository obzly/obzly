package segments_test

import (
	"testing"
	"time"

	"obzly/internal/storage/segments"

	"github.com/stretchr/testify/require"
)

// The SQL function computes exactly the id ingest stores.
func Test_ObzlySeriesID_MatchesSeriesID(t *testing.T) {
	db := openDuckDB(t)
	require.NoError(t, segments.RegisterNormalizationFunctions(db))

	var got uint64
	require.NoError(
		t,
		db.QueryRow(`SELECT obzly_series_id('http_requests_total', '{"code":"200","job":"api"}')`).Scan(&got),
	)

	require.Equal(t, segments.SeriesID("http_requests_total", `{"code":"200","job":"api"}`), got)
	require.NotEqual(t, segments.SeriesID("a", "{}"), segments.SeriesID("b", "{}"))
}

// Compaction fills series_id for metrics rows written before the column
// existed, so older segments gain ids as they are merged.
func Test_MergeSegments_Metrics_FillsMissingSeriesIDs(t *testing.T) {
	withoutID := segments.Schema{Stream: "metrics", Columns: []segments.ColumnDef{
		{Name: "id", Type: "VARCHAR"},
		{Name: "metric_name", Type: "VARCHAR"},
		{Name: "labels", Type: "VARCHAR"},
		{Name: "timestamp", Type: "TIMESTAMP_NS"},
	}}
	base := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	records := flushSegments(t, withoutID, [][]any{
		{"1", "up", `{"job":"api"}`, base},
		{"2", "up", `{"job":"web"}`, base.Add(time.Second)},
	}, [][]any{
		{"3", "up", `{"job":"api"}`, base.Add(2 * time.Second)},
	})

	merged := mergeSegments(t, records)

	db := openDuckDB(t)
	rows, err := db.Query(`SELECT metric_name, labels, series_id FROM read_parquet(?)`, merged.Path)
	require.NoError(t, err)
	defer rows.Close()
	n := 0
	for rows.Next() {
		var name, labels string
		var seriesID uint64
		require.NoError(t, rows.Scan(&name, &labels, &seriesID))
		require.Equal(t, segments.SeriesID(name, labels), seriesID)
		n++
	}
	require.Equal(t, 3, n)
}

// metrics_series merges keep one row per series - the earliest first_seen
// and latest last_seen - and record the real row count.
func Test_MergeSegments_MetricsSeries_DeduplicatesSeries(t *testing.T) {
	schema := segments.Schema{Stream: "metrics_series", Columns: []segments.ColumnDef{
		{Name: "project_id", Type: "VARCHAR"},
		{Name: "series_id", Type: "UBIGINT"},
		{Name: "metric_name", Type: "VARCHAR"},
		{Name: "labels", Type: "VARCHAR"},
		{Name: "type", Type: "VARCHAR"},
		{Name: "first_seen", Type: "TIMESTAMP_NS"},
		{Name: "last_seen", Type: "TIMESTAMP_NS"},
	}}
	base := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	records := flushSegments(t, schema, [][]any{
		{"p", uint64(1), "up", "{}", "gauge", base, base.Add(time.Minute)},
		{"p", uint64(2), "down", "{}", "gauge", base, base},
	}, [][]any{
		{"p", uint64(1), "up", "{}", "counter", base.Add(2 * time.Minute), base.Add(3 * time.Minute)},
	})

	merged := mergeSegments(t, records)

	require.Equal(t, 2, merged.RowCount)
	db := openDuckDB(t)
	var first, last time.Time
	var metricType string
	require.NoError(t, db.QueryRow(`SELECT first_seen, last_seen, type FROM read_parquet(?) WHERE series_id = 1`,
		merged.Path).Scan(&first, &last, &metricType))
	require.Equal(t, base, first.UTC())
	require.Equal(t, base.Add(3*time.Minute), last.UTC())
	require.Equal(t, "counter", metricType, "the latest type")
}
