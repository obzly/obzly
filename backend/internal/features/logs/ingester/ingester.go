// Package logs_ingester is the logs stream's write path onto the Parquet
// storage engine (see ARCHITECTURE.md).
package logs_ingester

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"runtime"
	"strconv"
	"strings"

	logs_core "obzly/internal/features/logs/core"
	projects_models "obzly/internal/features/projects/models"
	projects_services "obzly/internal/features/projects/services"
	"obzly/internal/storage/segments"
	"obzly/internal/util/attrnames"
	"obzly/internal/util/logger"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
)

// LogSchema is the logs stream's Parquet segment schema. Column order here
// must match the value order produced by rowFor.
var LogSchema = segments.Schema{
	Stream: logs_core.StreamLogs,
	Columns: []segments.ColumnDef{
		{Name: "id", Type: "VARCHAR"},
		{Name: "project_id", Type: "VARCHAR"},
		// TIMESTAMP_NS: DuckDB's plain TIMESTAMP is microsecond precision and
		// silently truncates Go's nanoseconds.
		{Name: "timestamp", Type: "TIMESTAMP_NS"},
		{Name: "level", Type: "VARCHAR"},
		{Name: "message", Type: "VARCHAR"},
		{Name: "client_ip", Type: "VARCHAR"},
		// Hex-encoded, empty string (not NULL) when absent. Older segments lack
		// these columns; union_by_name in ParquetFilesExpr reads them as NULL.
		{Name: "trace_id", Type: "VARCHAR"},
		{Name: "span_id", Type: "VARCHAR"},
		// Promoted OTel resource attributes (logs_core.PromotedResourceColumns): a
		// curated fast path for common filters. Also kept in the fields JSON, so
		// nothing reading fields.resource.* breaks; older segments read as NULL.
		{Name: "service_name", Type: "VARCHAR"},
		{Name: "service_version", Type: "VARCHAR"},
		{Name: "k8s_namespace_name", Type: "VARCHAR"},
		{Name: "k8s_pod_name", Type: "VARCHAR"},
		{Name: "k8s_container_name", Type: "VARCHAR"},
		{Name: "k8s_node_name", Type: "VARCHAR"},
		{Name: "k8s_cluster_name", Type: "VARCHAR"},
		{Name: "cloud_provider", Type: "VARCHAR"},
		{Name: "cloud_region", Type: "VARCHAR"},
		// JSON-encoded map[string]any, stored as text rather than DuckDB's
		// native JSON column type — see segments.ColumnDef's doc comment.
		{Name: "fields", Type: "VARCHAR"},
		// A project's promoted fields (projects_models.PromotedSlots) and a mask of
		// which were promoted at write time: queries read the column only where its
		// bit is set, else fields (always complete).
		{Name: "promoted_s1", Type: "VARCHAR"},
		{Name: "promoted_s2", Type: "VARCHAR"},
		{Name: "promoted_s3", Type: "VARCHAR"},
		{Name: "promoted_s4", Type: "VARCHAR"},
		{Name: "promoted_s5", Type: "VARCHAR"},
		{Name: "promoted_s6", Type: "VARCHAR"},
		{Name: "promoted_s7", Type: "VARCHAR"},
		{Name: "promoted_s8", Type: "VARCHAR"},
		{Name: "promoted_n1", Type: "DOUBLE"},
		{Name: "promoted_n2", Type: "DOUBLE"},
		{Name: "promoted_n3", Type: "DOUBLE"},
		{Name: "promoted_n4", Type: "DOUBLE"},
		{Name: "promoted_mask", Type: "BIGINT"},
		// Precomputed logs_core.PatternHash, so queries don't run the pattern regexes
		// per row. NULL in older segments; queries then compute it (where_builder).
		{Name: "pattern_hash", Type: "VARCHAR"},
	},
}

