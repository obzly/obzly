package logs_compactor_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	logs_compactor "obzly/internal/features/logs/compactor"
	logs_core "obzly/internal/features/logs/core"
	logs_ingester "obzly/internal/features/logs/ingester"
	logs_uploader "obzly/internal/features/logs/uploader"
	"obzly/internal/storage"
	"obzly/internal/storage/segments"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	_ "gocloud.dev/blob/memblob"
)

// seedTinySegments ingests count items with maxRows(1), one segment each,
// timestamps one minute apart from startTime.
func seedTinySegments(t *testing.T, dataDir string, projectID uuid.UUID, count int, startTime time.Time) {
	t.Helper()

	ingester, err := logs_ingester.NewLogIngester(dataDir, segments.WithMaxRows(1))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ingester.Close() })

	for i := range count {
		item := &logs_core.LogItem{
			ID:        uuid.New(),
			ProjectID: projectID,
			Timestamp: startTime.Add(time.Duration(i) * time.Minute),
			Level:     logs_core.LogLevelInfo,
			Message:   "msg",
		}
		require.NoError(t, ingester.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{projectID: {item}}))
	}
}

// seedTinySegmentsWithMessage is seedTinySegments with a given message, so a
// test can control on-disk size.
func seedTinySegmentsWithMessage(
	t *testing.T,
	dataDir string,
	projectID uuid.UUID,
	count int,
	startTime time.Time,
	message string,
) {
	t.Helper()

	ingester, err := logs_ingester.NewLogIngester(dataDir, segments.WithMaxRows(1))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ingester.Close() })

	for i := range count {
		item := &logs_core.LogItem{
			ID:        uuid.New(),
			ProjectID: projectID,
			Timestamp: startTime.Add(time.Duration(i) * time.Minute),
			Level:     logs_core.LogLevelInfo,
			Message:   message,
		}
		require.NoError(t, ingester.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{projectID: {item}}))
	}
}

func randomHex(t *testing.T, byteLen int) string {
	t.Helper()
	buf := make([]byte, byteLen)
	_, err := rand.Read(buf)
	require.NoError(t, err)
	return hex.EncodeToString(buf)
}

func totalRows(t *testing.T, repo *segments.SegmentRepository, projectID uuid.UUID) int64 {
	t.Helper()
	records, err := repo.ListByProject(projectID, "logs")
	require.NoError(t, err)

	var total int64
	for _, r := range records {
		total += r.RowCount
	}
	return total
}

func Test_CompactSmallSegments_MergesConsecutiveSmallSegments(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()
	seedTinySegments(t, dataDir, projectID, 5, time.Now().UTC())

	repo := &segments.SegmentRepository{}
	before, err := repo.ListByProject(projectID, "logs")
	require.NoError(t, err)
	require.Len(t, before, 5, "maxRows=1 should have produced 5 separate segments")
	require.Equal(t, segments.LevelFlush, before[0].Level)

	compactor, err := logs_compactor.NewLogCompactor(dataDir)
	require.NoError(t, err)
	defer compactor.Close()

	test_utils.AgeSegments(t, projectID, "logs", 48*time.Hour)
	result, err := compactor.CompactSmallSegments(projectID)

	require.NoError(t, err)
	require.Equal(t, 1, result.MergedGroups)
	require.Equal(t, 5, result.SegmentsRemoved)
	require.Equal(t, 1, result.SegmentsCreated)

	after, err := repo.ListByProject(projectID, "logs")
	require.NoError(t, err)
	require.Len(t, after, 1, "the 5 small segments should have collapsed into 1")
	require.Equal(t, segments.LevelCompacted, after[0].Level, "the level is saved in the catalog")
	require.Equal(t, int64(5), after[0].RowCount, "no rows should be lost during compaction")
	require.Equal(t, int64(5), totalRows(t, repo, projectID))
}

