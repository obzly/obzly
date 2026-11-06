package segments

import (
	"time"

	"github.com/google/uuid"
)

// SegmentRecord is the Postgres catalog row for a Segment: what queries,
// the uploader and the compactor use to find segment files.
type SegmentRecord struct {
	ID           uuid.UUID `json:"id"           gorm:"column:id"`
	ProjectID    uuid.UUID `json:"projectId"    gorm:"column:project_id"`
	Stream       string    `json:"stream"       gorm:"column:stream"`
	Path         string    `json:"path"         gorm:"column:path"`
	RowCount     int64     `json:"rowCount"     gorm:"column:row_count"`
	SizeBytes    int64     `json:"sizeBytes"    gorm:"column:size_bytes"`
	RawSizeBytes int64     `json:"rawSizeBytes" gorm:"column:raw_size_bytes"`

	MinTime   time.Time `json:"minTime"   gorm:"column:min_time"`
	MaxTime   time.Time `json:"maxTime"   gorm:"column:max_time"`
	CreatedAt time.Time `json:"createdAt" gorm:"column:created_at"`

	// Set once uploaded to object storage; nil while only on local disk.
	UploadedAt *time.Time `json:"uploadedAt,omitempty" gorm:"column:uploaded_at"`
	ObjectKey  string     `json:"objectKey,omitempty"  gorm:"column:object_key"`

	// SchemaVersion: SchemaVersionLegacy or CurrentSchemaVersion.
	SchemaVersion int `json:"schemaVersion" gorm:"column:schema_version"`

	// Level is the compaction level (see Segment.Level).
	Level int `json:"level" gorm:"column:level"`

	// WriterID is the process whose local disk holds the file until upload
	// (see SetLocalWriter); nil for rows written before writer_id existed.
	WriterID *string `json:"writerId,omitempty" gorm:"column:writer_id"`

	// Stats: what the file contains, for skipping it (SegmentStats); nil for
	// rows written before stats existed.
	Stats *SegmentStats `json:"stats,omitempty" gorm:"column:stats"`

	// CompactionAttempts: merges of this segment started and not completed
	// (see RecordCompactionAttempt); a merged-away segment's row is gone.
	CompactionAttempts int `json:"compactionAttempts" gorm:"column:compaction_attempts"`
}

// IsLegacy reports whether the file predates normalized names.
func (r *SegmentRecord) IsLegacy() bool {
	return r.SchemaVersion == SchemaVersionLegacy
}

func (SegmentRecord) TableName() string {
	return "stream_segments"
}

// FromSegment builds the metadata row for a just-flushed Segment.
func FromSegment(segment Segment) *SegmentRecord {
	return &SegmentRecord{
		ID:           uuid.New(),
		ProjectID:    segment.ProjectID,
		Stream:       segment.Stream,
		Path:         segment.Path,
		RowCount:     int64(segment.RowCount),
		SizeBytes:    segment.SizeBytes,
		RawSizeBytes: segment.RawSizeBytes,
		MinTime:      segment.MinTime,
		MaxTime:      segment.MaxTime,
		CreatedAt:    segment.CreatedAt,
		SchemaVersion: func() int {
			if segment.SchemaVersion == 0 {
				return CurrentSchemaVersion
			}
			return segment.SchemaVersion
		}(),
		Stats: segment.Stats,
		Level: segment.Level,
	}
}
