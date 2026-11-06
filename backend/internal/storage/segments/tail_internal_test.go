package segments

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_AppendTail_SegmentNotUploadedOnAnotherReplica_DoesNotHideTheTail(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "local.parquet")
	require.NoError(t, os.WriteFile(local, []byte("x"), 0o600))
	base := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	uploadedAt := base.Add(time.Minute)
	records := []*SegmentRecord{
		{Path: filepath.Join(dir, "uploaded.parquet"), CreatedAt: base, UploadedAt: &uploadedAt},
		{Path: local, CreatedAt: base.Add(2 * time.Minute)},
		// Another replica's newest segment, only on its own disk.
		{Path: filepath.Join(dir, "elsewhere.parquet"), CreatedAt: base.Add(5 * time.Minute)},
	}
	tail := &recordingTail{}

	files := AppendTail(context.Background(), tail, uuid.New(), "spans", records, base, base.Add(time.Hour), nil)

	assert.Equal(t, base.Add(2*time.Minute), tail.newestReadable,
		"only segments readable here move the tail's cutoff")
	require.Len(t, files, 1, "the tail still serves the pending rows")
}

func Test_ResolveFiles_SegmentNotUploadedYet_IsSkippedAsPending(t *testing.T) {
	dir := t.TempDir()
	record := &SegmentRecord{ID: uuid.New(), Stream: "spans", Path: filepath.Join(dir, "elsewhere.parquet")}

	_, source, err := resolveFile(context.Background(), &ObjectStore{}, dir, record)

	assert.True(t, errors.Is(err, ErrNotUploadedYet))
	assert.Equal(t, "pending", source)
	assert.NotContains(t, err.Error(), "no object storage is configured")
	assert.Empty(t, ResolveFiles(context.Background(), &ObjectStore{}, dir, []*SegmentRecord{record}))
}

type recordingTail struct {
	newestReadable time.Time
}

func (r *recordingTail) TailFiles(_ context.Context, _ uuid.UUID, _ string, newestReadable time.Time) []TailFile {
	r.newestReadable = newestReadable
	return []TailFile{{Path: "tail.parquet", MinTime: newestReadable, MaxTime: newestReadable.Add(time.Minute)}}
}
