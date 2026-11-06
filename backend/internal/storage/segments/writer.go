package segments

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"obzly/internal/telemetry"
	"obzly/internal/util/logger"

	"github.com/google/uuid"
	duckdb "github.com/marcboeker/go-duckdb/v2"
	"github.com/prometheus/client_golang/prometheus"
)

// Default flush thresholds (OpenObserve's 128MB / 600s memtable defaults).
const (
	DefaultMaxRows  = 100_000
	DefaultMaxBytes = 128 * 1024 * 1024
	DefaultMaxAge   = 10 * time.Minute
)

const (
	// requeueLimitFactor caps the rows a failed flush keeps (in multiples of
	// maxRows/maxBytes), so a persistent failure such as a full disk can't
	// grow memory without bound.
	requeueLimitFactor = 4
	// flushRetryBackoff spaces out size-triggered retries after a failed
	// flush, so each Append doesn't rebuild a segment that fails again.
	flushRetryBackoff = 10 * time.Second
)

// Writer buffers rows per project in memory and flushes each project's buffer to its own
// Parquet segment once a threshold is crossed. One Writer serves one stream (schema).
type Writer struct {
	dataDir string
	schema  Schema
	db      *sql.DB

	maxRows  int
	maxBytes int64
	maxAge   time.Duration

	buffers sync.Map // uuid.UUID -> *projectBuffer

	metrics writerMetrics

	requeueFailed bool
}

// writerMetrics caches this stream's label children so the per-row Append
// path does no label lookup.
type writerMetrics struct {
	appendedRows  prometheus.Counter
	appendedBytes prometheus.Counter
	bufferedRows  prometheus.Gauge
	bufferedBytes prometheus.Gauge
	flushOK       prometheus.Counter
	flushError    prometheus.Counter
	flushedRows   prometheus.Counter
	lostRows      prometheus.Counter
	flushDuration prometheus.Observer
	flushSize     prometheus.Observer
	flushLag      prometheus.Observer
}

type Option func(*Writer)

func WithMaxRows(n int) Option          { return func(w *Writer) { w.maxRows = n } }
func WithMaxBytes(n int64) Option       { return func(w *Writer) { w.maxBytes = n } }
func WithMaxAge(d time.Duration) Option { return func(w *Writer) { w.maxAge = d } }

// WithRequeueOnFlushFailure keeps a failed flush's rows buffered for the
// next flush. Only for writers whose rows exist nowhere else (direct
// writes): on the JetStream path an unacked batch is redelivered, so
// keeping its rows would write them twice.
func WithRequeueOnFlushFailure() Option { return func(w *Writer) { w.requeueFailed = true } }

type projectBuffer struct {
	mu           sync.Mutex
	rows         [][]any
	approxBytes  int64
	minTime      time.Time
	maxTime      time.Time
	oldestAppend time.Time
	// retryAt holds off size-triggered flushes after a failed one.
	retryAt time.Time
}

// pendingRows is a buffer's contents taken out for one flush.
type pendingRows struct {
	rows         [][]any
	rawBytes     int64
	minTime      time.Time
	maxTime      time.Time
	oldestAppend time.Time
}

// NewWriter opens an in-process DuckDB handle used only to build segment files and creates
// the stream's segment directory under dataDir.
func NewWriter(dataDir string, schema Schema, opts ...Option) (*Writer, error) {
	streamDir := filepath.Join(dataDir, schema.Stream)
	if err := os.MkdirAll(streamDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create segment directory %q: %w", streamDir, err)
	}

	db, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, fmt.Errorf("failed to open duckdb handle: %w", err)
	}

	w := &Writer{
		dataDir:  dataDir,
		schema:   schema,
		db:       db,
		maxRows:  DefaultMaxRows,
		maxBytes: DefaultMaxBytes,
		maxAge:   DefaultMaxAge,
		metrics:  newWriterMetrics(schema.Stream),
	}
	for _, opt := range opts {
		opt(w)
	}
	telemetry.RegisterDuckDB("writer_"+schema.Stream, db)

	return w, nil
}

