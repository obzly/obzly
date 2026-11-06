package segments

import (
	"context"
	"os"
	"time"

	"github.com/google/uuid"
)

// TailSource gives a query the project's rows not in any readable segment
// yet, as Parquet files: with JetStream ingest, the batches a worker hasn't
// written yet (ingest.Tail).
type TailSource interface {
	TailFiles(ctx context.Context, projectID uuid.UUID, stream string, newestReadable time.Time) []TailFile
}

// TailFile is one tail file and the time range of its rows.
type TailFile struct {
	Path             string
	MinTime, MaxTime time.Time
}

// AppendTail adds tail's files for [from, to] to files. newestReadable,
// passed to the tail, is when the newest planned segment became readable
// (created on this disk or without object storage, or uploaded): a tail
// built before then may hold rows that segment has too.
func AppendTail(
	ctx context.Context,
	tail TailSource,
	projectID uuid.UUID,
	stream string,
	records []*SegmentRecord,
	from, to time.Time,
	files []ResolvedFile,
) []ResolvedFile {
	if tail == nil {
		return files
	}
	var newestReadable time.Time
	for _, record := range records {
		readableAt := readableSince(record)
		if readableAt != nil && readableAt.After(newestReadable) {
			newestReadable = *readableAt
		}
	}
	for _, file := range tail.TailFiles(ctx, projectID, stream, newestReadable) {
		if !file.MaxTime.Before(from) && !file.MinTime.After(to) {
			files = append(files, ResolvedFile{Path: file.Path, MinTime: file.MinTime, MaxTime: file.MaxTime})
		}
	}
	return files
}

// readableSince is when record became readable on this replica: when it was
// uploaded, or when it was written if it's on this disk. A segment another
// replica wrote and hasn't uploaded isn't readable here; its batches are
// still pending in JetStream, so the tail must keep serving them.
func readableSince(record *SegmentRecord) *time.Time {
	if record.UploadedAt != nil {
		return record.UploadedAt
	}
	if _, err := os.Stat(record.Path); err == nil {
		return &record.CreatedAt
	}
	return nil
}
