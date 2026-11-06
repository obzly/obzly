package segments_test

import (
	"testing"
	"time"

	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func sampleSegment(projectID uuid.UUID, stream string, minTime, maxTime time.Time) segments.Segment {
	return segments.Segment{
		Path:      "/data/" + stream + "/" + projectID.String() + "/" + uuid.NewString() + ".parquet",
		ProjectID: projectID,
		Stream:    stream,
		RowCount:  10,
		SizeBytes: 1024,
		MinTime:   minTime,
		MaxTime:   maxTime,
		CreatedAt: time.Now().UTC(),
	}
}

func Test_Create_ThenListByTimeRange_FindsOverlappingSegment(t *testing.T) {
	repo := &segments.SegmentRepository{}
	projectID := uuid.New()
	base := time.Now().UTC().Truncate(time.Second)

	segment := sampleSegment(projectID, "logs", base, base.Add(time.Hour))
	require.NoError(t, repo.Create(segments.FromSegment(segment)))

	// Query window overlapping the segment's [minTime, maxTime].
	found, err := repo.ListByTimeRange(projectID, "logs", base.Add(30*time.Minute), base.Add(2*time.Hour))
	require.NoError(t, err)
	require.Len(t, found, 1)
	require.Equal(t, segment.Path, found[0].Path)
	require.Equal(t, segment.RowCount, int(found[0].RowCount))
	require.Nil(t, found[0].UploadedAt)
}

func Test_ListOldestCovering_ReturnsTheOldestSegmentsReachingTheAmount(t *testing.T) {
	repo := &segments.SegmentRepository{}
	projectID := uuid.New()
	base := time.Now().UTC().Truncate(time.Second)
	var paths []string
	for i := range 4 {
		segment := sampleSegment(projectID, "logs", base.Add(time.Duration(i)*time.Hour),
			base.Add(time.Duration(i)*time.Hour+time.Minute))
		require.NoError(t, repo.Create(segments.FromSegment(segment)))
		paths = append(paths, segment.Path)
	}
	other := sampleSegment(uuid.New(), "logs", base.Add(-time.Hour), base)
	require.NoError(t, repo.Create(segments.FromSegment(other)))

	pathsOf := func(records []*segments.SegmentRecord) []string {
		out := make([]string, len(records))
		for i, record := range records {
			out[i] = record.Path
		}
		return out
	}
	// 10 rows / 1024 bytes each: 15 rows take the two oldest, 1024 bytes one.
	byRows, err := repo.ListOldestCovering(projectID, "logs", segments.QuotaRows, 15)
	require.NoError(t, err)
	require.Equal(t, paths[:2], pathsOf(byRows))
	byBytes, err := repo.ListOldestCovering(projectID, "logs", segments.QuotaBytes, 1024)
	require.NoError(t, err)
	require.Equal(t, paths[:1], pathsOf(byBytes))
	_, err = repo.ListOldestCovering(projectID, "logs", "path", 1)
	require.Error(t, err)
}

func Test_ListByTimeRange_ExcludesNonOverlappingSegment(t *testing.T) {
	repo := &segments.SegmentRepository{}
	projectID := uuid.New()
	base := time.Now().UTC().Truncate(time.Second)

	segment := sampleSegment(projectID, "logs", base, base.Add(time.Hour))
	require.NoError(t, repo.Create(segments.FromSegment(segment)))

	// Query window entirely after the segment's time range.
	found, err := repo.ListByTimeRange(projectID, "logs", base.Add(2*time.Hour), base.Add(3*time.Hour))
	require.NoError(t, err)
	require.Empty(t, found)
}

func Test_ListByTimeRange_ExcludesOtherProjectsAndStreams(t *testing.T) {
	repo := &segments.SegmentRepository{}
	projectID := uuid.New()
	otherProjectID := uuid.New()
	base := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, repo.Create(segments.FromSegment(sampleSegment(projectID, "logs", base, base.Add(time.Hour)))))
	require.NoError(
		t,
		repo.Create(segments.FromSegment(sampleSegment(otherProjectID, "logs", base, base.Add(time.Hour)))),
	)
	require.NoError(t, repo.Create(segments.FromSegment(sampleSegment(projectID, "spans", base, base.Add(time.Hour)))))

	found, err := repo.ListByTimeRange(projectID, "logs", base, base.Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, found, 1)
	require.Equal(t, projectID, found[0].ProjectID)
	require.Equal(t, "logs", found[0].Stream)
}

func Test_ListPendingUpload_ThenMarkUploaded_RemovesItFromPending(t *testing.T) {
	repo := &segments.SegmentRepository{}
	projectID := uuid.New()
	base := time.Now().UTC().Truncate(time.Second)
	stream := "pending_test_" + uuid.NewString()[:8] // isolate from other tests' rows

	record := segments.FromSegment(sampleSegment(projectID, stream, base, base.Add(time.Hour)))
	require.NoError(t, repo.Create(record))

	pending, err := repo.ListPendingUpload(stream, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, record.ID, pending[0].ID)

	require.NoError(t, repo.MarkUploaded(record.ID, "s3://bucket/"+record.Path))

	pending, err = repo.ListPendingUpload(stream, 10)
	require.NoError(t, err)
	require.Empty(t, pending)
}