// Append adds one row to the project's buffer; rowBytes is its estimated size. When a
// threshold is crossed the buffer is flushed and the Segment returned, else (nil, nil).
func (w *Writer) Append(projectID uuid.UUID, row []any, rowBytes int64, ts time.Time) (*Segment, error) {
	if len(row) != len(w.schema.Columns) {
		return nil, fmt.Errorf(
			"row has %d values but schema %q has %d columns", len(row), w.schema.Stream, len(w.schema.Columns),
		)
	}

	buf := w.bufferFor(projectID)
	now := time.Now()

	buf.mu.Lock()
	if len(buf.rows) == 0 {
		buf.oldestAppend = now
		buf.minTime = ts
		buf.maxTime = ts
	} else {
		if ts.Before(buf.minTime) {
			buf.minTime = ts
		}
		if ts.After(buf.maxTime) {
			buf.maxTime = ts
		}
	}
	buf.rows = append(buf.rows, row)
	buf.approxBytes += rowBytes
	shouldFlush := (len(buf.rows) >= w.maxRows || buf.approxBytes >= w.maxBytes) && !now.Before(buf.retryAt)
	buf.mu.Unlock()

	w.metrics.appendedRows.Inc()
	w.metrics.appendedBytes.Add(float64(rowBytes))
	w.metrics.bufferedRows.Inc()
	w.metrics.bufferedBytes.Add(float64(rowBytes))

	if !shouldFlush {
		return nil, nil
	}

	segment, err := w.FlushProject(projectID)
	if err != nil && w.requeueFailed {
		// This row stays buffered with the rest for the next flush; failing
		// the append would only make the client resend, and duplicate, it.
		logger.GetLogger().Warn("segment flush failed, rows kept for the next flush",
			"stream", w.schema.Stream, "projectId", projectID, "error", err)
		return nil, nil
	}
	return segment, err
}

// FlushProject flushes the given project's buffered rows to a new Parquet
// segment file now, regardless of thresholds. Returns (nil, nil) if the
// buffer was empty.
func (w *Writer) FlushProject(projectID uuid.UUID) (*Segment, error) {
	buf := w.bufferFor(projectID)

	buf.mu.Lock()
	if len(buf.rows) == 0 {
		buf.mu.Unlock()
		return nil, nil
	}
	pending := pendingRows{
		rows:         buf.rows,
		rawBytes:     buf.approxBytes,
		minTime:      buf.minTime,
		maxTime:      buf.maxTime,
		oldestAppend: buf.oldestAppend,
	}
	buf.rows = nil
	buf.approxBytes = 0
	buf.mu.Unlock()

	w.metrics.bufferedRows.Sub(float64(len(pending.rows)))
	w.metrics.bufferedBytes.Sub(float64(pending.rawBytes))

	start := time.Now()
	segment, err := w.writeSegment(projectID, pending.rows, pending.rawBytes, pending.minTime, pending.maxTime)
	w.metrics.flushDuration.Observe(time.Since(start).Seconds())
	if err != nil {
		w.metrics.flushError.Inc()
		if w.requeueFailed {
			w.requeue(buf, pending)
		} else {
			// Without requeue the rows are gone from here: JetStream
			// redelivers the unacked batch, anything else loses them.
			w.metrics.lostRows.Add(float64(len(pending.rows)))
		}
		return nil, err
	}

	buf.mu.Lock()
	buf.retryAt = time.Time{}
	buf.mu.Unlock()

	w.metrics.flushOK.Inc()
	w.metrics.flushedRows.Add(float64(len(pending.rows)))
	w.metrics.flushSize.Observe(float64(segment.SizeBytes))
	w.metrics.flushLag.Observe(time.Since(pending.oldestAppend).Seconds())
	return segment, nil
}

