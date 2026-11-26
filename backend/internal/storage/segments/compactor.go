package segments

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"obzly/internal/telemetry"

	"github.com/google/uuid"
	"golang.org/x/sync/semaphore"
)

// Compactor holds the schema-agnostic mechanics of merging segment files and
// deleting them. Which segments to merge or expire is per-stream policy
// (e.g. logs/compactor.LogCompactor).
type Compactor struct {
	db *sql.DB
	// spillDir: a private handle's spill directory ("" when shared).
	spillDir string
	shared   bool
	// gate admits one sorting statement, or several streaming ones, at a
	// time on db (shared by every compactor on the shared handle).
	gate *semaphore.Weighted
}

// CompactorOption adjusts a Compactor.
type CompactorOption func(*compactorOptions)

type compactorOptions struct {
	spillDir string
	shared   bool
}

// compactorMemoryLimit bounds a merge: DuckDB defaults to 80% of RAM per
// handle, and in-memory sorts of L0->L1 merges OOM-killed the worker. Past the
// limit a merge fails instead; L1MaxRawBytes keeps sorted merges inside it and
// cold merges don't sort. One thread keeps the writer to one row group buffer.
const (
	compactorMemoryLimit = "1GB"
	compactorThreads     = 1
)

// A sorting statement (an L1 merge, a legacy rewrite, an ordered rollup)
// sorts in memory and may need the whole limit, so it holds every slot of
// its handle's gate; a streaming one (a cold concatenation, a grouped
// rollup, which spill) holds one. On the shared handle three cleanup loops
// and the segment rewrite compact at once: their cold merges, the long
// I/O-bound ones, still overlap, but two sorts never split the limit.
const (
	compactorGateSlots = 4
	sortingWeight      = compactorGateSlots
	streamingWeight    = 1
)

// compactorHandle and compactorGate back every WithSharedHandle compactor.
var (
	compactorHandle = &sharedHandle{
		kind: "compactor", limit: compactorMemoryLimit, threads: compactorThreads,
		setup: func(db *sql.DB) error {
			if _, err := db.Exec("SET preserve_insertion_order = false"); err != nil {
				return err
			}
			return RegisterNormalizationFunctions(db)
		},
	}
	compactorGate = semaphore.NewWeighted(compactorGateSlots)
)

// placeSegmentFile moves a finished temp file to its segment name; a
// variable so tests can stop a merge at the point a crash would.
var placeSegmentFile = os.Rename

// WithSpillDir is DuckDB's temp_directory for operators that can spill
// (default: the OS temp directory).
func WithSpillDir(dir string) CompactorOption {
	return func(o *compactorOptions) { o.spillDir = dir }
}

// WithSharedHandle merges on the process's one compactor database, capped at
// compactorMemoryLimit for all streams together, instead of a database per
// stream (4 GB on a worker).
func WithSharedHandle() CompactorOption {
	return func(o *compactorOptions) { o.shared = true }
}

func NewCompactor(opts ...CompactorOption) (*Compactor, error) {
	cfg := compactorOptions{spillDir: filepath.Join(os.TempDir(), "obzly-compaction-spill")}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.shared {
		db, err := compactorHandle.acquire(cfg.spillDir)
		if err != nil {
			return nil, fmt.Errorf("failed to open the shared compaction duckdb handle: %w", err)
		}
		return &Compactor{db: db, shared: true, gate: compactorGate}, nil
	}
	// Each stream's compactor spills to its own directory (NewSpillDir).
	spillDir, err := NewSpillDir(cfg.spillDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create compaction spill directory: %w", err)
	}

	db, err := sql.Open("duckdb", "")
	if err != nil {
		_ = os.RemoveAll(spillDir)
		return nil, fmt.Errorf("failed to open duckdb handle: %w", err)
	}
	// Database-wide settings in DuckDB, so every connection gets them.
	for _, setting := range []string{
		fmt.Sprintf("SET memory_limit = '%s'", compactorMemoryLimit),
		fmt.Sprintf("SET threads = %d", compactorThreads),
		fmt.Sprintf("SET temp_directory = '%s'", strings.ReplaceAll(spillDir, "'", "''")),
		"SET preserve_insertion_order = false",
	} {
		if _, err := db.Exec(setting); err != nil {
			_ = db.Close()
			_ = os.RemoveAll(spillDir)
			return nil, fmt.Errorf("failed to configure compaction duckdb handle (%s): %w", setting, err)
		}
	}
	if err := RegisterNormalizationFunctions(db); err != nil {
		_ = db.Close()
		_ = os.RemoveAll(spillDir)
		return nil, err
	}
	telemetry.RegisterDuckDB("compactor", db, MemoryLimitBytes(compactorMemoryLimit))
	return &Compactor{db: db, spillDir: spillDir, gate: semaphore.NewWeighted(compactorGateSlots)}, nil
}

