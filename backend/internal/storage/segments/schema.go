// Package segments implements the storage engine's hot tier (ARCHITECTURE.md):
// per-project buffers flushed to local Parquet segments on size/time
// thresholds. A stream is just a name plus a column schema, so every signal
// shares the same machinery.
package segments

import (
	"time"

	"github.com/google/uuid"
)

// ColumnDef describes one column of a stream's Parquet schema, using DuckDB
// type names. IDs and JSON are stored as VARCHAR, not UUID/JSON types, so
// callers pass plain strings and avoid driver-specific types.
type ColumnDef struct {
	Name string
	Type string
}

// Schema is one stream's row shape: a name plus its ordered columns. Rows
// appended to a Writer must follow this column order.
type Schema struct {
	Stream  string
	Columns []ColumnDef
}

// Compaction levels (stream_segments.level).
const (
	LevelFlush     = 0
	LevelCompacted = 1
)

type Segment struct {
	Path      string
	ProjectID uuid.UUID
	Stream    string
	RowCount  int
	SizeBytes int64
	// RawSizeBytes is the approximate uncompressed size as received (the flush
	// accounting); SizeBytes is the Parquet file's size.
	RawSizeBytes int64
	MinTime      time.Time
	MaxTime      time.Time
	CreatedAt    time.Time
	// SchemaVersion: 0 means CurrentSchemaVersion (new data).
	SchemaVersion int
	// Stats: nil when the stream records none (see SegmentStats).
	Stats *SegmentStats
	// Level: LevelFlush for a writer's flush, LevelCompacted once merged.
	Level int
}