// FlushStale flushes every project whose oldest buffered row is older than maxAge (the
// Writer's own when <= 0), so low-volume projects still flush on time.
func (w *Writer) FlushStale(maxAge time.Duration) ([]Segment, error) {
	if maxAge <= 0 {
		maxAge = w.maxAge
	}

	var stale []uuid.UUID
	cutoff := time.Now().Add(-maxAge)

	w.buffers.Range(func(key, value any) bool {
		projectID, ok := key.(uuid.UUID)
		if !ok {
			return true
		}
		buf, ok := value.(*projectBuffer)
		if !ok {
			return true
		}

		buf.mu.Lock()
		isStale := len(buf.rows) > 0 && buf.oldestAppend.Before(cutoff)
		buf.mu.Unlock()

		if isStale {
			stale = append(stale, projectID)
		}
		return true
	})

	segments := make([]Segment, 0, len(stale))
	var errs []error
	for _, projectID := range stale {
		segment, err := w.FlushProject(projectID)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to flush stale project %s: %w", projectID, err))
			continue
		}
		if segment != nil {
			segments = append(segments, *segment)
		}
	}

	return segments, errors.Join(errs...)
}

// FlushAll flushes every buffered project's rows now, e.g. during shutdown.
// One project's failure doesn't stop the others; the segments written are
// returned alongside the joined error.
func (w *Writer) FlushAll() ([]Segment, error) {
	var projectIDs []uuid.UUID
	w.buffers.Range(func(key, _ any) bool {
		if projectID, ok := key.(uuid.UUID); ok {
			projectIDs = append(projectIDs, projectID)
		}
		return true
	})

	segments := make([]Segment, 0, len(projectIDs))
	var errs []error
	for _, projectID := range projectIDs {
		segment, err := w.FlushProject(projectID)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to flush project %s: %w", projectID, err))
			continue
		}
		if segment != nil {
			segments = append(segments, *segment)
		}
	}

	return segments, errors.Join(errs...)
}

// CleanupStaleTempFiles removes leftover "*.parquet.tmp" files, the only trace a crash
// mid-write, mid-compaction or mid-download leaves. Call once at startup: it ignores age, so it
// must not run while this process (sole user of its data directory) writes segments.
func (w *Writer) CleanupStaleTempFiles() (removed int, err error) {
	walkErr := filepath.WalkDir(w.dataDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// A directory that doesn't exist yet (nothing ingested for
			// this stream yet) isn't a failure worth surfacing.
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".parquet.tmp") {
			return nil
		}
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			return fmt.Errorf("failed to remove stale temp file %s: %w", path, removeErr)
		}
		removed++
		return nil
	})
	if walkErr != nil {
		return removed, walkErr
	}
	return removed, nil
}

// Close flushes every buffered project and closes the underlying DuckDB
// handle, even when a flush fails. Safe to call once during shutdown.
func (w *Writer) Close() error {
	_, flushErr := w.FlushAll()
	if flushErr != nil {
		// Rows a failed flush kept in memory don't outlive the process.
		dropped := w.dropBuffered()
		logger.GetLogger().Error("final segment flush failed, unflushed rows are lost",
			"stream", w.schema.Stream, "droppedRows", dropped, "error", flushErr)
	}
	telemetry.UnregisterDuckDB(w.db)
	return errors.Join(flushErr, w.db.Close())
}

