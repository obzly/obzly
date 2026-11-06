package segments

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"obzly/internal/telemetry"

	"golang.org/x/sync/errgroup"
)

// Column-only reads of cold segments: a query reading a few columns of a large
// uncached segment fetches only those columns' chunks and the footer into a
// sparse local file at their own offsets; DuckDB never reads the holes. Later
// queries add more columns to the same file. Small or legacy segments, and
// queries reading most of a file's bytes, download it whole.
const (
	// defaultPartialMinSize: a smaller segment is downloaded whole.
	defaultPartialMinSize = 64 << 20
	// partialMaxShare: needing more than this share of a file's bytes,
	// downloading it whole is as cheap and serves every later query.
	partialMaxShare = 0.6
	// partialGapBytes: ranges this close are fetched as one request.
	partialGapBytes = 256 << 10
	// partialFetchers: ranges fetched at once per file.
	partialFetchers = 8
	partialSuffix   = ".partial.parquet"
	partialMeta     = ".cols"
)

// partialEnabled turns column-only reads on (QUERY_PARTIAL_DOWNLOADS);
// partialMinSize is defaultPartialMinSize (tests lower it).
var (
	partialEnabled = true
	partialMinSize = int64(defaultPartialMinSize)
)

// SetPartialDownloads turns column-only reads of cold segments on or off.
func SetPartialDownloads(enabled bool) { partialEnabled = enabled }

// ReferencedColumns is the columns of stream's schema that sql names, or
// nil when it may read them all (SELECT *, an unknown stream).
func ReferencedColumns(stream, sqlText string) []string {
	value, ok := anchorSchemas.Load(stream)
	if !ok || selectsAll.MatchString(sqlText) {
		return nil
	}
	schema, _ := value.(Schema)
	promoted := strings.Contains(sqlText, "{{promoted")
	var columns []string
	for _, column := range schema.Columns {
		named := regexp.MustCompile(`\b` + regexp.QuoteMeta(column.Name) + `\b`).MatchString(sqlText)
		// A promoted field token reads its column or, row by row, fields.
		if named || (promoted && (column.Name == "fields" || strings.HasPrefix(column.Name, "promoted_"))) {
			columns = append(columns, column.Name)
		}
	}
	return columns
}

// selectsAll: SELECT * (also * EXCLUDE / * REPLACE, t.*) reads every column.
var selectsAll = regexp.MustCompile(`(?i)select\s+(distinct\s+)?\*|,\s*\*\s*(,|from|exclude|replace)|\w\.\*`)

// partialLocks serializes work on one partial file within this process.
var partialLocks sync.Map // path -> *sync.Mutex

// metadataDB reads Parquet footers (parquet_metadata).
var (
	metadataOnce sync.Once
	metadataDB   *sql.DB
	errMetadata  error
)

func footerDB() (*sql.DB, error) {
	metadataOnce.Do(func() {
		metadataDB, errMetadata = sql.Open("duckdb", "")
		if errMetadata == nil {
			metadataDB.SetMaxOpenConns(2)
		}
	})
	return metadataDB, errMetadata
}

// errWholeFile: this read is better served by downloading the file whole.
var errWholeFile = errors.New("download whole")

