package metrics_parquetstore

import (
	"context"
	"fmt"
	"math"

	metrics_core "obzly/internal/features/metrics/core"
	metrics_ingester "obzly/internal/features/metrics/ingester"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
)

// DurableWriter is the JetStream path's writer: nothing
// flushes on its own, so the caller knows which messages each segment
// covers and acks them only after Upload.
type DurableWriter struct {
	ingester    *metrics_ingester.MetricIngester
	store       *MetricParquetStore
	segmentRepo *segments.SegmentRepository
}

// NewDurableWriter returns a writer onto this store's stream with its own
// buffers, outside the periodic ForceFlush.
func (s *MetricParquetStore) NewDurableWriter() (*DurableWriter, error) {
	ingester, err := metrics_ingester.NewMetricIngester(s.dataDir,
		segments.WithMaxRows(math.MaxInt),
		segments.WithMaxBytes(math.MaxInt64),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create durable metrics writer: %w", err)
	}
	return &DurableWriter{ingester: ingester, store: s, segmentRepo: s.segmentRepo}, nil
}

// Append buffers rows in RAM.
func (w *DurableWriter) Append(entries map[uuid.UUID][]*metrics_core.MetricSample) error {
	return w.ingester.StoreSamplesBatch(entries)
}

// Flush writes one project's rows to a catalogued segment; nil if none.
func (w *DurableWriter) Flush(projectID uuid.UUID) (*segments.SegmentRecord, error) {
	return w.ingester.FlushProject(projectID)
}

// Upload makes a flushed segment durable in object storage. A segment
// deleted meanwhile (e.g. merged away) counts as done.
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

// Series is the writer for the series the same Appends used (the
// metrics_series stream): the sink flushes and uploads it beside the
// samples, so its rows are covered by the same acknowledgement.
func (w *DurableWriter) Series() *SeriesDurableWriter {
	return &SeriesDurableWriter{samples: w}
}

// SeriesDurableWriter flushes the series of a DurableWriter's samples.
type SeriesDurableWriter struct {
	samples *DurableWriter
}

func (w *SeriesDurableWriter) Flush(projectID uuid.UUID) (*segments.SegmentRecord, error) {
	return w.samples.ingester.FlushSeries(projectID)
}

func (w *SeriesDurableWriter) Upload(ctx context.Context, record *segments.SegmentRecord) error {
	return w.samples.Upload(ctx, record)
}
