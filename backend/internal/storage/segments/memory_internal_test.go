package segments

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

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