func Test_CompactSmallSegments_NoSegments_ReturnsEmptyResult(t *testing.T) {
	compactor, err := logs_compactor.NewLogCompactor(t.TempDir())
	require.NoError(t, err)
	defer compactor.Close()

	result, err := compactor.CompactSmallSegments(uuid.New())

	require.NoError(t, err)
	require.Equal(t, 0, result.MergedGroups)
}

func Test_EnforceRetention_DeletesSegmentsOlderThanCutoff(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()
	// One segment 10 days old, one recent — 5-day retention should drop only the old one.
	seedTinySegments(t, dataDir, projectID, 1, time.Now().UTC().AddDate(0, 0, -10))
	seedTinySegments(t, dataDir, projectID, 1, time.Now().UTC())

	compactor, err := logs_compactor.NewLogCompactor(dataDir)
	require.NoError(t, err)
	defer compactor.Close()

	deleted, err := compactor.EnforceRetention(projectID, 5)

	require.NoError(t, err)
	require.Equal(t, 1, deleted)

	repo := &segments.SegmentRepository{}
	remaining, err := repo.ListByProject(projectID, "logs")
	require.NoError(t, err)
	require.Len(t, remaining, 1)
}

func Test_EnforceRetention_KeepsAllSegmentsWithinWindow(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()
	seedTinySegments(t, dataDir, projectID, 3, time.Now().UTC())

	compactor, err := logs_compactor.NewLogCompactor(dataDir)
	require.NoError(t, err)
	defer compactor.Close()

	deleted, err := compactor.EnforceRetention(projectID, 30)

	require.NoError(t, err)
	require.Equal(t, 0, deleted)
}

func Test_EnforceRetention_NoOpWhenMaxLifeDaysNotPositive(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()
	seedTinySegments(t, dataDir, projectID, 1, time.Now().UTC().AddDate(-1, 0, 0))

	compactor, err := logs_compactor.NewLogCompactor(dataDir)
	require.NoError(t, err)
	defer compactor.Close()

	deleted, err := compactor.EnforceRetention(projectID, 0)

	require.NoError(t, err)
	require.Equal(t, 0, deleted, "maxLifeDays<=0 means unlimited retention")
}

func Test_EnforceCountQuota_DeletesOldestSegmentsUntilUnderLimit(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()
	seedTinySegments(t, dataDir, projectID, 5, time.Now().UTC().Add(-time.Hour))

	compactor, err := logs_compactor.NewLogCompactor(dataDir)
	require.NoError(t, err)
	defer compactor.Close()

	deleted, err := compactor.EnforceCountQuota(projectID, 2)

	require.NoError(t, err)
	require.Equal(t, 3, deleted, "5 rows over a 2-row quota should evict the 3 oldest segments")
	require.Equal(t, int64(2), totalRows(t, &segments.SegmentRepository{}, projectID))
}

func Test_EnforceCountQuota_NoOpWhenUnderLimit(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()
	seedTinySegments(t, dataDir, projectID, 2, time.Now().UTC())

	compactor, err := logs_compactor.NewLogCompactor(dataDir)
	require.NoError(t, err)
	defer compactor.Close()

	deleted, err := compactor.EnforceCountQuota(projectID, 10)

	require.NoError(t, err)
	require.Equal(t, 0, deleted)
}

func Test_EnforceCountQuota_NoOpWhenMaxAmountNotPositive(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()
	seedTinySegments(t, dataDir, projectID, 2, time.Now().UTC())

	compactor, err := logs_compactor.NewLogCompactor(dataDir)
	require.NoError(t, err)
	defer compactor.Close()

	deleted, err := compactor.EnforceCountQuota(projectID, 0)

	require.NoError(t, err)
	require.Equal(t, 0, deleted)
}