// LogRollupSchema is logs_1m's schema: per-minute log counts by level,
// service, namespace, container, pod and message pattern.
var LogRollupSchema = segments.Schema{
	Stream: logs_core.StreamLogsRollup,
	Columns: []segments.ColumnDef{
		{Name: "project_id", Type: "VARCHAR"},
		{Name: "timestamp", Type: "TIMESTAMP"},
		{Name: "level", Type: "VARCHAR"},
		{Name: "service_name", Type: "VARCHAR"},
		{Name: "k8s_namespace_name", Type: "VARCHAR"},
		{Name: "k8s_container_name", Type: "VARCHAR"},
		{Name: "k8s_pod_name", Type: "VARCHAR"},
		{Name: "pattern_hash", Type: "VARCHAR"},
		{Name: "cnt", Type: "BIGINT"},
	},
}

// LogPodRollupSchema is logs_1m_pods' schema: logs_1m without the pattern.
var LogPodRollupSchema = segments.Schema{
	Stream: logs_core.StreamLogsPodRollup,
	Columns: []segments.ColumnDef{
		{Name: "project_id", Type: "VARCHAR"},
		{Name: "timestamp", Type: "TIMESTAMP"},
		{Name: "level", Type: "VARCHAR"},
		{Name: "service_name", Type: "VARCHAR"},
		{Name: "k8s_namespace_name", Type: "VARCHAR"},
		{Name: "k8s_container_name", Type: "VARCHAR"},
		{Name: "k8s_pod_name", Type: "VARCHAR"},
		{Name: "cnt", Type: "BIGINT"},
	},
}

// LogPatternRollupSchema is logs_1h_patterns' schema: per-hour log counts
// (timestamp: the hour's start) by level, service and message pattern.
var LogPatternRollupSchema = segments.Schema{
	Stream: logs_core.StreamLogsPatternRollup,
	Columns: []segments.ColumnDef{
		{Name: "project_id", Type: "VARCHAR"},
		{Name: "timestamp", Type: "TIMESTAMP"},
		{Name: "level", Type: "VARCHAR"},
		{Name: "service_name", Type: "VARCHAR"},
		{Name: "pattern_hash", Type: "VARCHAR"},
		{Name: "cnt", Type: "BIGINT"},
	},
}

func init() {
	for _, stream := range []string{logs_core.StreamLogs, logs_core.StreamAILogs} {
		schema := LogSchema
		schema.Stream = stream
		segments.RegisterSchemaAnchor(schema)
	}
	segments.RegisterSchemaAnchor(LogRollupSchema)
	segments.RegisterSchemaAnchor(LogPodRollupSchema)
	segments.RegisterSchemaAnchor(LogPatternRollupSchema)
}

// rowsPerWorker: a batch converts on one more goroutine per this many logs.
const rowsPerWorker = 128

type builtRow struct {
	row    []any
	bytes  int64
	fields map[string]any
}

type LogIngester struct {
	writer      *segments.Writer
	segmentRepo *segments.SegmentRepository
	// sources: nil for streams other than logs.
	sources *sourceIndex
}

// NewLogIngester creates a logs ingester writing segments under
// dataDir/logs/<project_id>/ and recording each in stream_segments.
func NewLogIngester(dataDir string, opts ...segments.Option) (*LogIngester, error) {
	return NewLogIngesterForStream(dataDir, logs_core.StreamLogs, opts...)
}

// NewLogIngesterForStream writes the logs schema under another stream name.
func NewLogIngesterForStream(dataDir, stream string, opts ...segments.Option) (*LogIngester, error) {
	schema := LogSchema
	schema.Stream = stream
	writer, err := segments.NewWriter(dataDir, schema, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create logs segment writer: %w", err)
	}

	ingester := &LogIngester{writer: writer, segmentRepo: &segments.SegmentRepository{}}
	if stream == logs_core.StreamLogs {
		ingester.sources = newSourceIndex()
	}
	return ingester, nil
}

