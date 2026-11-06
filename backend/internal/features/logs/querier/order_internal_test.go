package logs_querier

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type fixedTail struct{ path string }

// A newest-first search reads the tail, then segments by newest row: a long
// old segment read first made DuckDB's top-N scan all of it.
func Test_SearchInputs_NewestFirst_ListsTailThenSegmentsByNewestRow(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	segment := func(name string, minAgo, maxAgo time.Duration) *segments.SegmentRecord {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
		return &segments.SegmentRecord{ID: uuid.New(), Path: path, Stream: "logs",
			MinTime: now.Add(-minAgo), MaxTime: now.Add(-maxAgo), CreatedAt: now.Add(-48 * time.Hour)}
	}
	oldWide := segment("old-wide.parquet", 72*time.Hour, 20*time.Hour)
	newest := segment("newest.parquet", 10*time.Minute, time.Minute)
	middle := segment("middle.parquet", 3*time.Hour, 2*time.Hour)
	tailPath := filepath.Join(dir, "tail.parquet")
	q := &LogQuerier{stream: "logs", tail: fixedTail{path: tailPath}}

	tail, ordered := q.searchInputs(context.Background(), uuid.New(),
		[]*segments.SegmentRecord{oldWide, newest, middle}, now.Add(-24*time.Hour), now, true)

	require.Len(t, tail, 1)
	require.Equal(t, tailPath, tail[0].Path)
	var names []string
	for _, record := range ordered {
		names = append(names, filepath.Base(record.Path))
	}
	require.Equal(t, []string{"newest.parquet", "middle.parquet", "old-wide.parquet"}, names)
}

func (f fixedTail) TailFiles(context.Context, uuid.UUID, string, time.Time) []segments.TailFile {
	now := time.Now().UTC()
	return []segments.TailFile{{Path: f.path, MinTime: now.Add(-time.Minute), MaxTime: now}}
}
