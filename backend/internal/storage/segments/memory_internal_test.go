package segments

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func Test_HandleMemoryLimit_ReadsTheContainerLimit(t *testing.T) {
	dir := t.TempDir()
	original := cgroupLimitFiles
	t.Cleanup(func() { cgroupLimitFiles = original })
	cases := []struct {
		name, content, want string
	}{
		{"cgroup v2 limit", "2147483648\n", "819MB"},
		{"cgroup v2 no limit", "max\n", ""},
		{"cgroup v1 no limit", "9223372036854771712\n", ""},
	}
	for _, tc := range cases {
		path := filepath.Join(dir, tc.name)
		require.NoError(t, os.WriteFile(path, []byte(tc.content), 0o644))
		cgroupLimitFiles = []string{path}

		require.Equal(t, tc.want, HandleMemoryLimit(0.4), tc.name)
	}
	cgroupLimitFiles = []string{filepath.Join(dir, "missing")}
	require.Empty(t, HandleMemoryLimit(0.4), "no cgroup file: DuckDB's default")
}

func Test_MemoryLimitBytes_ReadsSizesTheWayDuckDBDoes(t *testing.T) {
	cases := map[string]int64{
		"1GB": 1_000_000_000, "512MB": 512_000_000, "819MB": 819_000_000, "1.5GB": 1_500_000_000,
		"256MiB": 256 << 20, "2 GiB": 2 << 30, "64mb": 64_000_000, "": 0, "lots": 0, "12XB": 0,
	}
	for limit, want := range cases {
		require.Equal(t, want, MemoryLimitBytes(limit), limit)
	}
}

func Test_SetHandleLimits_CapsMemoryAndThreadsWithoutSpilling(t *testing.T) {
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	require.NoError(t, SetHandleLimits(db, metadataMemoryLimit, metadataThreads))

	limit, threads := handleSettings(t, db)
	require.Equal(t, "30.5 MiB", limit)
	require.Equal(t, "1", threads)
}

func Test_NewWriter_WritersShareOneCappedHandle_AndFlushAfterItsSpillDirectoryWasRemoved(t *testing.T) {
	schema := Schema{Stream: "test_capped_writer", Columns: []ColumnDef{{Name: "v", Type: "BIGINT"}}}
	first, err := NewWriter(t.TempDir(), schema)
	require.NoError(t, err)
	second, err := NewWriter(t.TempDir(), schema)
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Close() })

	require.Same(t, first.db, second.db, "every writer flushes through one database")
	limit, threads := handleSettings(t, first.db)
	require.Equal(t, "366.2 MiB", limit, "WriterMemoryLimit, not 80% of RAM")
	require.Equal(t, "2", threads)
	require.NoError(t, first.Close())

	// The query tail empties its directory after opening its writers, and the
	// shared spill directory may be under it.
	require.NoError(t, os.RemoveAll(writerHandle.spillDirectory()))
	now := time.Now().UTC()
	segment, err := second.writeSegment(uuid.New(), [][]any{{int64(1)}, {int64(2)}}, 16, now, now)
	require.NoError(t, err, "one writer's Close leaves the shared handle open for the others")
	require.FileExists(t, segment.Path)
	require.DirExists(t, writerHandle.spillDirectory())
}

func Test_SharedHandle_LastReleaseClosesTheDatabaseAndRemovesItsSpillDirectory(t *testing.T) {
	handle := &sharedHandle{kind: "test_shared", limit: "64MB", threads: 1}
	root := filepath.Join(t.TempDir(), "spill")

	first, err := handle.acquire(root)
	require.NoError(t, err)
	second, err := handle.acquire(filepath.Join(t.TempDir(), "elsewhere"))
	require.NoError(t, err)
	require.Same(t, first, second)
	spillDir := handle.spillDirectory()
	require.Equal(t, root, filepath.Dir(spillDir), "the first acquire chooses the spill directory")

	require.NoError(t, handle.release())
	require.NoError(t, first.Ping(), "still open while referenced")
	require.NoError(t, handle.release())
	require.Error(t, first.Ping(), "closed with the last reference")
	require.NoDirExists(t, spillDir)
	require.Error(t, handle.release(), "more releases than acquires")

	reopened, err := handle.acquire(root)
	require.NoError(t, err)
	require.NotSame(t, first, reopened)
	require.NoError(t, handle.release())
}

