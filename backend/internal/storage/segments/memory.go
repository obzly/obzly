package segments

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// cgroupLimitFiles: cgroup v2, then v1. A v1 "no limit" is a huge number.
var cgroupLimitFiles = []string{"/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes"}

// unlimitedCgroupBytes: v1 reports no limit as about 2^63.
const unlimitedCgroupBytes = 1 << 60

// HandleMemoryLimit is fraction of this container's memory limit as a DuckDB
// memory_limit ("820MB"), or "" without a limit. DuckDB defaults every handle
// to 80% of the limit and a process has several, which got a worker
// OOM-killed; a cap makes big queries spill instead.
func HandleMemoryLimit(fraction float64) string {
	limit := containerMemoryLimit()
	if limit <= 0 {
		return ""
	}
	return fmt.Sprintf("%dMB", int64(float64(limit)*fraction)>>20)
}

// DefaultMaxSpillSize caps one handle's spill files until SetMaxSpillSize
// (QUERIER_SPILL_MAX_SIZE's default). DuckDB's own default is 90% of the
// free disk, which the segments and JetStream share.
const DefaultMaxSpillSize = "8GB"

// spillDirPrefix names a handle's own spill directory under a spill root.
const spillDirPrefix = "h-"

var (
	maxSpillSize = DefaultMaxSpillSize
	// cleanedSpillRoots: roots this process already swept of a previous run's
	// handle directories. The lock keeps a sweep from removing a directory
	// another handle just created.
	cleanedSpillRoots   = map[string]bool{}
	cleanedSpillRootsMu sync.Mutex
)

// SetMaxSpillSize caps every query handle's spill files ("" keeps the
// default). Call it at startup, before any handle is opened.
func SetMaxSpillSize(size string) {
	if strings.TrimSpace(size) != "" {
		maxSpillSize = size
	}
}

// ConfigureMemory caps db at limit ("" leaves DuckDB's default) and its
// spill files at the max spill size, spilling to a directory of its own
// under spillRoot (NewSpillDir). It returns that directory ("" without a
// limit) for the caller to remove once db is closed.
func ConfigureMemory(db *sql.DB, limit, spillRoot string) (string, error) {
	setting := fmt.Sprintf("SET max_temp_directory_size = '%s'", strings.ReplaceAll(maxSpillSize, "'", "''"))
	if _, err := db.Exec(setting); err != nil {
		return "", fmt.Errorf("failed to cap the duckdb spill size: %w", err)
	}
	if limit == "" {
		return "", nil
	}
	spillDir, err := NewSpillDir(spillRoot)
	if err != nil {
		return "", err
	}
	for _, setting := range []string{
		fmt.Sprintf("SET memory_limit = '%s'", strings.ReplaceAll(limit, "'", "''")),
		fmt.Sprintf("SET temp_directory = '%s'", strings.ReplaceAll(spillDir, "'", "''")),
	} {
		if _, err := db.Exec(setting); err != nil {
			_ = os.RemoveAll(spillDir)
			return "", fmt.Errorf("failed to configure duckdb memory (%s): %w", setting, err)
		}
	}
	return spillDir, nil
}

// NewSpillDir creates a spill directory for one DuckDB handle under root:
// handles sharing one wrote same-named spill files and crashed (SIGSEGV/SIGBUS).
// The first call per root removes a previous run's directories, since DuckDB
// deletes spill files only on a clean Close.
func NewSpillDir(root string) (string, error) {
	root = filepath.Clean(root)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("failed to create duckdb spill directory: %w", err)
	}
	cleanedSpillRootsMu.Lock()
	defer cleanedSpillRootsMu.Unlock()
	if !cleanedSpillRoots[root] {
		cleanedSpillRoots[root] = true
		stale, _ := filepath.Glob(filepath.Join(root, spillDirPrefix+"*"))
		for _, dir := range stale {
			_ = os.RemoveAll(dir)
		}
	}
	dir, err := os.MkdirTemp(root, spillDirPrefix)
	if err != nil {
		return "", fmt.Errorf("failed to create duckdb spill directory: %w", err)
	}
	return dir, nil
}

func containerMemoryLimit() int64 {
	for _, path := range cgroupLimitFiles {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		value := strings.TrimSpace(string(raw))
		if value == "max" {
			return 0
		}
		limit, err := strconv.ParseInt(value, 10, 64)
		if err != nil || limit <= 0 || limit >= unlimitedCgroupBytes {
			return 0
		}
		return limit
	}
	return 0
}
