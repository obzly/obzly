package segments_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func Test_LoadOrCreateWriterID_SameDiskKeepsIDNewDiskGetsNewOne(t *testing.T) {
	dataDir := t.TempDir()

	first, err := segments.LoadOrCreateWriterID(dataDir)
	require.NoError(t, err)
	require.NotEmpty(t, first)

	again, err := segments.LoadOrCreateWriterID(dataDir)
	require.NoError(t, err)
	require.Equal(t, first, again, "a restart on the same volume keeps its writer id")

	other, err := segments.LoadOrCreateWriterID(t.TempDir())
	require.NoError(t, err)
	require.NotEqual(t, first, other, "a fresh disk (emptyDir) gets a new writer id")

	entries, err := os.ReadDir(dataDir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "no temp file left behind")
}

func Test_LoadOrCreateWriterID_CreatesMissingDataDir(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "not", "yet", "created")

	id, err := segments.LoadOrCreateWriterID(dataDir)
	require.NoError(t, err)
	require.NotEmpty(t, id)
}

func Test_CreateAndReplaceSegments_WhenLocalWriterSet_StampWriterID(t *testing.T) {
	setLocalWriter(t, "replica-a", false)
	repo := &segments.SegmentRepository{}
	stream := uniqueStream()
	projectID := uuid.New()
	base := time.Now().UTC()

	source := segments.FromSegment(sampleSegment(projectID, stream, base, base.Add(time.Minute)))
	require.NoError(t, repo.Create(source))
	merged := segments.FromSegment(sampleSegment(projectID, stream, base, base.Add(time.Minute)))
	require.NoError(t, repo.ReplaceSegments([]uuid.UUID{source.ID}, merged))

	records, err := repo.ListByProject(projectID, stream)
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.NotNil(t, records[0].WriterID)
	require.Equal(t, "replica-a", *records[0].WriterID)
	deleteStream(t, repo, projectID, stream)
}

func Test_Create_WhenLocalWriterUnset_LeavesWriterIDNull(t *testing.T) {
	segments.ResetLocalWriter()
	repo := &segments.SegmentRepository{}
	stream := uniqueStream()
	projectID := uuid.New()
	base := time.Now().UTC()

	require.NoError(t, repo.Create(segments.FromSegment(sampleSegment(projectID, stream, base, base))))

	records, err := repo.ListByProject(projectID, stream)
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Nil(t, records[0].WriterID)
	deleteStream(t, repo, projectID, stream)
}

// Regression for the HA split (finding 1b): an uploader must only see rows
// whose files are on its own disk.
func Test_ListPendingUploadForLocalWriter_ScopesToOwnRowsAndOptionallyUnowned(t *testing.T) {
	repo := &segments.SegmentRepository{}
	stream := uniqueStream()
	projectID := uuid.New()
	base := time.Now().UTC()

	createWithWriter := func(writerID string) *segments.SegmentRecord {
		if writerID == "" {
			segments.ResetLocalWriter()
		} else {
			segments.SetLocalWriter(writerID, false)
		}
		record := segments.FromSegment(sampleSegment(projectID, stream, base, base))
		require.NoError(t, repo.Create(record))
		return record
	}
	own := createWithWriter("replica-a")
	createWithWriter("replica-b")
	unowned := createWithWriter("")
	t.Cleanup(segments.ResetLocalWriter)

	idsOf := func(records []*segments.SegmentRecord) []uuid.UUID {
		ids := make([]uuid.UUID, len(records))
		for i, record := range records {
			ids[i] = record.ID
		}
		return ids
	}

	segments.SetLocalWriter("replica-a", false)
	pending, err := repo.ListPendingUploadForLocalWriter(stream, 100)
	require.NoError(t, err)
	require.ElementsMatch(t, []uuid.UUID{own.ID}, idsOf(pending), "api replica: own rows only")

	segments.SetLocalWriter("replica-a", true)
	pending, err = repo.ListPendingUploadForLocalWriter(stream, 100)
	require.NoError(t, err)
	require.ElementsMatch(t, []uuid.UUID{own.ID, unowned.ID}, idsOf(pending),
		"worker replica: own rows plus rows written before writer_id existed")

	segments.ResetLocalWriter()
	pending, err = repo.ListPendingUploadForLocalWriter(stream, 100)
	require.NoError(t, err)
	require.Len(t, pending, 3, "unset: every pending row, as before")

	deleteStream(t, repo, projectID, stream)
}

func setLocalWriter(t *testing.T, writerID string, uploadsUnowned bool) {
	t.Helper()
	segments.SetLocalWriter(writerID, uploadsUnowned)
	t.Cleanup(segments.ResetLocalWriter)
}

func deleteStream(t *testing.T, repo *segments.SegmentRepository, projectID uuid.UUID, stream string) {
	t.Helper()
	_, err := repo.DeleteByProject(projectID, stream)
	require.NoError(t, err)
}
