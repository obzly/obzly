package segments

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
	limit := ContainerMemoryLimit()
	if limit <= 0 {
		return ""
	}
	return fmt.Sprintf("%dMB", int64(float64(limit)*fraction)>>20)
}

// Caps for the handles that used to run with DuckDB's default memory_limit
// (80% of the container each, while a worker holds a dozen handles).
const (
	// WriterMemoryLimit caps the one database every writer shares: a flush
	// loads one L0 batch (INGEST_L0_TARGET_BYTES, 64 MB) or one direct-write
	// buffer (128 MB) into a table and copies it to Parquet, a few signals at
	// once; past the cap the tables spill instead of failing the flush.
	WriterMemoryLimit = "384MB"
	writerThreads     = 2
	// TailMergerMemoryLimit: the tail merger copies a project's pending
	// delta files (at most one L0 batch) into one file.
	TailMergerMemoryLimit = "256MB"
	TailMergerThreads     = 2
	// ShortLivedMemoryLimit: one-off reads of a single segment (reconcile's
	// row counts and stats), spilling past it.
	ShortLivedMemoryLimit = "256MB"
	shortLivedThreads     = 2
	// metadataMemoryLimit: handles that only read Parquet footers or write
	// an empty file (footer lookups, audit row counts, schema anchors).
	metadataMemoryLimit = "32MB"
	metadataThreads     = 1
)

// writerHandle is the database every Writer in the process shares.
var writerHandle = &sharedHandle{kind: "writer", limit: WriterMemoryLimit, threads: writerThreads}

// DefaultMaxSpillSize caps one handle's spill files until SetMaxSpillSize
// (QUERIER_SPILL_MAX_SIZE's default). DuckDB's own default is 90% of the
// free disk, which the segments and JetStream share.
const DefaultMaxSpillSize = "8GB"

// spillDirPrefix names a handle's own spill directory under a spill root.
const spillDirPrefix = "h-"

var (
	sizePattern = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)\s*([A-Za-z]*)$`)
	sizeUnits   = map[string]int64{
		"": 1, "b": 1, "bytes": 1,
		"kb": 1e3, "mb": 1e6, "gb": 1e9, "tb": 1e12,
		"kib": 1 << 10, "mib": 1 << 20, "gib": 1 << 30, "tib": 1 << 40,
	}
)

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

// ContainerShare is fraction of containerBytes (ContainerMemoryLimit) as a
// DuckDB memory_limit, kept within floorBytes..ceilingBytes; ceilingBytes
// without a container limit.
func ContainerShare(containerBytes int64, fraction float64, floorBytes, ceilingBytes int64) string {
	share := ceilingBytes
	if containerBytes > 0 {
		share = min(max(int64(float64(containerBytes)*fraction), floorBytes), ceilingBytes)
	}
	return fmt.Sprintf("%dMB", share/1_000_000)
}

// SetHandleLimits caps db at limit ("" leaves DuckDB's default) and threads
// (0 leaves DuckDB's default: every core of the node, not the pod's share),
// without a spill directory: for handles that never hold much.
func SetHandleLimits(db *sql.DB, limit string, threads int) error {
	var settings []string
	if limit != "" {
		settings = append(settings, fmt.Sprintf("SET memory_limit = '%s'", strings.ReplaceAll(limit, "'", "''")))
	}
	if threads > 0 {
		settings = append(settings, fmt.Sprintf("SET threads = %d", threads))
	}
	for _, setting := range settings {
		if _, err := db.Exec(setting); err != nil {
			return fmt.Errorf("failed to configure duckdb (%s): %w", setting, err)
		}
	}
	return nil
}

// MemoryLimitBytes reads a DuckDB size ("1GB", "820MB", "512MiB") the way
// DuckDB does (KB/MB/GB are powers of 1000, KiB/MiB/GiB of 1024); 0 for ""
// or anything else.
func MemoryLimitBytes(limit string) int64 {
	match := sizePattern.FindStringSubmatch(strings.TrimSpace(limit))
	if match == nil {
		return 0
	}
	value, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return 0
	}
	unit := strings.ToLower(match[2])
	multiplier, ok := sizeUnits[unit]
	if !ok {
		return 0
	}
	return int64(value * float64(multiplier))
}

// ContainerMemoryLimit is this container's cgroup memory limit in bytes, 0
// without one.
func ContainerMemoryLimit() int64 {
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
