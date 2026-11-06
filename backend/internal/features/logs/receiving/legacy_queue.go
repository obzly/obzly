package logs_receiving

import (
	"encoding/json"
	"fmt"
	"log/slog"

	logs_core "obzly/internal/features/logs/core"
	cache_utils "obzly/internal/util/cache"

	"github.com/google/uuid"
)

// Older releases queued logs in this Valkey list; a hard-killed one could
// leave entries behind.
// TODO: remove DrainLegacyQueue once no deployment can still hold entries.
const (
	legacyQueueKey   = "obzly:logs:queue"
	legacyDrainBatch = 5_000
)

// DrainLegacyQueue stores whatever an older release left in the Valkey logs
// queue, once, at startup. RPOP is atomic, so replicas draining together
// never store an entry twice. Returns how many logs it stored.
func DrainLegacyQueue(logger *slog.Logger) (int, error) {
	queue := cache_utils.NewValkeyQueueService()
	stored := 0
	for {
		serialized, err := queue.DequeueBatch(legacyQueueKey, legacyDrainBatch, 0)
		if err != nil {
			return stored, fmt.Errorf("failed to read the legacy logs queue: %w", err)
		}
		if len(serialized) == 0 {
			return stored, nil
		}

		batch := map[uuid.UUID][]*logs_core.LogItem{}
		for _, data := range serialized {
			var item logs_core.LogItem
			if err := json.Unmarshal(data, &item); err != nil {
				logger.Warn("Skipping undecodable legacy queue entry", slog.String("error", err.Error()))
				continue
			}
			batch[item.ProjectID] = append(batch[item.ProjectID], &item)
		}
		if err := storeBatch(batch); err != nil {
			// Put them back for the next start rather than lose them.
			if requeueErr := queue.EnqueueBatch(legacyQueueKey, serialized); requeueErr != nil {
				logger.Error("Failed to re-queue legacy logs - lost",
					slog.Int("logs", len(serialized)), slog.String("error", requeueErr.Error()))
			}
			return stored, fmt.Errorf("failed to store legacy queued logs: %w", err)
		}
		stored += len(serialized)
	}
}
