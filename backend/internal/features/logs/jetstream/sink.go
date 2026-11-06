// Package logs_jetstream wires logs into the JetStream ingest path: AI tool
// events go to their own stream, everything else to logs.
package logs_jetstream

import (
	"log/slog"
	"time"

	"obzly/internal/broker"
	logs_core "obzly/internal/features/logs/core"
	"obzly/internal/ingest"

	"github.com/google/uuid"
)

// Writer is one logs stream's durable writer (logs_parquetstore.DurableWriter).
type Writer interface {
	ingest.Writer
	Append(entries map[uuid.UUID][]*logs_core.LogItem) error
}

// NewSink writes logs to logsWriter and AI tool events to aiWriter.
func NewSink(logsWriter, aiWriter Writer, targetBytes int64, maxAge time.Duration, log *slog.Logger) *ingest.Sink {
	appendRows := func(projectID uuid.UUID, data []byte) error {
		items, err := ingest.DecodePayload[*logs_core.LogItem](data)
		if err != nil {
			return err
		}
		for _, item := range items {
			item.ProjectID = projectID
		}

		logsBatch, aiBatch := logs_core.SplitAIEvents(map[uuid.UUID][]*logs_core.LogItem{projectID: items})
		if len(logsBatch) > 0 {
			if err := logsWriter.Append(logsBatch); err != nil {
				return err
			}
		}
		if len(aiBatch) > 0 {
			return aiWriter.Append(aiBatch)
		}
		return nil
	}

	writers := map[string]ingest.Writer{
		logs_core.StreamLogs:   logsWriter,
		logs_core.StreamAILogs: aiWriter,
	}
	return ingest.NewSink(broker.SignalLogs, appendRows, writers, targetBytes, maxAge, log)
}
