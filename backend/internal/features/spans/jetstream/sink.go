// Package spans_jetstream wires spans into the JetStream ingest path.
package spans_jetstream

import (
	"log/slog"
	"time"

	"obzly/internal/broker"
	spans_core "obzly/internal/features/spans/core"
	spans_ingester "obzly/internal/features/spans/ingester"
	"obzly/internal/ingest"

	"github.com/google/uuid"
)

// Writer is the spans stream's durable writer (spans_parquetstore.DurableWriter).
type Writer interface {
	ingest.Writer
	Append(entries map[uuid.UUID][]*spans_core.Span) error
}

// NewSink writes spans batches to writer.
func NewSink(writer Writer, targetBytes int64, maxAge time.Duration, log *slog.Logger) *ingest.Sink {
	appendRows := func(projectID uuid.UUID, data []byte) error {
		items, err := ingest.DecodePayload[*spans_core.Span](data)
		if err != nil {
			return err
		}
		for _, item := range items {
			item.ProjectID = projectID
		}
		return writer.Append(map[uuid.UUID][]*spans_core.Span{projectID: items})
	}
	writers := map[string]ingest.Writer{spans_ingester.SpanSchema.Stream: writer}
	return ingest.NewSink(broker.SignalSpans, appendRows, writers, targetBytes, maxAge, log)
}
