package segments

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeCachedFile creates a size-byte file at path with its mtime set to age
// ago, so tests control eviction order.
func writeCachedFile(t *testing.T, path string, size int, age time.Duration) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, make([]byte, size), 0o644))
	modTime := time.Now().Add(-age)
	require.NoError(t, os.Chtimes(path, modTime, modTime))
}

func Test_EvictColdCache_UnderLimit_DeletesNothing(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "logs", "proj1", "a.parquet")
	writeCachedFile(t, a, 100, time.Hour)

	require.NoError(t, EvictColdCache(dir, 1000))

	_, err := os.Stat(a)
	assert.NoError(t, err, "a file under the size limit must survive")
}

// Before EvictColdCache existed nothing deleted a downloaded segment, so the
// cache grew without bound.
func Test_EvictColdCache_OverLimit_DeletesOldestFirst(t *testing.T) {
	dir := t.TempDir()
	oldest := filepath.Join(dir, "logs", "proj1", "oldest.parquet")
	middle := filepath.Join(dir, "spans", "proj1", "middle.parquet")
	newest := filepath.Join(dir, "metrics", "proj1", "newest.parquet")

	writeCachedFile(t, oldest, 400, 3*time.Hour)
	writeCachedFile(t, middle, 400, 2*time.Hour)
	writeCachedFile(t, newest, 400, 1*time.Hour)
	// total 1200, maxBytes 1000, low water mark 900: evicting the oldest file
	// (400) is enough.

	require.NoError(t, EvictColdCache(dir, 1000))

	_, err := os.Stat(oldest)
	assert.True(t, os.IsNotExist(err), "the oldest file must be evicted first")

	_, err = os.Stat(middle)
	assert.NoError(t, err, "the middle-aged file must survive")

	_, err = os.Stat(newest)
	assert.NoError(t, err, "the newest file must survive")
}

func Test_EvictColdCache_EvictsDownToLowWaterMark_NotJustUnderLimit(t *testing.T) {
	dir := t.TempDir()
	// Four 300-byte files: evicting one reaches the low water mark (900) exactly,
	// which the next assertion pins down.
	paths := make([]string, 4)
	for i := range paths {
		paths[i] = filepath.Join(dir, "logs", "proj1", string(rune('a'+i))+".parquet")
		writeCachedFile(t, paths[i], 300, time.Duration(4-i)*time.Hour)
	}

	require.NoError(t, EvictColdCache(dir, 1000))

	survivors := 0
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			survivors++
		}
	}
	// 900 / 300 = 3 survivors: the target is the low water mark, not maxBytes.
	assert.Equal(t, 3, survivors)
}

func Test_EvictColdCache_EmptyOrMissingDirectory_IsANoOp(t *testing.T) {
	dir := t.TempDir()
	assert.NoError(t, EvictColdCache(dir, 1000), "an empty cache directory")
	assert.NoError(
		t,
		EvictColdCache(filepath.Join(dir, "does-not-exist"), 1000),
		"a cache directory that doesn't exist yet (cold tier configured but nothing downloaded)",
	)
}

func Test_EvictColdCache_InvalidMaxBytes_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	assert.Error(t, EvictColdCache(dir, 0))
	assert.Error(t, EvictColdCache(dir, -1))
}
