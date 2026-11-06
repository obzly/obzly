// Package metrics_jetstream wires metrics into the JetStream ingest path.
package metrics_jetstream

import (
	"log/slog"
	"time"

	"obzly/internal/broker"
	metrics_core "obzly/internal/features/metrics/core"
	metrics_ingester "obzly/internal/features/metrics/ingester"
	"obzly/internal/ingest"

	"github.com/google/uuid"
)

// Writer is the metrics stream's durable writer (metrics_parquetstore.DurableWriter).
type Writer interface {
	ingest.Writer
	Append(entries map[uuid.UUID][]*metrics_core.MetricSample) error
}

// NewSink writes samples through writer, and each flush's series through
// seriesWriter (the metrics_series stream).
func NewSink(
	writer Writer,
	seriesWriter ingest.Writer,
	targetBytes int64,
	maxAge time.Duration,
	log *slog.Logger,
) *ingest.Sink {
	appendRows := func(projectID uuid.UUID, data []byte) error {
		items, err := ingest.DecodePayload[*metrics_core.MetricSample](data)
		if err != nil {
			return err
		}
		for _, item := range items {
			item.ProjectID = projectID
		}
		return writer.Append(map[uuid.UUID][]*metrics_core.MetricSample{projectID: items})
	}
	writers := map[string]ingest.Writer{
		metrics_ingester.MetricSchema.Stream: writer,
		metrics_ingester.StreamSeries:        seriesWriter,
	}
	return ingest.NewSink(broker.SignalMetrics, appendRows, writers, targetBytes, maxAge, log)
}