func Test_NewCompactor_SharedHandle_OneDatabaseForEveryStream(t *testing.T) {
	logs, err := NewCompactor(WithSpillDir(t.TempDir()), WithSharedHandle())
	require.NoError(t, err)
	t.Cleanup(func() { _ = logs.Close() })
	spans, err := NewCompactor(WithSpillDir(t.TempDir()), WithSharedHandle())
	require.NoError(t, err)
	t.Cleanup(func() { _ = spans.Close() })

	require.Same(t, logs.db, spans.db)
	require.Same(t, logs.gate, spans.gate)
	limit, threads := handleSettings(t, logs.db)
	require.Equal(t, "953.6 MiB", limit)
	require.Equal(t, "1", threads)
	var preserveOrder string
	require.NoError(t, logs.db.QueryRow("SELECT current_setting('preserve_insertion_order')::VARCHAR").
		Scan(&preserveOrder))
	require.Equal(t, "false", preserveOrder)
}

func Test_CompactorGate_ASortWaitsForStreamingStatements_WhichStillRunTogether(t *testing.T) {
	compactor, err := NewCompactor(WithSpillDir(t.TempDir()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = compactor.Close() })
	ctx := context.Background()

	// Two streaming statements in flight.
	require.NoError(t, compactor.gate.Acquire(ctx, streamingWeight))
	require.NoError(t, compactor.gate.Acquire(ctx, streamingWeight))
	require.True(t, compactor.gate.TryAcquire(streamingWeight), "a third streaming one joins them")
	compactor.gate.Release(streamingWeight)

	sorted := make(chan error, 1)
	go func() { sorted <- compactor.execGated(true, "SELECT 1") }()
	select {
	case <-sorted:
		t.Fatal("a sort must wait for the streaming statements")
	case <-time.After(100 * time.Millisecond):
	}
	compactor.gate.Release(2 * streamingWeight)
	select {
	case err := <-sorted:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the sort never ran")
	}
}

func Test_NewTokenBlooms_LogsAndAILogsShareOneDatabase(t *testing.T) {
	logs, err := NewTokenBlooms(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = logs.Close() })
	aiLogs, err := NewTokenBlooms(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = aiLogs.Close() })

	require.Same(t, logs.db, aiLogs.db)
	limit, _ := handleSettings(t, logs.db)
	require.Equal(t, "366.2 MiB", limit)
}

func Test_ConfigureMemory_CapsTheHandle(t *testing.T) {
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	spillDir, err := ConfigureMemory(db, "819MB", filepath.Join(t.TempDir(), "spill"))
	require.NoError(t, err)

	var limit, maxTemp string
	require.NoError(t, db.QueryRow(
		"SELECT current_setting('memory_limit'), current_setting('max_temp_directory_size')",
	).Scan(&limit, &maxTemp))
	require.Equal(t, "781.0 MiB", limit)
	require.Equal(t, "7.4 GiB", maxTemp, "spill capped at DefaultMaxSpillSize, not 90% of the disk")
	require.DirExists(t, spillDir)

	noLimitDir, err := ConfigureMemory(db, "", t.TempDir())
	require.NoError(t, err)
	require.Empty(t, noLimitDir, "no limit: no spill directory")
}

func Test_ConfigureMemory_EachHandleSpillsToItsOwnDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spill")
	stale := filepath.Join(root, spillDirPrefix+"left-by-a-killed-process")
	require.NoError(t, os.MkdirAll(stale, 0o755))
	unrelated := filepath.Join(root, "token-bloom-logs")
	require.NoError(t, os.MkdirAll(unrelated, 0o755))

	dirs := make([]string, 0, 2)
	for range 2 {
		db, err := sql.Open("duckdb", "")
		require.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })
		dir, err := ConfigureMemory(db, "256MB", root)
		require.NoError(t, err)

		var tempDir string
		require.NoError(t, db.QueryRow("SELECT current_setting('temp_directory')").Scan(&tempDir))
		require.Equal(t, dir, tempDir)
		require.Equal(t, root, filepath.Dir(dir))
		dirs = append(dirs, dir)
	}

	require.NotEqual(t, dirs[0], dirs[1])
	require.DirExists(t, dirs[0], "the second handle's sweep must not remove the first's directory")
	require.NoDirExists(t, stale)
	require.DirExists(t, unrelated)
}

// handleSettings is db's memory_limit and threads.
func handleSettings(t *testing.T, db *sql.DB) (limit, threads string) {
	t.Helper()
	require.NoError(t, db.QueryRow(
		"SELECT current_setting('memory_limit'), current_setting('threads')::VARCHAR",
	).Scan(&limit, &threads))
	return limit, threads
}