func (c *Compactor) Close() error {
	if c.shared {
		return compactorHandle.release()
	}
	telemetry.UnregisterDuckDB(c.db)
	err := c.db.Close()
	_ = os.RemoveAll(c.spillDir)
	return err
}

// MergeSegments concatenates records' files into one new file under
// dataDir/<stream>/<project_id>/. All records must share ProjectID and Stream.
// Rows are sorted by CompactionSortColumns for row-group pruning only; queries
// never rely on file order.
func (c *Compactor) MergeSegments(records []*SegmentRecord, dataDir string) (*Segment, error) {
	return c.MergeSegmentsAt(records, dataDir, 0)
}

// MergeSegmentsAt merges records into one file at level (0: one above the
// inputs, at least LevelCompacted). LevelCold merges concatenate instead of
// sorting: their inputs are sorted L1 files, and a sort of 512 MB-1 GB
// would need several GB of memory.
func (c *Compactor) MergeSegmentsAt(records []*SegmentRecord, dataDir string, level int) (*Segment, error) {
	return c.MergeSegmentsWhere(records, dataDir, level, "", nil)
}

// MergeSegmentsWhere is MergeSegmentsAt keeping only the rows where holds
// (e.g. trace sampling into cold files; "" keeps all). Its arguments bind
// after the input files.
func (c *Compactor) MergeSegmentsWhere(
	records []*SegmentRecord, dataDir string, level int, where string, whereArgs []any,
) (*Segment, error) {
	if len(records) == 0 {
		return nil, errors.New("no segments to merge")
	}

	projectID := records[0].ProjectID
	stream := records[0].Stream
	paths := make([]string, len(records))
	inputStats := make([]*SegmentStats, len(records))
	var totalRows int64
	var totalRawBytes int64
	minTime, maxTime := records[0].MinTime, records[0].MaxTime
	inputLevel := LevelCompacted
	version := records[0].SchemaVersion

	for i, record := range records {
		if record.ProjectID != projectID || record.Stream != stream {
			return nil, errors.New("cannot merge segments across different projects or streams")
		}
		if record.IsLegacy() != records[0].IsLegacy() {
			return nil, errors.New("cannot merge legacy segments with current ones")
		}
		version = max(version, record.SchemaVersion)
		paths[i] = record.Path
		inputStats[i] = record.Stats
		inputLevel = max(inputLevel, record.Level)
		totalRows += record.RowCount
		totalRawBytes += record.RawSizeBytes
		if record.MinTime.Before(minTime) {
			minTime = record.MinTime
		}
		if record.MaxTime.After(maxTime) {
			maxTime = record.MaxTime
		}
	}

	segmentDir := filepath.Join(dataDir, stream, projectID.String())
	if err := os.MkdirAll(segmentDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create project segment directory: %w", err)
	}

	outputPath := filepath.Join(
		segmentDir,
		fmt.Sprintf("%d_%s.parquet", time.Now().UnixNano(), uuid.NewString()[:8]),
	)

	if level == 0 {
		level = inputLevel
	}
	fromExpr, args := ParquetFilesExpr(paths)
	query := "SELECT * FROM " + fromExpr
	_, deduplicates := DeduplicatingMerges[stream]
	sorts := deduplicates || level < LevelCold
	recount := false
	if deduplicates {
		query = fmt.Sprintf(DeduplicatingMerges[stream], fromExpr)
	} else if level < LevelCold {
		source, err := c.withoutRepeatedIDs(stream, fromExpr, args)
		if err != nil {
			return nil, err
		}
		recount = source != fromExpr
		sorted, err := sortedSelect(c.db, stream, source, args, !records[0].IsLegacy())
		if err != nil {
			return nil, err
		}
		query = sorted
	}
	if where != "" {
		query = "SELECT * FROM (" + query + ") WHERE " + where
		args = append(append([]any{}, args...), whereArgs...)
	}
	if err := c.copyToSegment(query, args, outputPath, sorts); err != nil {
		return nil, fmt.Errorf("failed to merge segments: %w", err)
	}

	fileInfo, err := os.Stat(outputPath)
	if err != nil {
		_ = os.Remove(outputPath)
		return nil, fmt.Errorf("failed to stat merged segment: %w", err)
	}
	if deduplicates || recount || where != "" {
		if err := c.db.QueryRow("SELECT count(*) FROM read_parquet(?)", outputPath).Scan(&totalRows); err != nil {
			_ = os.Remove(outputPath)
			return nil, fmt.Errorf("failed to count merged segment rows: %w", err)
		}
	}

	return &Segment{
		Path:          outputPath,
		ProjectID:     projectID,
		Stream:        stream,
		RowCount:      int(totalRows),
		SizeBytes:     fileInfo.Size(),
		RawSizeBytes:  totalRawBytes,
		MinTime:       minTime,
		MaxTime:       maxTime,
		CreatedAt:     time.Now().UTC(),
		SchemaVersion: version,
		Stats:         mergeStats(stream, inputStats),
		Level:         level,
	}, nil
}

