package logs_querier

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Without a search filter, a segment wholly inside the window counts from
// the catalog (its file is never opened - it isn't even Parquet); a
// boundary segment is read.
func Test_CountMatching_Unfiltered_CountsInsideSegmentsFromTheCatalog(t *testing.T) {
	dir := t.TempDir()
	projectID := uuid.New()
	from := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	boundaryPath := filepath.Join(dir, "boundary.parquet")
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	// 10 rows, 10 minutes apart from 09:30: 10:00 through 11:00 are inside.
	_, err = db.Exec(fmt.Sprintf(`COPY (
		SELECT '%s' AS project_id, 'r' || r AS id,
		       TIMESTAMP '2026-10-01 09:30:00' + to_minutes((r * 10)::BIGINT) AS timestamp
		FROM range(10) t(r)) TO '%s' (FORMAT PARQUET)`, projectID, boundaryPath))
	require.NoError(t, err)
	neverOpened := filepath.Join(dir, "never-opened.parquet")
	require.NoError(t, os.WriteFile(neverOpened, []byte("not parquet"), 0o644))
	records := []*segments.SegmentRecord{
		{
			ID:       uuid.New(),
			Stream:   "logs",
			Path:     neverOpened,
			RowCount: 1000,
			MinTime: from.Add(
				time.Minute,
			),
			MaxTime:       to.Add(-time.Minute),
			SchemaVersion: segments.CurrentSchemaVersion,
		},
		{
			ID:       uuid.New(),
			Stream:   "logs",
			Path:     boundaryPath,
			RowCount: 10,
			MinTime: from.Add(
				-30 * time.Minute,
			),
			MaxTime:       from.Add(time.Hour),
			SchemaVersion: segments.CurrentSchemaVersion,
		},
	}
	q, err := NewLogQuerier(WithQueryDir(filepath.Join(dir, "query")))
	require.NoError(t, err)
	defer func() { _ = q.Close() }()
	where := "project_id = ? AND timestamp >= ? AND timestamp <= ?"
	args := []any{projectID.String(), from, to}

	total, err := q.countMatching(nil, records, from, to, true, where, args, 0, nil)

	require.NoError(t, err)
	require.EqualValues(t, 1000+7, total, "catalog rows plus the boundary file's 7 rows in the window")
}

// DuckDB's error for a file deleted after planning is what triggers the
// re-plan.
func Test_SegmentVanished_MatchesDuckDBsMissingFileError(t *testing.T) {
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	_, err = db.Exec("SELECT * FROM read_parquet('" + filepath.Join(t.TempDir(), "gone.parquet") + "')")

	require.True(t, segmentVanished(fmt.Errorf("query fragment failed: %w", err)), "gone before the query bound")
	require.True(t, segmentVanished(errors.New(`fragment f: IO Error: Cannot open file "/data/logs/p/1.parquet": `+
		`No such file or directory`)), "gone while it ran (seen on the test Site)")
	require.False(t, segmentVanished(errors.New("syntax error")))
}
