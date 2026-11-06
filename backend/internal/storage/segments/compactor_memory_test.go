package segments

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Every connection of the compactor's handle gets the memory limit (DuckDB's
// 80%-of-RAM default got a worker OOM-killed).
func Test_NewCompactor_BoundsDuckDBMemoryOnEveryConnection(t *testing.T) {
	compactor, err := NewCompactor(WithSpillDir(t.TempDir()))
	require.NoError(t, err)
	defer func() { _ = compactor.Close() }()
	compactor.db.SetMaxIdleConns(0)

	for range 3 {
		var limit string
		require.NoError(t, compactor.db.QueryRow("SELECT current_setting('memory_limit')").Scan(&limit))
		require.Contains(t, []string{"1000.0 MiB", "953.6 MiB", "1.0 GiB", "1.0 GB", "1000.0 MB"}, limit)
	}
}

// An L1 merge at the planner's raw cap sorts in memory within the
// compactor's limit (this sort doesn't spill).
func Test_MergeSegmentsAt_L1MergeAtTheRawCap_FitsTheMemoryLimit(t *testing.T) {
	compactor, err := NewCompactor(WithSpillDir(t.TempDir()))
	require.NoError(t, err)
	defer func() { _ = compactor.Close() }()
	dir := t.TempDir()
	records := writeMergeInputs(t, compactor, dir, 3, 570_000)

	merged, err := compactor.MergeSegmentsAt(records, dir, LevelCompacted)

	require.NoError(t, err)
	require.Equal(t, 1_710_000, merged.RowCount)
}

// A cold merge concatenates instead of sorting, so it streams in little
// memory however large the inputs.
func Test_MergeSegmentsAt_ColdMerge_StreamsUnderASmallMemoryLimit(t *testing.T) {
	compactor, err := NewCompactor(WithSpillDir(t.TempDir()))
	require.NoError(t, err)
	defer func() { _ = compactor.Close() }()
	_, err = compactor.db.Exec("SET memory_limit = '192MB'")
	require.NoError(t, err)
	dir := t.TempDir()
	records := writeMergeInputs(t, compactor, dir, 3, 600_000)

	merged, err := compactor.MergeSegmentsAt(records, dir, LevelCold)

	require.NoError(t, err, "a sorted merge of this size fails at 192 MB")
	require.Equal(t, 1_800_000, merged.RowCount)
	require.Equal(t, LevelCold, merged.Level)
}

// writeMergeInputs writes files of rows ~150 B each (incompressible text).
func writeMergeInputs(t *testing.T, compactor *Compactor, dir string, files, rows int) []*SegmentRecord {
	t.Helper()
	projectID := uuid.New()
	var records []*SegmentRecord
	for i := range files {
		path := filepath.Join(dir, fmt.Sprintf("in_%d.parquet", i))
		_, err := compactor.db.Exec(fmt.Sprintf(`COPY (
			SELECT uuid()::VARCHAR AS id, '%s' AS project_id,
			       'svc' || (random() * 50)::INT AS service_name,
			       (TIMESTAMP '2026-10-01' + to_seconds((random() * 3600)::BIGINT))::TIMESTAMP_NS AS timestamp,
			       md5(random()::VARCHAR) || md5(random()::VARCHAR) || md5(random()::VARCHAR) AS message
			FROM range(%d)) TO '%s' (FORMAT PARQUET)`, projectID, rows, path))
		require.NoError(t, err)
		records = append(records, &SegmentRecord{ID: uuid.New(), ProjectID: projectID, Stream: "logs",
			Path: path, RowCount: int64(rows), SchemaVersion: CurrentSchemaVersion, Level: LevelCompacted})
	}
	return records
}
