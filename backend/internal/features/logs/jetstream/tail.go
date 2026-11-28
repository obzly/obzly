package logs_jetstream

import (
	"log/slog"
	"math"

	"obzly/internal/broker"
	logs_core "obzly/internal/features/logs/core"
	logs_ingester "obzly/internal/features/logs/ingester"
	"obzly/internal/ingest"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
)

// NewTail serves the logs and AI logs rows still pending in JetStream (see
// ingest.Tail), written under dir.
func NewTail(dir string, log *slog.Logger) (*ingest.Tail, error) {
	newWriter := func() (ingest.DeltaWriter, error) { return newDeltaWriter(dir) }
	return ingest.NewTail(broker.SignalLogs, dir, newWriter, log)
}

// newDeltaWriter is one of the tail's writers: its own logs and AI logs
// segment writers, as it buffers one project's rows at a time.
func newDeltaWriter(dir string) (ingest.DeltaWriter, error) {
	writers := map[string]*logs_ingester.LogIngester{}
	for _, stream := range []string{logs_core.StreamLogs, logs_core.StreamAILogs} {
		writer, err := logs_ingester.NewLogIngesterForStream(dir, stream,
			segments.WithMaxRows(math.MaxInt), segments.WithMaxBytes(math.MaxInt64))
		if err != nil {
			return nil, err
		}
		// The durable writer records these rows' sources; the tail is rebuilt
		// per query and would only add work to it.
		writer.DisableSourceIndex()
		writers[stream] = writer
	}

	write := func(projectID uuid.UUID, payloads [][]byte) (map[string]*segments.Segment, error) {
		var items []*logs_core.LogItem
		for _, payload := range payloads {
			decoded, err := ingest.DecodePayload[*logs_core.LogItem](payload)
			if err != nil {
				continue
			}
			for _, item := range decoded {
				item.ProjectID = projectID
			}
			items = append(items, decoded...)
		}

		files := map[string]*segments.Segment{}
		logsBatch, aiBatch := logs_core.SplitAIEvents(map[uuid.UUID][]*logs_core.LogItem{projectID: items})
		for stream, batch := range map[string]map[uuid.UUID][]*logs_core.LogItem{
			logs_core.StreamLogs:   logsBatch,
			logs_core.StreamAILogs: aiBatch,
		} {
			if len(batch[projectID]) == 0 {
				continue
			}
			if err := writers[stream].StoreLogsBatch(batch); err != nil {
				return nil, err
			}
			file, err := writers[stream].WriteFile(projectID)
			if err != nil {
				return nil, err
			}
			files[stream] = file
		}
		return files, nil
	}
	return write, nil
}