// StoreLogsBatch buffers log items by project and flushes any project over a
// threshold. Returns the first error; other projects' buffered rows are kept.
func (i *LogIngester) StoreLogsBatch(entries map[uuid.UUID][]*logs_core.LogItem) error {
	for projectID, logItems := range entries {
		rows, err := buildRows(projectID, logItems)
		if err != nil {
			return err
		}
		for k, logItem := range logItems {
			if i.sources != nil {
				i.sources.observe(projectID, rows[k].fields, logItem.Timestamp)
			}

			segment, err := i.writer.Append(projectID, rows[k].row, rows[k].bytes, logItem.Timestamp)
			if err != nil {
				return fmt.Errorf("failed to append log item %s: %w", logItem.ID, err)
			}
			if segment != nil {
				if err := i.recordSegment(*segment); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

// FlushStale flushes project buffers older than the writer's max age, so
// low-volume projects still flush on time, not just size.
func (i *LogIngester) FlushStale() ([]segments.Segment, error) {
	flushed, err := i.writer.FlushStale(0)
	// Segments written before a failed project still need their catalog rows.
	return flushed, errors.Join(err, i.recordSegments(flushed))
}

// FlushAll flushes every buffered project now, regardless of thresholds
// (shutdown, and tests needing deterministic segments).
func (i *LogIngester) FlushAll() ([]segments.Segment, error) {
	flushed, err := i.writer.FlushAll()
	// Segments written before a failed project still need their catalog rows.
	return flushed, errors.Join(err, i.recordSegments(flushed))
}

// FlushProject flushes one project's rows and records the segment; nil if
// nothing was buffered.
func (i *LogIngester) FlushProject(projectID uuid.UUID) (*segments.SegmentRecord, error) {
	segment, err := i.writer.FlushProject(projectID)
	if err != nil || segment == nil {
		return nil, err
	}
	record := segments.FromSegment(*segment)
	if err := i.segmentRepo.Create(record); err != nil {
		return nil, fmt.Errorf("failed to record segment metadata for %s: %w", segment.Path, err)
	}
	i.flushSources(projectID)
	return record, nil
}

// DisableSourceIndex stops recording log sources, for a writer whose rows
// another writer records (the query-time tail).
func (i *LogIngester) DisableSourceIndex() {
	i.sources = nil
}

// WriteFile writes one project's buffered rows to a Parquet file that is
// not recorded in the catalog (the query-time tail); nil if none.
func (i *LogIngester) WriteFile(projectID uuid.UUID) (*segments.Segment, error) {
	// The tail's rows are recorded by the writer that catalogs them.
	if i.sources != nil {
		i.sources.take(projectID)
	}
	return i.writer.FlushProject(projectID)
}

// CleanupStaleTempFiles removes "*.parquet.tmp" files left by a crash
// mid-flush. Call once at startup, before ingestion resumes.
func (i *LogIngester) CleanupStaleTempFiles() (int, error) {
	return i.writer.CleanupStaleTempFiles()
}

func (i *LogIngester) recordSegments(flushed []segments.Segment) error {
	for _, segment := range flushed {
		if err := i.recordSegment(segment); err != nil {
			return err
		}
	}
	return nil
}

func (i *LogIngester) recordSegment(segment segments.Segment) error {
	if err := i.segmentRepo.Create(segments.FromSegment(segment)); err != nil {
		return fmt.Errorf("failed to record segment metadata for %s: %w", segment.Path, err)
	}
	i.flushSources(segment.ProjectID)
	return nil
}

// flushSources writes a project's log sources once its rows are in a
// recorded segment. The index only serves the Logs sidebar: a failure must
// not fail the batch, which would then be redelivered and written again.
func (i *LogIngester) flushSources(projectID uuid.UUID) {
	if i.sources == nil {
		return
	}
	if err := i.sources.flush(projectID); err != nil {
		logger.GetLogger().Warn("failed to update the log source index", "projectId", projectID, "error", err)
	}
}

// Close flushes every buffered project and releases the underlying DuckDB
// handle. Call once during shutdown.
func (i *LogIngester) Close() error {
	return i.writer.Close()
}

// rowFor also returns the log's normalized fields.
// buildRows converts a batch's logs on up to GOMAXPROCS goroutines, in
// order: a row's message pattern (regexes) is most of its cost, and each
// ingest shard hands its batches over on a single goroutine, so one busy
// project was capped at one core.
func buildRows(projectID uuid.UUID, logItems []*logs_core.LogItem) ([]builtRow, error) {
	rows := make([]builtRow, len(logItems))
	convert := func(from, to int) error {
		for k := from; k < to; k++ {
			row, rowBytes, fields, err := rowFor(projectID, logItems[k])
			if err != nil {
				return fmt.Errorf("failed to convert log item %s: %w", logItems[k].ID, err)
			}
			rows[k] = builtRow{row: row, bytes: rowBytes, fields: fields}
		}
		return nil
	}

	workers := min(runtime.GOMAXPROCS(0), (len(logItems)+rowsPerWorker-1)/rowsPerWorker)
	if workers <= 1 {
		return rows, convert(0, len(logItems))
	}
	chunk := (len(logItems) + workers - 1) / workers
	var group errgroup.Group
	for from := 0; from < len(logItems); from += chunk {
		group.Go(func() error { return convert(from, min(from+chunk, len(logItems))) })
	}
	return rows, group.Wait()
}

func rowFor(projectID uuid.UUID, logItem *logs_core.LogItem) ([]any, int64, map[string]any, error) {
	// Every write path ends here, so current-version segments always hold
	// normalized names and lowercase levels (segments.CurrentSchemaVersion).
	fields := attrnames.NormalizeMap(logItem.Fields)
	fieldsJSON := "{}"
	if len(fields) > 0 {
		// Custom field values are always stored as strings, whatever their JSON
		// type, so equals/contains/in see one type per field. Direct callers (e.g.
		// tests) bypass the HTTP path, so it is enforced here.
		stringified := make(map[string]string, len(fields))
		for key, value := range fields {
			stringified[key] = logs_core.StringifyFieldValue(value)
		}

		encoded, err := json.Marshal(stringified)
		if err != nil {
			return nil, 0, nil, fmt.Errorf("failed to marshal fields: %w", err)
		}
		fieldsJSON = string(encoded)
	}

	promotedValues := make([]string, len(logs_core.PromotedResourceColumns))
	promotedBytes := 0
	for i, promoted := range logs_core.PromotedResourceColumns {
		promotedValues[i] = logs_core.PromotedColumnValue(fields, promoted.OtelKey)
		promotedBytes += len(promotedValues[i])
	}

	row := []any{
		logItem.ID.String(),
		projectID.String(),
		logItem.Timestamp,
		strings.ToLower(string(logItem.Level)),
		logItem.Message,
		logItem.ClientIP,
		logItem.TraceID,
		logItem.SpanID,
	}
	for _, value := range promotedValues {
		row = append(row, value)
	}
	row = append(row, fieldsJSON)
	promoted, mask := promotedFieldValues(fields, projects_services.GetPromotedFieldService().ActiveFields(projectID))
	row = append(row, promoted...)
	row = append(row, mask)
	row = append(row, logs_core.PatternHash(logItem.Message))

	// Rough size estimate for the byte-based flush threshold.
	rowBytes := int64(
		len(
			logItem.Message,
		) + len(
			logItem.ClientIP,
		) + len(
			logItem.TraceID,
		) + len(
			logItem.SpanID,
		) + len(
			fieldsJSON,
		) + promotedBytes + logs_core.PatternHashLength + 64,
	)

	return row, rowBytes, fields, nil
}

// promotedFieldValues are a row's promoted columns, in PromotedSlots order, and
// its promoted_mask. A promoted field the row lacks is a NULL with its bit
// set (absent, no need to look in fields); a number field whose value
// isn't a number leaves its bit unset, so queries read it from fields.
func promotedFieldValues(fields map[string]any, active []projects_models.PromotedField) ([]any, int64) {
	values := make([]any, len(projects_models.PromotedSlots))
	var mask int64
	bySlot := make(map[string]string, len(active))
	for _, field := range active {
		bySlot[field.Slot] = field.Field
	}
	for i, slot := range projects_models.PromotedSlots {
		field, ok := bySlot[slot]
		if !ok {
			continue
		}
		raw, present := fields[field]
		if projects_models.SlotType(slot) == projects_models.PromotedTypeNumber {
			if !present {
				mask |= projects_models.SlotBit(slot)
				continue
			}
			number, err := strconv.ParseFloat(logs_core.StringifyFieldValue(raw), 64)
			if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
				continue
			}
			values[i] = number
		} else if present {
			values[i] = logs_core.StringifyFieldValue(raw)
		}
		mask |= projects_models.SlotBit(slot)
	}
	return values, mask
}