// RewriteLegacySegment copies a version 1 segment (read from sourcePath,
// which may be a downloaded copy) to a new current-version file with names
// normalized - the same rewrite queries apply on read.
func (c *Compactor) RewriteLegacySegment(record *SegmentRecord, sourcePath, dataDir string) (*Segment, error) {
	segmentDir := filepath.Join(dataDir, record.Stream, record.ProjectID.String())
	if err := os.MkdirAll(segmentDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create project segment directory: %w", err)
	}
	outputPath := filepath.Join(
		segmentDir,
		fmt.Sprintf("%d_%s.parquet", time.Now().UnixNano(), uuid.NewString()[:8]),
	)

	fromExpr, args := SourceExpr(record.Stream, []ResolvedFile{{Path: sourcePath, Legacy: true}})
	query, err := sortedSelect(c.db, record.Stream, fromExpr, args, true)
	if err != nil {
		return nil, err
	}
	if err := c.copyToSegment(query, args, outputPath, true); err != nil {
		return nil, fmt.Errorf("failed to rewrite segment: %w", err)
	}

	fileInfo, err := os.Stat(outputPath)
	if err != nil {
		_ = os.Remove(outputPath)
		return nil, fmt.Errorf("failed to stat rewritten segment: %w", err)
	}
	stats, err := computeStats(c.db, record.Stream, "read_parquet(?)", []any{outputPath})
	if err != nil {
		_ = os.Remove(outputPath)
		return nil, err
	}
	return &Segment{
		Path: outputPath, ProjectID: record.ProjectID, Stream: record.Stream,
		RowCount: int(record.RowCount), SizeBytes: fileInfo.Size(), RawSizeBytes: record.RawSizeBytes,
		MinTime: record.MinTime, MaxTime: record.MaxTime, CreatedAt: time.Now().UTC(),
		SchemaVersion: CurrentSchemaVersion, Stats: stats, Level: record.Level,
	}, nil
}

// DeleteSegmentFiles removes the segments' local files; an already-missing
// file is not an error. Callers remove the catalog rows (DeleteByIDs).
func (c *Compactor) DeleteSegmentFiles(records []*SegmentRecord) error {
	for _, record := range records {
		if err := os.Remove(record.Path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to delete segment file %s: %w", record.Path, err)
		}
		_ = os.Remove(BloomPath(record.Path))
	}
	return nil
}

// copyToSegment writes query's rows to a temp file beside outputPath and
// renames it into place, as Writer.writeSegment does: a crash mid-COPY
// leaves only a ".parquet.tmp" (swept by CleanupStaleTempFiles), never a
// truncated file under a segment's name. A failed merge is retried
// (MaxCompactionAttempts), so the temp file is removed on failure.
func (c *Compactor) copyToSegment(query string, args []any, outputPath string, sorts bool) error {
	tempPath := outputPath + ".tmp"
	if err := c.execGated(sorts, compactionCopySQL(query, tempPath), args...); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if err := placeSegmentFile(tempPath, outputPath); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("failed to finalize segment file: %w", err)
	}
	return nil
}

// execGated runs a statement that reads or writes whole segments once the
// gate admits it (see sortingWeight).
// withoutRepeatedIDs is fromExpr keeping one row per CompactionUniqueColumn
// value, or fromExpr itself when no id repeats. The check reads only the id
// column, so a merge without repeats (nearly all) costs little more.
func (c *Compactor) withoutRepeatedIDs(stream, fromExpr string, args []any) (string, error) {
	column := CompactionUniqueColumn[stream]
	if column == "" {
		return fromExpr, nil
	}
	present, err := columnsOf(c.db, fromExpr, args)
	if err != nil {
		return "", err
	}
	if !present[column] {
		return fromExpr, nil
	}
	if err := c.gate.Acquire(context.Background(), streamingWeight); err != nil {
		return "", err
	}
	var repeated int64
	query := `SELECT count("` + column + `") - count(DISTINCT "` + column + `") FROM ` + fromExpr
	err = c.db.QueryRow(query, args...).Scan(&repeated)
	c.gate.Release(streamingWeight)
	if err != nil {
		return "", fmt.Errorf("failed to check merge for repeated ids: %w", err)
	}
	if repeated == 0 {
		return fromExpr, nil
	}
	telemetry.CompactionRepeatedRows.WithLabelValues(stream).Add(float64(repeated))
	return `(SELECT * FROM ` + fromExpr + ` QUALIFY "` + column + `" IS NULL OR row_number() OVER (PARTITION BY "` +
		column + `") = 1)`, nil
}

func (c *Compactor) execGated(sorts bool, statement string, args ...any) error {
	weight := int64(streamingWeight)
	if sorts {
		weight = sortingWeight
	}
	if err := c.gate.Acquire(context.Background(), weight); err != nil {
		return err
	}
	defer c.gate.Release(weight)
	_, err := c.db.Exec(statement, args...)
	return err
}
