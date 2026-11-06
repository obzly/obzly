package segments_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	_ "gocloud.dev/blob/memblob"
)

func Test_ResolveFiles_WithManyColdSegments_KeepsTheirOrder(t *testing.T) {
	ctx := context.Background()
	store, err := segments.NewObjectStore(ctx, "mem://")
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	dir := t.TempDir()
	projectID := uuid.New()
	var records []*segments.SegmentRecord
	for i := range 20 {
		local := filepath.Join(dir, fmt.Sprintf("upload-%d.parquet", i))
		require.NoError(t, os.WriteFile(local, fmt.Appendf(nil, "segment %d", i), 0o644))
		record := &segments.SegmentRecord{
			ID: uuid.New(), ProjectID: projectID, Stream: "metrics",
			Path: filepath.Join(dir, "elsewhere", fmt.Sprintf("%d.parquet", i)),
		}
		record.ObjectKey = "resolve-test/" + record.ID.String() + ".parquet"
		require.NoError(t, store.Upload(ctx, record.ObjectKey, local))
		records = append(records, record)
	}

	files := segments.ResolveFiles(ctx, store, filepath.Join(dir, "cache"), records)

	require.Len(t, files, len(records))
	for i, file := range files {
		content, err := os.ReadFile(file.Path)
		require.NoError(t, err)
		require.Equal(t, fmt.Sprintf("segment %d", i), string(content), "file %d", i)
	}
}