func newWriterMetrics(stream string) writerMetrics {
	return writerMetrics{
		appendedRows:  telemetry.WriterAppendedRows.WithLabelValues(stream),
		appendedBytes: telemetry.WriterAppendedBytes.WithLabelValues(stream),
		bufferedRows:  telemetry.WriterBufferedRows.WithLabelValues(stream),
		bufferedBytes: telemetry.WriterBufferedBytes.WithLabelValues(stream),
		flushOK:       telemetry.SegmentFlushes.WithLabelValues(stream, telemetry.ResultOK),
		flushError:    telemetry.SegmentFlushes.WithLabelValues(stream, telemetry.ResultError),
		flushedRows:   telemetry.SegmentFlushedRows.WithLabelValues(stream, telemetry.ResultOK),
		lostRows:      telemetry.SegmentFlushedRows.WithLabelValues(stream, telemetry.ResultError),
		flushDuration: telemetry.SegmentFlushDuration.WithLabelValues(stream),
		flushSize:     telemetry.SegmentFlushSize.WithLabelValues(stream),
		flushLag:      telemetry.SegmentFlushLag.WithLabelValues(stream),
	}
}

func (w *Writer) bufferFor(projectID uuid.UUID) *projectBuffer {
	value, _ := w.buffers.LoadOrStore(projectID, &projectBuffer{})
	buf, _ := value.(*projectBuffer)
	return buf
}

// requeue puts a failed flush's rows back in front of the rows appended
// since. Past the requeue limit the oldest failed rows are dropped.
func (w *Writer) requeue(buf *projectBuffer, failed pendingRows) {
	buf.mu.Lock()
	defer buf.mu.Unlock()

	keep := min(len(failed.rows), max(requeueLimit(w.maxRows)-len(buf.rows), 0))
	if perRow := failed.rawBytes / int64(len(failed.rows)); perRow > 0 {
		fit := max(requeueLimit(w.maxBytes)-buf.approxBytes, 0) / perRow
		keep = int(min(int64(keep), fit))
	}
	dropped := len(failed.rows) - keep
	keptBytes := failed.rawBytes
	if dropped > 0 {
		keptBytes = failed.rawBytes * int64(keep) / int64(len(failed.rows))
	}

	if keep > 0 {
		// After a drop the batch's time range may be wider than its kept
		// rows; that's safe, a segment's range only has to cover its rows.
		if len(buf.rows) == 0 {
			buf.minTime, buf.maxTime, buf.oldestAppend = failed.minTime, failed.maxTime, failed.oldestAppend
		} else {
			if failed.minTime.Before(buf.minTime) {
				buf.minTime = failed.minTime
			}
			if failed.maxTime.After(buf.maxTime) {
				buf.maxTime = failed.maxTime
			}
			if failed.oldestAppend.Before(buf.oldestAppend) {
				buf.oldestAppend = failed.oldestAppend
			}
		}
		buf.rows = slices.Concat(failed.rows[dropped:], buf.rows)
		buf.approxBytes += keptBytes
	}
	buf.retryAt = time.Now().Add(flushRetryBackoff)

	w.metrics.bufferedRows.Add(float64(keep))
	w.metrics.bufferedBytes.Add(float64(keptBytes))
	w.metrics.lostRows.Add(float64(dropped))
}

// dropBuffered empties every buffer and returns how many rows it held.
func (w *Writer) dropBuffered() int {
	dropped := 0
	w.buffers.Range(func(_, value any) bool {
		buf, ok := value.(*projectBuffer)
		if !ok {
			return true
		}
		buf.mu.Lock()
		rows, rawBytes := len(buf.rows), buf.approxBytes
		buf.rows = nil
		buf.approxBytes = 0
		buf.mu.Unlock()

		dropped += rows
		w.metrics.bufferedRows.Sub(float64(rows))
		w.metrics.bufferedBytes.Sub(float64(rawBytes))
		w.metrics.lostRows.Add(float64(rows))
		return true
	})
	return dropped
}

func requeueLimit[T int | int64](threshold T) T {
	if threshold > T(math.MaxInt)/requeueLimitFactor {
		return threshold
	}
	return threshold * requeueLimitFactor
}

