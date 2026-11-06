package segments

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"obzly/internal/telemetry"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "gocloud.dev/blob/memblob"
)

// A cold segment read for a few columns is fetched column by column: the
// sparse file answers those columns' queries like the whole file, takes a
// fraction of its disk, and gains columns as later queries need them; a
// query needing most of the file downloads it whole instead.
func Test_ResolvePartial_FetchesOnlyTheColumnsRead(t *testing.T) {
	partialMinSize = 0
	t.Cleanup(func() { partialMinSize = defaultPartialMinSize })
	ctx := context.Background()
	store, err := NewObjectStore(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	source := filepath.Join(t.TempDir(), "segment.parquet")
	_, err = db.Exec(`COPY (SELECT i AS id, TIMESTAMP '2026-10-13 00:00:00' + to_seconds(i) AS timestamp,
		['info', 'warn', 'error'][1 + i % 3] AS level, i % 7 AS service,
		(SELECT string_agg(md5((i * 31 + x)::VARCHAR), '') FROM range(30) r(x)) AS message
		FROM range(20000) t(i)) TO '` + source + `' (FORMAT PARQUET, ROW_GROUP_SIZE 4096)`)
	require.NoError(t, err)
	info, err := os.Stat(source)
	require.NoError(t, err)
	record := &SegmentRecord{ID: uuid.New(), ProjectID: uuid.New(), Stream: "logs", Path: "/nowhere/x.parquet",
		ObjectKey: "logs/x.parquet", SizeBytes: info.Size(), SchemaVersion: CurrentSchemaVersion}
	require.NoError(t, store.Upload(ctx, record.ObjectKey, source))
	cacheDir := t.TempDir()

	query := func(path, sqlText string) string {
		var result string
		require.NoError(t, db.QueryRow(sqlText, path).Scan(&result))
		return result
	}
	levels := "SELECT string_agg(level || '=' || n, ',' ORDER BY level) FROM " +
		"(SELECT level, count(*) n FROM read_parquet(?) GROUP BY level)"

	downloaded := func() float64 {
		return testutil.ToFloat64(telemetry.ObjectStoreBytes.WithLabelValues(opDownload))
	}
	before := downloaded()
	path, err := resolvePartial(ctx, store, cacheDir, record, []string{"timestamp", "level"})
	require.NoError(t, err)
	assert.Less(t, downloaded()-before, float64(info.Size())/20, "only the read columns are downloaded")
	assert.Equal(t, query(source, levels), query(path, levels))
	partial, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, info.Size(), partial.Size(), "the file keeps its layout")
	// Linux file systems keep the holes; APFS (macOS) may fill them on close.
	if runtime.GOOS == "linux" {
		assert.Less(t, allocatedSize(partial), info.Size()/4, "and only the read columns take disk")
	}

	// A later query adds a column.
	services := "SELECT sum(service)::VARCHAR FROM read_parquet(?) WHERE level = 'warn'"
	path, err = resolvePartial(ctx, store, cacheDir, record, []string{"level", "service"})
	require.NoError(t, err)
	assert.Equal(t, query(source, services), query(path, services))
	assert.Equal(t, query(source, levels), query(path, levels), "earlier columns still there")

	// Most of the bytes: whole download, and the partial copy goes.
	_, err = resolvePartial(ctx, store, cacheDir, record, []string{"message"})
	require.ErrorIs(t, err, errWholeFile)
	assert.NoFileExists(t, path)
}

func Test_ReferencedColumns_ColumnsTheSQLNames(t *testing.T) {
	RegisterSchemaAnchor(Schema{Stream: "partial_test", Columns: []ColumnDef{
		{Name: "timestamp", Type: "TIMESTAMP"}, {Name: "level", Type: "VARCHAR"},
		{Name: "message", Type: "VARCHAR"}, {Name: "fields", Type: "VARCHAR"},
		{Name: "promoted_s1", Type: "VARCHAR"},
	}})
	assert.Equal(t, []string{"timestamp", "level"},
		ReferencedColumns("partial_test", "SELECT level, count(*) FROM {{source}} WHERE timestamp >= ? GROUP BY level"))
	assert.Nil(t, ReferencedColumns("partial_test", "SELECT * FROM {{source}}"))
	assert.Nil(t, ReferencedColumns("unknown_stream", "SELECT level FROM {{source}}"))
	assert.Equal(t, []string{"level", "fields", "promoted_s1"},
		ReferencedColumns("partial_test", "SELECT level FROM {{source}} WHERE {{promoted:s1:1:$.x}} = ?"))
}
