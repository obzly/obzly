package spans_jetstream

import (
	"fmt"
	"log/slog"
	"math"

	"obzly/internal/broker"
	spans_core "obzly/internal/features/spans/core"
	spans_ingester "obzly/internal/features/spans/ingester"
	"obzly/internal/ingest"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
)

// NewTail serves the spans rows still pending in JetStream (see
// ingest.Tail), written under dir.
func NewTail(dir string, log *slog.Logger) (*ingest.Tail, error) {
	writer, err := spans_ingester.NewSpanIngester(dir,
		segments.WithMaxRows(math.MaxInt), segments.WithMaxBytes(math.MaxInt64))
	if err != nil {
		return nil, fmt.Errorf("failed to create tail writer: %w", err)
	}

	write := func(projectID uuid.UUID, payloads [][]byte) (map[string]*segments.Segment, error) {
		var items []*spans_core.Span
		for _, payload := range payloads {
			decoded, err := ingest.DecodePayload[*spans_core.Span](payload)
			if err != nil {
				continue
			}
			for _, item := range decoded {
				item.ProjectID = projectID
			}
			items = append(items, decoded...)
		}
		if len(items) == 0 {
			return map[string]*segments.Segment{}, nil
		}
		if err := writer.StoreSpansBatch(map[uuid.UUID][]*spans_core.Span{projectID: items}); err != nil {
			return nil, err
		}
		file, err := writer.WriteFile(projectID)
		if err != nil {
			return nil, err
		}
		return map[string]*segments.Segment{spans_ingester.SpanSchema.Stream: file}, nil
	}
	return ingest.NewTail(broker.SignalSpans, dir, write, log)
}