func Test_DeleteByIDs_RemovesRecords(t *testing.T) {
	repo := &segments.SegmentRepository{}
	projectID := uuid.New()
	base := time.Now().UTC().Truncate(time.Second)
	stream := "delete_test_" + uuid.NewString()[:8]

	record := segments.FromSegment(sampleSegment(projectID, stream, base, base.Add(time.Hour)))
	require.NoError(t, repo.Create(record))

	require.NoError(t, repo.DeleteByIDs([]uuid.UUID{record.ID}))

	remaining, err := repo.ListByProject(projectID, stream)
	require.NoError(t, err)
	require.Empty(t, remaining)
}

func Test_DeleteByIDs_EmptySlice_NoError(t *testing.T) {
	repo := &segments.SegmentRepository{}

	require.NoError(t, repo.DeleteByIDs(nil))
}

func Test_ReplaceSegments_SwapsSourcesForMerged(t *testing.T) {
	repo := &segments.SegmentRepository{}
	projectID := uuid.New()
	base := time.Now().UTC().Truncate(time.Second)
	a := segments.FromSegment(sampleSegment(projectID, "logs", base, base.Add(time.Minute)))
	b := segments.FromSegment(sampleSegment(projectID, "logs", base.Add(time.Minute), base.Add(2*time.Minute)))
	require.NoError(t, repo.Create(a))
	require.NoError(t, repo.Create(b))

	merged := segments.FromSegment(sampleSegment(projectID, "logs", base, base.Add(2*time.Minute)))
	require.NoError(t, repo.ReplaceSegments([]uuid.UUID{a.ID, b.ID}, merged))

	remaining, err := repo.ListByProject(projectID, "logs")
	require.NoError(t, err)
	require.Len(t, remaining, 1)
	require.Equal(t, merged.ID, remaining[0].ID)
}

// Regression: a compaction that overlapped a project deletion wrote the
// merged segment back, so the deleted logs reappeared.
func Test_ReplaceSegments_WhenASourceWasDeleted_ChangesNothing(t *testing.T) {
	repo := &segments.SegmentRepository{}
	projectID := uuid.New()
	base := time.Now().UTC().Truncate(time.Second)
	a := segments.FromSegment(sampleSegment(projectID, "logs", base, base.Add(time.Minute)))
	b := segments.FromSegment(sampleSegment(projectID, "logs", base.Add(time.Minute), base.Add(2*time.Minute)))
	require.NoError(t, repo.Create(a))
	require.NoError(t, repo.Create(b))
	require.NoError(t, repo.DeleteByIDs([]uuid.UUID{b.ID}))

	merged := segments.FromSegment(sampleSegment(projectID, "logs", base, base.Add(2*time.Minute)))
	err := repo.ReplaceSegments([]uuid.UUID{a.ID, b.ID}, merged)
	require.ErrorIs(t, err, segments.ErrSegmentsGone)

	remaining, err := repo.ListByProject(projectID, "logs")
	require.NoError(t, err)
	require.Len(t, remaining, 1, "the surviving source is kept and no merged row is written")
	require.Equal(t, a.ID, remaining[0].ID)
}

func Test_DeleteByProject_RemovesOnlyThatProjectAndStream(t *testing.T) {
	repo := &segments.SegmentRepository{}
	projectID, otherProjectID := uuid.New(), uuid.New()
	base := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, repo.Create(segments.FromSegment(sampleSegment(projectID, "logs", base, base.Add(time.Minute)))))
	require.NoError(t, repo.Create(segments.FromSegment(sampleSegment(projectID, "logs", base, base.Add(time.Minute)))))
	require.NoError(
		t,
		repo.Create(segments.FromSegment(sampleSegment(projectID, "spans", base, base.Add(time.Minute)))),
	)
	require.NoError(
		t,
		repo.Create(segments.FromSegment(sampleSegment(otherProjectID, "logs", base, base.Add(time.Minute)))),
	)

	deleted, err := repo.DeleteByProject(projectID, "logs")
	require.NoError(t, err)
	require.Len(t, deleted, 2)
	for _, record := range deleted {
		require.NotEmpty(t, record.Path, "returned rows carry their file paths")
	}

	left, err := repo.ListByProject(projectID, "logs")
	require.NoError(t, err)
	require.Empty(t, left)
	spans, err := repo.ListByProject(projectID, "spans")
	require.NoError(t, err)
	require.Len(t, spans, 1)
	other, err := repo.ListByProject(otherProjectID, "logs")
	require.NoError(t, err)
	require.Len(t, other, 1)
}