// resolvePartial is record's file holding at least columns, fetched column
// by column into the cache; errWholeFile when downloading it whole is
// better.
func resolvePartial(
	ctx context.Context, store *ObjectStore, cacheDir string, record *SegmentRecord, columns []string,
) (string, error) {
	if !partialEnabled || columns == nil || record.IsLegacy() || record.SizeBytes < partialMinSize {
		return "", errWholeFile
	}
	path := filepath.Join(cacheDir, record.Stream, record.ProjectID.String(), record.ID.String()+partialSuffix)
	lock, _ := partialLocks.LoadOrStore(path, &sync.Mutex{})
	mutex, _ := lock.(*sync.Mutex)
	mutex.Lock()
	defer mutex.Unlock()

	have := readPartialMeta(path)
	if _, err := os.Stat(path); err != nil {
		have = map[string]bool{}
	}
	missing := slices.DeleteFunc(slices.Clone(columns), func(column string) bool { return have[column] })
	if len(missing) == 0 && have[footerKey] {
		now := time.Now()
		_ = os.Chtimes(path, now, now)
		telemetry.QueryPartialReads.WithLabelValues(record.Stream, "cached").Inc()
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()

	if !have[footerKey] {
		have = map[string]bool{}
		if err := file.Truncate(record.SizeBytes); err != nil {
			return "", err
		}
		if err := fetchFooter(ctx, store, record, file); err != nil {
			return "", err
		}
		have[footerKey] = true
		if err := writePartialMeta(path, have); err != nil {
			return "", err
		}
	}

	ranges, needed, err := columnRanges(path, missing)
	if err != nil {
		return "", err
	}
	// Mostly the whole file anyway: download it whole (and drop this one).
	if float64(needed+partialBytes(path, have)) > partialMaxShare*float64(record.SizeBytes) {
		_ = os.Remove(path)
		_ = os.Remove(path + partialMeta)
		return "", errWholeFile
	}
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(partialFetchers)
	for _, r := range ranges {
		group.Go(func() error {
			data, err := store.ReadRange(groupCtx, record.ObjectKey, r.offset, r.length)
			if err != nil {
				return err
			}
			_, err = file.WriteAt(data, r.offset)
			return err
		})
	}
	if err := group.Wait(); err != nil {
		return "", fmt.Errorf("failed to fetch columns of segment %s: %w", record.ID, err)
	}
	// No fsync: a cache file needs no durability (reads see the page cache),
	// and macOS's full fsync fills a sparse file's holes.
	for _, column := range missing {
		have[column] = true
	}
	if err := writePartialMeta(path, have); err != nil {
		return "", err
	}
	telemetry.QueryPartialReads.WithLabelValues(record.Stream, "fetched").Inc()
	return path, nil
}

const footerKey = "\x00footer"

// fetchFooter writes the file's header magic and footer at their offsets.
func fetchFooter(ctx context.Context, store *ObjectStore, record *SegmentRecord, file *os.File) error {
	size := record.SizeBytes
	tail, err := store.ReadRange(ctx, record.ObjectKey, size-8, 8)
	if err != nil {
		return err
	}
	if len(tail) != 8 || string(tail[4:]) != "PAR1" {
		return fmt.Errorf("segment %s is not a Parquet file", record.ID)
	}
	footerLength := int64(binary.LittleEndian.Uint32(tail[:4]))
	footer, err := store.ReadRange(ctx, record.ObjectKey, size-8-footerLength, footerLength)
	if err != nil {
		return err
	}
	if _, err := file.WriteAt([]byte("PAR1"), 0); err != nil {
		return err
	}
	if _, err := file.WriteAt(footer, size-8-footerLength); err != nil {
		return err
	}
	_, err = file.WriteAt(tail, size-8)
	return err
}

type byteRange struct{ offset, length int64 }

// columnRanges is the byte ranges of columns' chunks (and bloom filters)
// in the file at path, merged, and their total size.
func columnRanges(path string, columns []string) ([]byteRange, int64, error) {
	if len(columns) == 0 {
		return nil, 0, nil
	}
	db, err := footerDB()
	if err != nil {
		return nil, 0, err
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(columns)), ", ")
	args := []any{path}
	for _, column := range columns {
		args = append(args, column)
	}
	rows, err := db.Query(`SELECT COALESCE(NULLIF(dictionary_page_offset, 0), data_page_offset),
		total_compressed_size, COALESCE(bloom_filter_offset, 0), COALESCE(bloom_filter_length, 0)
		FROM parquet_metadata(?) WHERE path_in_schema IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to read segment footer: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ranges []byteRange
	var total int64
	for rows.Next() {
		var start, length, bloomOffset, bloomLength int64
		if err := rows.Scan(&start, &length, &bloomOffset, &bloomLength); err != nil {
			return nil, 0, err
		}
		ranges = append(ranges, byteRange{start, length})
		total += length
		if bloomOffset > 0 && bloomLength > 0 {
			ranges = append(ranges, byteRange{bloomOffset, bloomLength})
			total += bloomLength
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return mergeRanges(ranges), total, nil
}

func mergeRanges(ranges []byteRange) []byteRange {
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].offset < ranges[j].offset })
	var merged []byteRange
	for _, r := range ranges {
		if n := len(merged); n > 0 && r.offset <= merged[n-1].offset+merged[n-1].length+partialGapBytes {
			end := max(merged[n-1].offset+merged[n-1].length, r.offset+r.length)
			merged[n-1].length = end - merged[n-1].offset
			continue
		}
		merged = append(merged, r)
	}
	return merged
}

// partialBytes is the size of the column chunks already in the file.
func partialBytes(path string, have map[string]bool) int64 {
	var columns []string
	for column := range have {
		if column != footerKey {
			columns = append(columns, column)
		}
	}
	_, total, err := columnRanges(path, columns)
	if err != nil {
		return 0
	}
	return total
}

func readPartialMeta(path string) map[string]bool {
	have := map[string]bool{}
	raw, err := os.ReadFile(path + partialMeta)
	if err != nil {
		return have
	}
	var columns []string
	if json.Unmarshal(raw, &columns) == nil {
		for _, column := range columns {
			have[column] = true
		}
	}
	return have
}

func writePartialMeta(path string, have map[string]bool) error {
	columns := make([]string, 0, len(have))
	for column := range have {
		columns = append(columns, column)
	}
	sort.Strings(columns)
	raw, err := json.Marshal(columns)
	if err != nil {
		return err
	}
	temp := path + partialMeta + ".tmp"
	if err := os.WriteFile(temp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(temp, path+partialMeta)
}
