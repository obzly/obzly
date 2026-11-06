package segments_test

import (
	"testing"
	"time"

	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// A 5-minute rollup keeps, per series and bucket, the first, last, min and
// max samples with their times, the count and the sum - and fills
// series_id for a file written before it.
func Test_BuildRollup_KeepsEachBucketsExtremes(t *testing.T) {
	schema := segments.Schema{Stream: "metrics", Columns: []segments.ColumnDef{
		{Name: "id", Type: "VARCHAR"},
		{Name: "project_id", Type: "VARCHAR"},
		{Name: "metric_name", Type: "VARCHAR"},
		{Name: "labels", Type: "VARCHAR"},
		{Name: "value", Type: "DOUBLE"},
		{Name: "timestamp", Type: "TIMESTAMP_NS"},
		{Name: "type", Type: "VARCHAR"},
	}}
	base := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	project := uuid.NewString()
	row := func(offset time.Duration, value float64) []any {
		return []any{uuid.NewString(), project, "up", `{"job":"a"}`, value, base.Add(offset), "gauge"}
	}
	records := flushSegments(t, schema, [][]any{
		row(10*time.Second, 5), row(70*time.Second, 9), row(130*time.Second, 1), row(250*time.Second, 4),
		row(310*time.Second, 7), // next bucket
	})
	compactor, err := segments.NewCompactor()
	require.NoError(t, err)
	t.Cleanup(func() { _ = compactor.Close() })

	rollup, err := compactor.BuildRollup(records[0], "metrics_5m", t.TempDir())
	require.NoError(t, err)

	require.Equal(t, 2, rollup.RowCount)
	require.Equal(t, records[0].ID.String(), rollup.Stats.Source)
	require.Equal(t, segments.LevelCold, rollup.Level)
	db := openDuckDB(t)
	var seriesID uint64
	var count int64
	var sum, first, last, minValue, maxValue float64
	var firstTS, lastTS, minTS, maxTS time.Time
	require.NoError(t, db.QueryRow(`SELECT series_id, count, sum, first_ts, first_value, last_ts, last_value,
		min_ts, min_value, max_ts, max_value FROM read_parquet(?) WHERE bucket = ?`, rollup.Path, base).
		Scan(&seriesID, &count, &sum, &firstTS, &first, &lastTS, &last, &minTS, &minValue, &maxTS, &maxValue))
	require.Equal(t, segments.SeriesID("up", `{"job":"a"}`), seriesID)
	require.EqualValues(t, 4, count)
	require.InDelta(t, 19, sum, 1e-9)
	require.Equal(t, []float64{5, 4, 1, 9}, []float64{first, last, minValue, maxValue})
	require.Equal(t, base.Add(10*time.Second), firstTS.UTC())
	require.Equal(t, base.Add(250*time.Second), lastTS.UTC())
	require.Equal(t, base.Add(130*time.Second), minTS.UTC())
	require.Equal(t, base.Add(70*time.Second), maxTS.UTC())
	require.Equal(t, base.Add(10*time.Second), rollup.MinTime)
	require.Equal(t, base.Add(310*time.Second), rollup.MaxTime)
}
