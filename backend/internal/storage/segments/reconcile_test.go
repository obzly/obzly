package segments_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"obzly/internal/storage"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// One project's catalog and bucket after a bad day: an intact segment, one
// whose object and disk copy are gone, one whose object is gone but the
// disk copy isn't, one with the wrong size, an orphan from after a
// database restore, an orphan overlapping cataloged data, and junk.
func Test_Reconcile_FindsAndRepairsEachKindOnce(t *testing.T) {
	ctx := context.Background()
	store := newMemObjectStore(t)
	dir := t.TempDir()
	project := uuid.New()
	t.Cleanup(func() {
		storage.GetDb().Exec("DELETE FROM stream_segments WHERE project_id = ?", project)
		storage.GetDb().Exec("DELETE FROM lost_segments WHERE project_id = ?", project)
	})
	day := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
	cataloged := func(offset time.Duration, upload bool) *segments.SegmentRecord {
		t.Helper()
		record := writeSegment(t, dir, project, day.Add(offset))
		if upload {
			require.NoError(t, store.Upload(ctx, record.ObjectKey, record.Path))
		}
		require.NoError(t, storage.GetDb().Create(record).Error)
		return record
	}
	intact := cataloged(0, true)
	lost := cataloged(2*time.Hour, false)
	require.NoError(t, os.Remove(lost.Path))
	reuploadable := cataloged(4*time.Hour, false)
	resized := cataloged(6*time.Hour, true)
	require.NoError(t, storage.GetDb().Model(resized).Update("size_bytes", resized.SizeBytes+1).Error)

	// Retired long ago but never deleted: before the restore point.
	old := writeSegment(t, dir, project, day.Add(-24*time.Hour))
	require.NoError(t, store.Upload(ctx, old.ObjectKey, old.Path))
	time.Sleep(20 * time.Millisecond)
	restorePoint := time.Now()
	time.Sleep(20 * time.Millisecond)
	orphan := writeSegment(t, dir, project, day.Add(10*time.Hour))
	orphan.Level = 1
	orphan.ObjectKey = segments.ObjectKeyFor(orphan)
	require.NoError(t, store.Upload(ctx, orphan.ObjectKey, orphan.Path))
	overlapping := writeSegment(t, dir, project, day) // intact's hour
	require.NoError(t, store.Upload(ctx, overlapping.ObjectKey, overlapping.Path))
	junk := "logs/" + project.String() + "/notes.txt"
	require.NoError(t, store.Upload(ctx, junk, orphan.Path))

	options := segments.ReconcileOptions{
		Streams: []string{"logs"}, Projects: []uuid.UUID{project}, DataDir: t.TempDir(), AdoptSince: restorePoint,
	}
	dry, err := segments.Reconcile(ctx, store, options)
	require.NoError(t, err)
	assert.Equal(t, 4, dry.RowsChecked)
	kinds := findingsByID(dry)
	assert.Equal(t, segments.FindingLost, kinds[lost.ID.String()].Kind)
	assert.Equal(t, segments.FindingReuploadable, kinds[reuploadable.ID.String()].Kind)
	assert.Equal(t, segments.FindingSizeMismatch, kinds[resized.ID.String()].Kind)
	assert.Equal(t, segments.FindingOrphan, kinds[orphan.ID.String()].Kind)
	assert.Contains(t, kinds[orphan.ID.String()].Detail, "5 rows")
	assert.Equal(t, segments.FindingOrphanOverlapping, kinds[overlapping.ID.String()].Kind)
	assert.Equal(t, segments.FindingOrphanOld, kinds[old.ID.String()].Kind)
	assert.NotContains(t, kinds, intact.ID.String())
	assert.Len(t, dry.Findings, 7, "the six above plus the junk key")
	for _, finding := range dry.Findings {
		assert.False(t, finding.Fixed, "a dry run changes nothing")
	}
	assert.True(t, rowExists(t, lost.ID))

	// Without a restore point, --apply adopts nothing.
	unanchored := options
	unanchored.Apply, unanchored.AdoptSince = true, time.Time{}
	first, err := segments.Reconcile(ctx, store, unanchored)
	require.NoError(t, err)
	kinds = findingsByID(first)
	assert.Equal(t, segments.FindingOrphan, kinds[orphan.ID.String()].Kind)
	assert.False(t, kinds[orphan.ID.String()].Fixed)
	assert.False(t, rowExists(t, orphan.ID))
	for _, id := range []uuid.UUID{lost.ID, reuploadable.ID} {
		assert.True(t, kinds[id.String()].Fixed, id)
	}
	assert.False(t, rowExists(t, lost.ID), "a lost segment leaves the catalog")
	var lostRows int64
	require.NoError(t, storage.GetDb().Raw("SELECT count(*) FROM lost_segments WHERE id = ?", lost.ID).
		Scan(&lostRows).Error)
	assert.Equal(t, int64(1), lostRows)
	exists, err := store.Exists(ctx, reuploadable.ObjectKey)
	require.NoError(t, err)
	assert.True(t, exists)

	options.Apply = true
	applied, err := segments.Reconcile(ctx, store, options)
	require.NoError(t, err)
	kinds = findingsByID(applied)
	assert.True(t, kinds[orphan.ID.String()].Fixed)
	var adopted segments.SegmentRecord
	require.NoError(t, storage.GetDb().Where("id = ?", orphan.ID).First(&adopted).Error)
	assert.Equal(t, int64(5), adopted.RowCount)
	assert.Equal(t, 1, adopted.Level)
	assert.True(t, adopted.MinTime.Equal(orphan.MinTime))
	assert.True(t, adopted.MaxTime.Equal(orphan.MaxTime))
	assert.False(t, rowExists(t, overlapping.ID), "overlapping orphans are only reported")
	assert.False(t, rowExists(t, old.ID), "retired data stays retired")

	again, err := segments.Reconcile(ctx, store, options)
	require.NoError(t, err)
	kinds = findingsByID(again)
	assert.Len(t, again.Findings, 4, "only what needs a person is left: size, overlap, old, junk")
	assert.Equal(t, segments.FindingSizeMismatch, kinds[resized.ID.String()].Kind)
}

