package logs_parquetstore

import (
	"context"
	"fmt"
	"math"

	logs_core "obzly/internal/features/logs/core"
	logs_ingester "obzly/internal/features/logs/ingester"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
)

// DurableWriter is the JetStream path's writer: nothing flushes on its own,
// so the caller knows which messages each segment covers and acks them only
// after Upload.
type DurableWriter struct {
	ingester    *logs_ingester.LogIngester
	store       *LogParquetStore
	segmentRepo *segments.SegmentRepository
}

// NewDurableWriter returns a writer onto this store's stream with its own
// buffers, outside the periodic ForceFlush.
func (s *LogParquetStore) NewDurableWriter() (*DurableWriter, error) {
	ingester, err := logs_ingester.NewLogIngesterForStream(s.dataDir, s.stream,
		segments.WithMaxRows(math.MaxInt),
		segments.WithMaxBytes(math.MaxInt64),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create durable log writer: %w", err)
	}
	return &DurableWriter{ingester: ingester, store: s, segmentRepo: s.segmentRepo}, nil
}

// Append buffers rows in RAM.
func (w *DurableWriter) Append(entries map[uuid.UUID][]*logs_core.LogItem) error {
	return w.ingester.StoreLogsBatch(entries)
}

// Flush writes one project's rows to a catalogued segment; nil if none.
func (w *DurableWriter) Flush(projectID uuid.UUID) (*segments.SegmentRecord, error) {
	return w.ingester.FlushProject(projectID)
}

// Upload makes a flushed segment durable in object storage. Without object
// storage the local file is all there is, so it is done already. A segment
// already merged or deleted counts as done: its rows are in the merged,
// uploaded file, or meant to be gone.
func (w *DurableWriter) Upload(ctx context.Context, record *segments.SegmentRecord) error {
	if w.store.uploader == nil {
		return nil
	}
	err := w.store.uploader.Upload(ctx, record)
	if err == nil {
		return nil
	}
	exists, existsErr := w.segmentRepo.Exists(record.ID)
	if existsErr == nil && !exists {
		return nil
	}
	return err
}