// writeSegment loads rows into a fresh DuckDB table via the Appender API (far faster than
// INSERTs), exports it to Parquet and drops it. UUID table names keep concurrent flushes apart.
func (w *Writer) writeSegment(
	projectID uuid.UUID,
	rows [][]any,
	rawBytes int64,
	minTime, maxTime time.Time,
) (*Segment, error) {
	tempTable := "flush_" + strings.ReplaceAll(uuid.NewString(), "-", "")

	ddl := w.createTableDDL(tempTable)
	if _, err := w.db.Exec(ddl); err != nil {
		return nil, fmt.Errorf("failed to create temp table: %w", err)
	}
	defer func() {
		_, _ = w.db.Exec("DROP TABLE IF EXISTS " + tempTable)
	}()

	if err := w.appendRows(tempTable, rows); err != nil {
		return nil, err
	}

	stats, err := computeStats(w.db, w.schema.Stream, tempTable, nil)
	if err != nil {
		return nil, err
	}

	segmentDir := filepath.Join(w.dataDir, w.schema.Stream, projectID.String())
	if err := os.MkdirAll(segmentDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create project segment directory: %w", err)
	}

	segmentPath := filepath.Join(
		segmentDir,
		fmt.Sprintf("%d_%s.parquet", time.Now().UnixNano(), uuid.NewString()[:8]),
	)

	// COPY to a temp path, then rename: a crash mid-COPY leaves only a ".parquet.tmp"
	// (swept at startup), never a truncated file at a name the catalog may point to.
	tempPath := segmentPath + ".tmp"
	copySQL := copyToParquetSQL("SELECT * FROM "+tempTable, tempPath)
	if _, err := w.db.Exec(copySQL); err != nil {
		_ = os.Remove(tempPath)
		return nil, fmt.Errorf("failed to export segment to parquet: %w", err)
	}

	if err := os.Rename(tempPath, segmentPath); err != nil {
		_ = os.Remove(tempPath)
		return nil, fmt.Errorf("failed to finalize segment file: %w", err)
	}

	fileInfo, err := os.Stat(segmentPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat written segment: %w", err)
	}

	return &Segment{
		Path:         segmentPath,
		ProjectID:    projectID,
		Stream:       w.schema.Stream,
		RowCount:     len(rows),
		SizeBytes:    fileInfo.Size(),
		RawSizeBytes: rawBytes,
		MinTime:      minTime,
		MaxTime:      maxTime,
		CreatedAt:    time.Now().UTC(),
		Stats:        stats,
	}, nil
}

func (w *Writer) createTableDDL(tableName string) string {
	columnDefs := make([]string, len(w.schema.Columns))
	for i, col := range w.schema.Columns {
		columnDefs[i] = fmt.Sprintf("%s %s", col.Name, col.Type)
	}
	return fmt.Sprintf("CREATE TABLE %s (%s)", tableName, strings.Join(columnDefs, ", "))
}

func (w *Writer) appendRows(tableName string, rows [][]any) error {
	conn, err := w.db.Conn(context.Background())
	if err != nil {
		return fmt.Errorf("failed to get connection for appender: %w", err)
	}
	defer func() { _ = conn.Close() }()

	return conn.Raw(func(driverConn any) error {
		dconn, ok := driverConn.(driver.Conn)
		if !ok {
			return fmt.Errorf("unexpected driver connection type %T", driverConn)
		}

		appender, err := duckdb.NewAppenderFromConn(dconn, "", tableName)
		if err != nil {
			return fmt.Errorf("failed to create appender: %w", err)
		}

		for i, row := range rows {
			// AppendRow takes ...driver.Value, a distinct named type from
			// ...any, so each row needs an explicit element-wise conversion.
			values := make([]driver.Value, len(row))
			for j, v := range row {
				values[j] = v
			}

			if err := appender.AppendRow(values...); err != nil {
				_ = appender.Close()
				return fmt.Errorf("failed to append row %d: %w", i, err)
			}
		}

		if err := appender.Close(); err != nil {
			return fmt.Errorf("failed to close appender: %w", err)
		}
		return nil
	})
}
