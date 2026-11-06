package spans_parquetstore

import (
	"context"
	"fmt"
	"math"

	spans_core "obzly/internal/features/spans/core"
	spans_ingester "obzly/internal/features/spans/ingester"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
)

// DurableWriter is the JetStream path's writer: nothing
// flushes on its own, so the caller knows which messages each segment
// covers and acks them only after Upload.
type DurableWriter struct {
	ingester    *spans_ingester.SpanIngester
	store       *SpanParquetStore
	segmentRepo *segments.SegmentRepository
}

// NewDurableWriter returns a writer onto this store's stream with its own
// buffers, outside the periodic ForceFlush.
func (s *SpanParquetStore) NewDurableWriter() (*DurableWriter, error) {
	ingester, err := spans_ingester.NewSpanIngester(s.dataDir,
		segments.WithMaxRows(math.MaxInt),
		segments.WithMaxBytes(math.MaxInt64),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create durable spans writer: %w", err)
	}
	return &DurableWriter{ingester: ingester, store: s, segmentRepo: s.segmentRepo}, nil
}

// Append buffers rows in RAM.
func (w *DurableWriter) Append(entries map[uuid.UUID][]*spans_core.Span) error {
	return w.ingester.StoreSpansBatch(entries)
}

// Flush writes one project's rows to a catalogued segment; nil if none.
func (w *DurableWriter) Flush(projectID uuid.UUID) (*segments.SegmentRecord, error) {
	return w.ingester.FlushProject(projectID)
}

// Upload makes a flushed segment durable in object storage.
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