func Test_EnforceSizeQuota_DeletesOldestSegmentsUntilUnderTarget(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()
	// High-entropy content, so 5 segments exceed 1MB even after Parquet
	// compression (repeated text compresses to almost nothing).
	seedTinySegmentsWithMessage(t, dataDir, projectID, 5, time.Now().UTC().Add(-time.Hour), randomHex(t, 300_000))

	repo := &segments.SegmentRepository{}
	before, err := repo.ListByProject(projectID, "logs")
	require.NoError(t, err)
	var totalBytes int64
	for _, r := range before {
		totalBytes += r.SizeBytes
	}
	require.Greater(t, totalBytes, int64(1024*1024), "test setup should produce more than 1MB of segments")

	compactor, err := logs_compactor.NewLogCompactor(dataDir)
	require.NoError(t, err)
	defer compactor.Close()

	deleted, err := compactor.EnforceSizeQuota(projectID, 1)

	require.NoError(t, err)
	require.Positive(t, deleted)

	after, err := repo.ListByProject(projectID, "logs")
	require.NoError(t, err)
	require.Less(t, len(after), len(before))
}

func Test_EnforceSizeQuota_NoOpWhenUnderLimit(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()
	seedTinySegments(t, dataDir, projectID, 2, time.Now().UTC())

	compactor, err := logs_compactor.NewLogCompactor(dataDir)
	require.NoError(t, err)
	defer compactor.Close()

	deleted, err := compactor.EnforceSizeQuota(projectID, 10_000) // 10GB, way over tiny test data

	require.NoError(t, err)
	require.Equal(t, 0, deleted)
}

// uploadOneSegment seeds and uploads one segment, returning its object key.
// The isolated writer keeps other packages' pending rows out of the upload.
func uploadOneSegment(t *testing.T, dataDir string, projectID uuid.UUID, store *segments.ObjectStore) string {
	t.Helper()
	test_utils.UseIsolatedSegmentWriter(t)
	seedTinySegments(t, dataDir, projectID, 1, time.Now().UTC().AddDate(0, 0, -10))

	uploader := logs_uploader.NewLogUploader(store)
	uploaded, err := uploader.UploadPending(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, 1, uploaded)

	repo := &segments.SegmentRepository{}
	records, err := repo.ListByProject(projectID, "logs")
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.NotEmpty(t, records[0].ObjectKey, "upload should have recorded an object key")

	return records[0].ObjectKey
}

func Test_EnforceRetention_WithObjectStore_DeletesObjectStorageCopyToo(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()

	store, err := segments.NewObjectStore(context.Background(), "mem://")
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	objectKey := uploadOneSegment(t, dataDir, projectID, store)
	exists, err := store.Exists(context.Background(), objectKey)
	require.NoError(t, err)
	require.True(t, exists, "test setup should have actually uploaded the object")

	compactor, err := logs_compactor.NewLogCompactor(dataDir, logs_compactor.WithObjectStore(store))
	require.NoError(t, err)
	defer compactor.Close()

	deleted, err := compactor.EnforceRetention(projectID, 1)
	require.NoError(t, err)
	require.Equal(t, 1, deleted)

	exists, err = store.Exists(context.Background(), objectKey)
	require.NoError(t, err)
	require.False(t, exists, "retention cleanup should also remove the object storage copy")
}

func Test_DeleteAllForProject_WithObjectStore_DeletesObjectStorageCopyToo(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()

	store, err := segments.NewObjectStore(context.Background(), "mem://")
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	objectKey := uploadOneSegment(t, dataDir, projectID, store)

	compactor, err := logs_compactor.NewLogCompactor(dataDir, logs_compactor.WithObjectStore(store))
	require.NoError(t, err)
	defer compactor.Close()

	deleted, err := compactor.DeleteAllForProject(projectID)
	require.NoError(t, err)
	require.Equal(t, 1, deleted)

	exists, err := store.Exists(context.Background(), objectKey)
	require.NoError(t, err)
	require.False(t, exists, "project deletion should also remove the object storage copy")
}

