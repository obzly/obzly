package metrics_jetstream

import (
	"fmt"
	"log/slog"
	"math"

	"obzly/internal/broker"
	metrics_core "obzly/internal/features/metrics/core"
	metrics_ingester "obzly/internal/features/metrics/ingester"
	"obzly/internal/ingest"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
)

// NewTail serves the metrics rows still pending in JetStream (see
// ingest.Tail), written under dir.
func NewTail(dir string, log *slog.Logger) (*ingest.Tail, error) {
	writer, err := metrics_ingester.NewSampleOnlyIngester(dir,
		segments.WithMaxRows(math.MaxInt), segments.WithMaxBytes(math.MaxInt64))
	if err != nil {
		return nil, fmt.Errorf("failed to create tail writer: %w", err)
	}

	write := func(projectID uuid.UUID, payloads [][]byte) (map[string]*segments.Segment, error) {
		var items []*metrics_core.MetricSample
		for _, payload := range payloads {
			decoded, err := ingest.DecodePayload[*metrics_core.MetricSample](payload)
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
		if err := writer.StoreSamplesBatch(map[uuid.UUID][]*metrics_core.MetricSample{projectID: items}); err != nil {
			return nil, err
		}
		file, err := writer.WriteFile(projectID)
		if err != nil {
			return nil, err
		}
		return map[string]*segments.Segment{metrics_ingester.MetricSchema.Stream: file}, nil
	}
	return ingest.NewTail(broker.SignalMetrics, dir, write, log)
}
