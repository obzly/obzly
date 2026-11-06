// Package spans_ingester is the spans stream's write path onto the Parquet
// storage engine; it mirrors logs_ingester.
package spans_ingester

import (
	"encoding/json"
	"errors"
	"fmt"

	spans_core "obzly/internal/features/spans/core"
	"obzly/internal/storage/segments"
	"obzly/internal/util/attrnames"

	"github.com/google/uuid"
)

// SpanSchema is the spans stream's Parquet segment schema. Column order
// here must match the value order produced by rowFor.
var SpanSchema = segments.Schema{
	Stream: "spans",
	Columns: []segments.ColumnDef{
		{Name: "id", Type: "VARCHAR"},
		{Name: "project_id", Type: "VARCHAR"},
		{Name: "trace_id", Type: "VARCHAR"},
		{Name: "span_id", Type: "VARCHAR"},
		{Name: "parent_span_id", Type: "VARCHAR"},
		{Name: "name", Type: "VARCHAR"},
		{Name: "service_name", Type: "VARCHAR"},
		{Name: "kind", Type: "VARCHAR"},
		// TIMESTAMP_NS: DuckDB's TIMESTAMP silently truncates to microseconds.
		{Name: "start_time", Type: "TIMESTAMP_NS"},
		{Name: "end_time", Type: "TIMESTAMP_NS"},
		{Name: "status_code", Type: "VARCHAR"},
		{Name: "status_message", Type: "VARCHAR"},
		// JSON text (see segments.ColumnDef for why not DuckDB's JSON type).
		// Display-only, so values are not stringified as logs' fields are.
		{Name: "attributes", Type: "VARCHAR"},
		{Name: "resource_attributes", Type: "VARCHAR"},
		{Name: "events", Type: "VARCHAR"},
	},
}

type SpanIngester struct {
	writer      *segments.Writer
	segmentRepo *segments.SegmentRepository
}

func NewSpanIngester(dataDir string, opts ...segments.Option) (*SpanIngester, error) {
	writer, err := segments.NewWriter(dataDir, SpanSchema, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create spans segment writer: %w", err)
	}

	return &SpanIngester{writer: writer, segmentRepo: &segments.SegmentRepository{}}, nil
}

// StoreSpansBatch buffers spans per project and flushes any project whose
// buffer crosses a threshold.
func (i *SpanIngester) StoreSpansBatch(entries map[uuid.UUID][]*spans_core.Span) error {
	for projectID, batchSpans := range entries {
		for _, span := range batchSpans {
			row, rowBytes, err := rowFor(projectID, span)
			if err != nil {
				return fmt.Errorf("failed to convert span %s: %w", span.SpanID, err)
			}

			segment, err := i.writer.Append(projectID, row, rowBytes, span.StartTime)
			if err != nil {
				return fmt.Errorf("failed to append span %s: %w", span.SpanID, err)
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

func (i *SpanIngester) FlushStale() ([]segments.Segment, error) {
	flushed, err := i.writer.FlushStale(0)
	// Segments written before a failed project still need their catalog rows.
	return flushed, errors.Join(err, i.recordSegments(flushed))
}

func (i *SpanIngester) FlushAll() ([]segments.Segment, error) {
	flushed, err := i.writer.FlushAll()
	// Segments written before a failed project still need their catalog rows.
	return flushed, errors.Join(err, i.recordSegments(flushed))
}

// FlushProject flushes one project's rows and records the segment; nil if
// nothing was buffered.
func (i *SpanIngester) FlushProject(projectID uuid.UUID) (*segments.SegmentRecord, error) {
	segment, err := i.writer.FlushProject(projectID)
	if err != nil || segment == nil {
		return nil, err
	}
	record := segments.FromSegment(*segment)
	if err := i.segmentRepo.Create(record); err != nil {
		return nil, fmt.Errorf("failed to record segment metadata for %s: %w", segment.Path, err)
	}
	return record, nil
}

// WriteFile writes one project's buffered rows to a Parquet file that is
// not recorded in the catalog (the query-time tail); nil if none.
func (i *SpanIngester) WriteFile(projectID uuid.UUID) (*segments.Segment, error) {
	return i.writer.FlushProject(projectID)
}

// CleanupStaleTempFiles removes "*.parquet.tmp" files left by a crash
// mid-flush; call once at startup, before ingestion resumes.
func (i *SpanIngester) CleanupStaleTempFiles() (int, error) {
	return i.writer.CleanupStaleTempFiles()
}

func (i *SpanIngester) recordSegments(flushed []segments.Segment) error {
	for _, segment := range flushed {
		if err := i.recordSegment(segment); err != nil {
			return err
		}
	}
	return nil
}

func (i *SpanIngester) recordSegment(segment segments.Segment) error {
	if err := i.segmentRepo.Create(segments.FromSegment(segment)); err != nil {
		return fmt.Errorf("failed to record segment metadata for %s: %w", segment.Path, err)
	}
	return nil
}

func (i *SpanIngester) Close() error {
	return i.writer.Close()
}

func rowFor(projectID uuid.UUID, span *spans_core.Span) ([]any, int64, error) {
	attributesJSON, err := marshalOrEmpty(attrnames.NormalizeMap(span.Attributes))
	if err != nil {
		return nil, 0, fmt.Errorf("failed to marshal attributes: %w", err)
	}

	resourceAttributesJSON, err := marshalOrEmpty(attrnames.NormalizeMap(span.ResourceAttributes))
	if err != nil {
		return nil, 0, fmt.Errorf("failed to marshal resource attributes: %w", err)
	}

	eventsJSON := "[]"
	if len(span.Events) > 0 {
		events := make([]spans_core.SpanEvent, len(span.Events))
		for i, event := range span.Events {
			event.Attributes = attrnames.NormalizeMap(event.Attributes)
			events[i] = event
		}
		encoded, err := json.Marshal(events)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to marshal events: %w", err)
		}
		eventsJSON = string(encoded)
	}

	row := []any{
		span.ID.String(),
		projectID.String(),
		span.TraceID,
		span.SpanID,
		span.ParentSpanID,
		span.Name,
		span.ServiceName,
		string(span.Kind),
		span.StartTime,
		span.EndTime,
		string(span.StatusCode),
		span.StatusMessage,
		attributesJSON,
		resourceAttributesJSON,
		eventsJSON,
	}

	rowBytes := int64(
		len(span.Name) + len(span.ServiceName) + len(attributesJSON) +
			len(resourceAttributesJSON) + len(eventsJSON) + 96,
	)

	return row, rowBytes, nil
}

func marshalOrEmpty(m map[string]any) (string, error) {
	if len(m) == 0 {
		return "{}", nil
	}
	encoded, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