func Test_DeleteAllForProject_WithObjectStore_SegmentNeverUploaded_IsUnaffected(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()
	seedTinySegments(t, dataDir, projectID, 1, time.Now().UTC())

	store, err := segments.NewObjectStore(context.Background(), "mem://")
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	compactor, err := logs_compactor.NewLogCompactor(dataDir, logs_compactor.WithObjectStore(store))
	require.NoError(t, err)
	defer compactor.Close()

	// The segment's ObjectKey is empty (never uploaded) - deletion must
	// not attempt (and fail on) an object storage call for it.
	deleted, err := compactor.DeleteAllForProject(projectID)
	require.NoError(t, err)
	require.Equal(t, 1, deleted)
}

func Test_EnforceRetention_WithObjectStore_AlreadyMissingObject_DoesNotError(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()

	store, err := segments.NewObjectStore(context.Background(), "mem://")
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	objectKey := uploadOneSegment(t, dataDir, projectID, store)
	// Simulate a retry after a partially completed prior delete: the
	// object is already gone, but the catalog row still has the key.
	require.NoError(t, store.Delete(context.Background(), objectKey))

	compactor, err := logs_compactor.NewLogCompactor(dataDir, logs_compactor.WithObjectStore(store))
	require.NoError(t, err)
	defer compactor.Close()

	deleted, err := compactor.EnforceRetention(projectID, 1)
	require.NoError(t, err)
	require.Equal(t, 1, deleted, "an already-missing object storage copy should not block segment deletion")
}

// Another replica wrote and uploaded one of the segments, so its file is not
// on this disk: the merge must download it.
func Test_CompactSmallSegments_InputOnlyInObjectStorage_DownloadsItAndMerges(t *testing.T) {
	test_utils.UseIsolatedSegmentWriter(t)
	dataDir := t.TempDir()
	projectID := uuid.New()
	seedTinySegments(t, dataDir, projectID, 3, time.Now().UTC())
	store, err := segments.NewObjectStore(context.Background(), "mem://")
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	uploaded, err := logs_uploader.NewLogUploader(store).UploadPending(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, 3, uploaded)
	repo := &segments.SegmentRepository{}
	records, err := repo.ListByProject(projectID, "logs")
	require.NoError(t, err)
	require.NoError(t, os.Remove(records[0].Path))

	compactor, err := logs_compactor.NewLogCompactor(dataDir, logs_compactor.WithObjectStore(store))
	require.NoError(t, err)
	defer compactor.Close()
	test_utils.AgeSegments(t, projectID, "logs", 48*time.Hour)
	result, err := compactor.CompactSmallSegments(projectID)

	require.NoError(t, err)
	require.Equal(t, 1, result.MergedGroups)
	require.Equal(t, int64(3), totalRows(t, repo, projectID), "the downloaded segment's row is kept")
}

func Test_CompactSmallSegments_InputNeitherLocalNorUploaded_SkipsTheGroup(t *testing.T) {
	test_utils.UseIsolatedSegmentWriter(t)
	dataDir := t.TempDir()
	projectID := uuid.New()
	seedTinySegments(t, dataDir, projectID, 3, time.Now().UTC())
	store, err := segments.NewObjectStore(context.Background(), "mem://")
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	repo := &segments.SegmentRepository{}
	records, err := repo.ListByProject(projectID, "logs")
	require.NoError(t, err)
	require.NoError(t, os.Remove(records[0].Path))

	compactor, err := logs_compactor.NewLogCompactor(dataDir, logs_compactor.WithObjectStore(store))
	require.NoError(t, err)
	defer compactor.Close()
	test_utils.AgeSegments(t, projectID, "logs", 48*time.Hour)
	result, err := compactor.CompactSmallSegments(projectID)

	require.NoError(t, err, "a segment still pending on another replica is retried next pass")
	require.Zero(t, result.MergedGroups)
	after, err := repo.ListByProject(projectID, "logs")
	require.NoError(t, err)
	require.Len(t, after, 3)
}

