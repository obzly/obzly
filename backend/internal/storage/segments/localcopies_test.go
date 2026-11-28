package segments_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// A merge deletes the row, then the local and remote files: a check that
// listed the row before the merge finds both copies gone.
func Test_VerifyUploaded_SegmentMergedAwayAfterListing_IsNoLoss(t *testing.T) {
	repo := &segments.SegmentRepository{}
	merged, kept := uploadedWithoutCopies(t), uploadedWithoutCopies(t)
	require.NoError(t, repo.DeleteByIDs([]uuid.UUID{merged.ID}))

	result, err := segments.VerifyUploaded(context.Background(), newMemObjectStore(t), repo,
		[]*segments.SegmentRecord{merged, kept}, time.Second)

	require.ErrorContains(t, err, "unrecoverable data loss")
	require.ErrorContains(t, err, kept.ID.String())
	require.NotContains(t, err.Error(), merged.ID.String())
	require.Equal(t, 1, result.Unrecoverable, "only the segment whose row remains is lost")
}

// uploadedWithoutCopies catalogs an uploaded segment whose local file and
// object don't exist, in its own stream: tests listing every uploaded logs
// segment would see it as lost.
func uploadedWithoutCopies(t *testing.T) *segments.SegmentRecord {
	t.Helper()
	repo := &segments.SegmentRepository{}
	projectID := uuid.New()
	now := time.Now().UTC()
	record := &segments.SegmentRecord{
		ProjectID: projectID, Stream: "verify_test", Path: filepath.Join(t.TempDir(), "gone.parquet"),
		MinTime: now, MaxTime: now, UploadedAt: &now, ObjectKey: "verify_test/" + uuid.NewString() + ".parquet",
		SchemaVersion: segments.CurrentSchemaVersion,
	}
	require.NoError(t, repo.Create(record))
	t.Cleanup(func() { _, _ = repo.DeleteByProject(projectID, "verify_test") })
	return record
}