// writeSegment writes five log rows from start and returns the catalog row
// it would have once uploaded.
func writeSegment(t *testing.T, dir string, project uuid.UUID, start time.Time) *segments.SegmentRecord {
	t.Helper()
	id := uuid.New()
	path := filepath.Join(dir, id.String()+".parquet")
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	_, err = db.Exec(fmt.Sprintf(`COPY (SELECT ?::TIMESTAMPTZ + to_seconds(i) AS timestamp, 'checkout' AS service_name,
		'info' AS level, 'ok' AS message FROM range(5) t(i)) TO '%s' (FORMAT PARQUET)`, path), start)
	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	uploaded := time.Now().UTC()
	record := &segments.SegmentRecord{
		ID: id, ProjectID: project, Stream: "logs", Path: path, RowCount: 5,
		SizeBytes: info.Size(), RawSizeBytes: info.Size(), MinTime: start, MaxTime: start.Add(4 * time.Second),
		CreatedAt: uploaded, UploadedAt: &uploaded, SchemaVersion: segments.CurrentSchemaVersion,
	}
	record.ObjectKey = segments.ObjectKeyFor(record)
	return record
}

func findingsByID(report *segments.ReconcileReport) map[string]segments.ReconcileFinding {
	out := map[string]segments.ReconcileFinding{}
	for _, finding := range report.Findings {
		out[finding.SegmentID] = finding
	}
	return out
}

func rowExists(t *testing.T, id uuid.UUID) bool {
	var count int64
	require.NoError(t, storage.GetDb().Model(&segments.SegmentRecord{}).Where("id = ?", id).Count(&count).Error)
	return count > 0
}

// The audit samples uploaded segments (here all of one project's) and
// names each kind of disagreement with object storage.
func Test_Audit_FlagsMissingResizedAndMiscountedSegments(t *testing.T) {
	ctx := context.Background()
	store := newMemObjectStore(t)
	dir := t.TempDir()
	project := uuid.New()
	t.Cleanup(func() { storage.GetDb().Exec("DELETE FROM stream_segments WHERE project_id = ?", project) })
	day := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
	uploaded := func(offset time.Duration) *segments.SegmentRecord {
		record := writeSegment(t, dir, project, day.Add(offset))
		require.NoError(t, store.Upload(ctx, record.ObjectKey, record.Path))
		require.NoError(t, storage.GetDb().Create(record).Error)
		return record
	}
	intact := uploaded(0)
	missing := uploaded(time.Hour)
	require.NoError(t, store.Delete(ctx, missing.ObjectKey))
	resized := uploaded(2 * time.Hour)
	junk := filepath.Join(dir, "junk")
	require.NoError(t, os.WriteFile(junk, []byte("truncated"), 0o644))
	require.NoError(t, store.Upload(ctx, resized.ObjectKey, junk))
	miscounted := uploaded(3 * time.Hour)
	require.NoError(t, storage.GetDb().Model(miscounted).Update("row_count", 6).Error)

	result, err := segments.Audit(ctx, store, segments.AuditOptions{
		SamplePercent: 100, MaxSamples: 10, Projects: []uuid.UUID{project}, TempDir: t.TempDir(),
	})
	require.NoError(t, err)
	assert.Equal(t, 4, result.Sampled)
	problems := map[string]string{}
	for _, failure := range result.Failures {
		problems[failure.SegmentID] = failure.Problem
	}
	assert.Equal(t, map[string]string{
		missing.ID.String(): "missing", resized.ID.String(): "size", miscounted.ID.String(): "row-count",
	}, problems)
	assert.NotContains(t, problems, intact.ID.String())

	capped, err := segments.Audit(ctx, store, segments.AuditOptions{
		SamplePercent: 1, MaxSamples: 10, Projects: []uuid.UUID{project}, TempDir: t.TempDir(),
	})
	require.NoError(t, err)
	assert.Equal(t, 1, capped.Sampled, "at least one segment, however small the percentage")
}