func Test_CompactSmallSegments_MergeFails_CountsTheAttemptAndKeepsTheSegments(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()
	seedTinySegments(t, dataDir, projectID, 3, time.Now().UTC())
	repo := &segments.SegmentRepository{}
	records, err := repo.ListByProject(projectID, "logs")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(records[0].Path, []byte("not parquet"), 0o644))

	compactor, err := logs_compactor.NewLogCompactor(dataDir)
	require.NoError(t, err)
	defer compactor.Close()
	test_utils.AgeSegments(t, projectID, "logs", 48*time.Hour)
	result, err := compactor.CompactSmallSegments(projectID)

	require.Error(t, err)
	require.Zero(t, result.MergedGroups)
	after, err := repo.ListByProject(projectID, "logs")
	require.NoError(t, err)
	require.Len(t, after, 3)
	for _, record := range after {
		require.Equal(t, 1, record.CompactionAttempts)
	}
	files, err := os.ReadDir(filepath.Dir(records[0].Path))
	require.NoError(t, err)
	require.Len(t, files, 3, "the failed merge's partial output is removed")
}

// A merge that crashes the process is counted before it runs, so after
// MaxCompactionAttempts passes its segments are left out instead of taking
// the worker down every pass.
func Test_CompactSmallSegments_SegmentsAtMaxAttempts_AreLeftOutAndTheRestMerge(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()
	seedTinySegments(t, dataDir, projectID, 5, time.Now().UTC())
	repo := &segments.SegmentRepository{}
	records, err := repo.ListByProject(projectID, "logs")
	require.NoError(t, err)
	quarantined := []uuid.UUID{records[0].ID, records[1].ID}
	require.NoError(t, storage.GetDb().Exec(
		"UPDATE stream_segments SET compaction_attempts = ? WHERE id IN ?",
		segments.MaxCompactionAttempts, quarantined).Error)

	compactor, err := logs_compactor.NewLogCompactor(dataDir)
	require.NoError(t, err)
	defer compactor.Close()
	test_utils.AgeSegments(t, projectID, "logs", 48*time.Hour)
	result, err := compactor.CompactSmallSegments(projectID)

	require.NoError(t, err)
	require.Equal(t, 1, result.MergedGroups)
	require.Equal(t, 3, result.SegmentsRemoved)
	after, err := repo.ListByProject(projectID, "logs")
	require.NoError(t, err)
	require.Len(t, after, 3)
	var kept []uuid.UUID
	for _, record := range after {
		if record.CompactionAttempts == segments.MaxCompactionAttempts {
			kept = append(kept, record.ID)
		}
	}
	require.ElementsMatch(t, quarantined, kept)
	require.Equal(t, int64(5), totalRows(t, repo, projectID), "left-out segments stay queryable")
}

func Test_CompactSmallSegments_OneGroupFails_OtherGroupsStillMerge(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()
	dayOne := time.Now().UTC().AddDate(0, 0, -3).Truncate(24 * time.Hour).Add(time.Hour)
	seedTinySegments(t, dataDir, projectID, 2, dayOne)
	seedTinySegments(t, dataDir, projectID, 2, dayOne.AddDate(0, 0, 1))
	repo := &segments.SegmentRepository{}
	records, err := repo.ListByProject(projectID, "logs")
	require.NoError(t, err)
	require.Len(t, records, 4)
	ids := make([]uuid.UUID, len(records))
	for i, record := range records {
		ids[i] = record.ID
	}
	require.NoError(t, repo.SetLevel(ids, segments.LevelCompacted))
	require.NoError(t, os.WriteFile(records[0].Path, []byte("not parquet"), 0o644))

	compactor, err := logs_compactor.NewLogCompactor(dataDir)
	require.NoError(t, err)
	defer compactor.Close()
	result, err := compactor.CompactSmallSegments(projectID)

	require.Error(t, err, "the failed group is still reported")
	require.Equal(t, 1, result.MergedGroups, "the second day merges although the first failed")
	after, err := repo.ListByProject(projectID, "logs")
	require.NoError(t, err)
	require.Len(t, after, 3)
}
