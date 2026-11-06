package segments_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"obzly/internal/storage"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func Test_Create_RowLandsInItsStreamMonthPartition(t *testing.T) {
	repo := &segments.SegmentRepository{}
	now := time.Now().UTC()
	require.NoError(t, repo.EnsurePartitions(now))
	projectID := uuid.New()
	t.Cleanup(func() {
		_, _ = repo.DeleteByProject(projectID, "logs")
		_, _ = repo.DeleteByProject(projectID, "adhoc_stream")
	})

	logs := segments.FromSegment(sampleSegment(projectID, "logs", now, now))
	require.NoError(t, repo.Create(logs))
	other := segments.FromSegment(sampleSegment(projectID, "adhoc_stream", now, now))
	require.NoError(t, repo.Create(other))

	require.Equal(t, monthPartition("logs", now), partitionOf(t, logs.ID))
	require.Equal(t, "stream_segments_other", partitionOf(t, other.ID))
}

func Test_EnsurePartition_MovesRowsOutOfTheDefaultPartition(t *testing.T) {
	repo := &segments.SegmentRepository{}
	// Far enough ahead that nothing else creates this month.
	future := time.Now().UTC().AddDate(0, 30, 0)
	dropPartition(t, monthPartition("logs", future))
	t.Cleanup(func() { dropPartition(t, monthPartition("logs", future)) })
	projectID := uuid.New()
	t.Cleanup(func() { _, _ = repo.DeleteByProject(projectID, "logs") })

	record := segments.FromSegment(sampleSegment(projectID, "logs", future, future))
	require.NoError(t, repo.Create(record))
	require.Equal(t, "stream_segments_logs_default", partitionOf(t, record.ID))

	require.NoError(t, repo.EnsurePartition("logs", future))

	require.Equal(t, monthPartition("logs", future), partitionOf(t, record.ID))
	found, err := repo.ListByTimeRange(projectID, "logs", future.Add(-time.Minute), future.Add(time.Minute))
	require.NoError(t, err)
	require.Len(t, found, 1)
}

func Test_EnsurePartition_ConcurrentCallsForTheSameMonth_AllSucceed(t *testing.T) {
	repo := &segments.SegmentRepository{}
	future := time.Now().UTC().AddDate(0, 31, 0)
	dropPartition(t, monthPartition("spans", future))
	t.Cleanup(func() { dropPartition(t, monthPartition("spans", future)) })

	errs := make([]error, 6)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = repo.EnsurePartition("spans", future)
		}(i)
	}
	wg.Wait()

	for _, err := range errs {
		require.NoError(t, err)
	}
	require.True(t, tableExists(t, monthPartition("spans", future)))
}

// The reason for partitioning by max_time: a recent-window query never
// touches older months.
func Test_ListByTimeRangeQuery_RecentWindow_SkipsOlderMonthPartitions(t *testing.T) {
	repo := &segments.SegmentRepository{}
	now := time.Now().UTC()
	older := now.AddDate(0, -3, 0)
	require.NoError(t, repo.EnsurePartition("metrics", older))
	require.NoError(t, repo.EnsurePartitions(now))

	var plan []string
	err := storage.GetDb().Raw(
		`EXPLAIN SELECT * FROM stream_segments
		 WHERE project_id = ? AND stream = ? AND min_time <= ? AND max_time >= ?`,
		uuid.New(), "metrics", now, now.Add(-time.Hour),
	).Scan(&plan).Error
	require.NoError(t, err)

	text := fmt.Sprint(plan)
	require.Contains(t, text, monthPartition("metrics", now))
	require.NotContains(t, text, monthPartition("metrics", older))
	require.NotContains(t, text, "stream_segments_logs", "other streams are pruned too")
}

func monthPartition(stream string, at time.Time) string {
	return "stream_segments_" + stream + "_" + at.UTC().Format("200601")
}

func partitionOf(t *testing.T, id uuid.UUID) string {
	t.Helper()
	var partition string
	require.NoError(t, storage.GetDb().
		Raw(`SELECT tableoid::regclass::text FROM stream_segments WHERE id = ?`, id).
		Scan(&partition).Error)
	return partition
}

