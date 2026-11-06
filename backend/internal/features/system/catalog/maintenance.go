// Package system_catalog maintains the partitioned segment catalog: monthly partitions
// are created ahead of need, and past months are dropped once retention empties them.
package system_catalog

import (
	"context"
	"log/slog"
	"time"

	"obzly/internal/cache"
	"obzly/internal/storage/segments"
	"obzly/internal/util/leaderlock"
)

// maintenanceInterval: partitions are created three months ahead, so a few
// passes a day leave ample room for a missed one.
const maintenanceInterval = 6 * time.Hour

const lockName = "catalog-partitions"

// Start runs one pass now and then every maintenanceInterval until ctx ends.
// Leader-locked: one replica at a time does the work.
func Start(ctx context.Context, log *slog.Logger) {
	repo := &segments.SegmentRepository{}
	go func() {
		ticker := time.NewTicker(maintenanceInterval)
		defer ticker.Stop()
		for {
			_, err := leaderlock.RunExclusive(ctx, cache.GetCache(), lockName, 10*time.Minute,
				func(context.Context) error { return maintain(repo, log) })
			if err != nil {
				log.Error("catalog partition maintenance failed", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func maintain(repo *segments.SegmentRepository, log *slog.Logger) error {
	now := time.Now().UTC()
	if err := repo.EnsurePartitions(now); err != nil {
		return err
	}
	dropped, err := repo.DropEmptyPastPartitions(now)
	if len(dropped) > 0 {
		log.Info("dropped empty catalog partitions", "partitions", dropped)
	}
	return err
}