func tableExists(t *testing.T, name string) bool {
	t.Helper()
	var exists bool
	require.NoError(t, storage.GetDb().Raw(`SELECT to_regclass(?) IS NOT NULL`, name).Scan(&exists).Error)
	return exists
}

func dropPartition(t *testing.T, name string) {
	t.Helper()
	require.NoError(t, storage.GetDb().Exec(`DROP TABLE IF EXISTS `+name).Error)
}

func Test_DropEmptyPastPartitions_DropsOnlyEmptyEndedMonths(t *testing.T) {
	repo := &segments.SegmentRepository{}
	now := time.Now().UTC()
	emptyMonth := time.Date(2001, 1, 15, 0, 0, 0, 0, time.UTC)
	usedMonth := time.Date(2001, 2, 15, 0, 0, 0, 0, time.UTC)
	future := now.AddDate(0, 32, 0)
	for _, month := range []time.Time{emptyMonth, usedMonth, future} {
		dropPartition(t, monthPartition("logs", month))
		t.Cleanup(func() { dropPartition(t, monthPartition("logs", month)) })
		require.NoError(t, repo.EnsurePartition("logs", month))
	}
	require.NoError(t, repo.EnsurePartitions(now))
	projectID := uuid.New()
	t.Cleanup(func() { _, _ = repo.DeleteByProject(projectID, "logs") })
	require.NoError(t, repo.Create(segments.FromSegment(sampleSegment(projectID, "logs", usedMonth, usedMonth))))

	dropped, err := repo.DropEmptyPastPartitions(now)

	require.NoError(t, err)
	require.Contains(t, dropped, monthPartition("logs", emptyMonth))
	require.False(t, tableExists(t, monthPartition("logs", emptyMonth)))
	require.True(t, tableExists(t, monthPartition("logs", usedMonth)), "a month still holding a row is kept")
	require.True(t, tableExists(t, monthPartition("logs", now)), "the current month is never dropped")
	require.True(t, tableExists(t, monthPartition("logs", future)), "future months are never dropped")

	// Retention deletes the last row; the next pass drops the month.
	_, err = repo.DeleteByProject(projectID, "logs")
	require.NoError(t, err)
	dropped, err = repo.DropEmptyPastPartitions(now)
	require.NoError(t, err)
	require.Contains(t, dropped, monthPartition("logs", usedMonth))

	backfilled := segments.FromSegment(sampleSegment(projectID, "logs", usedMonth, usedMonth))
	require.NoError(t, repo.Create(backfilled))
	require.Equal(t, "stream_segments_logs_default", partitionOf(t, backfilled.ID),
		"a later backfill for a dropped month lands in the default partition")
}

// Queries on a stream queue behind a waiting lock, so the drop gives up
// quickly instead of stalling them behind a long-running reader.
func Test_DropEmptyPastPartitions_WhenStreamIsBusy_TimesOutInsteadOfWaiting(t *testing.T) {
	repo := &segments.SegmentRepository{}
	month := time.Date(2001, 3, 15, 0, 0, 0, 0, time.UTC)
	dropPartition(t, monthPartition("logs", month))
	t.Cleanup(func() { dropPartition(t, monthPartition("logs", month)) })
	require.NoError(t, repo.EnsurePartition("logs", month))

	reader := storage.GetDb().Begin()
	require.NoError(t, reader.Error)
	t.Cleanup(func() { reader.Rollback() })
	var ignored []uuid.UUID
	require.NoError(t, reader.Raw(`SELECT id FROM stream_segments WHERE stream = 'logs' LIMIT 1`).Scan(&ignored).Error)

	started := time.Now()
	_, err := repo.DropEmptyPastPartitions(time.Now().UTC())

	require.Error(t, err)
	require.Contains(t, err.Error(), "lock timeout")
	require.Less(t, time.Since(started), 10*time.Second)
	require.True(t, tableExists(t, monthPartition("logs", month)))
}
