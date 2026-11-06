package logs_compactor

import (
	"context"
	"fmt"
	"os"
	"sort"
	"time"

	logs_core "obzly/internal/features/logs/core"
	"obzly/internal/storage/segments"
	"obzly/internal/util/logger"

	"github.com/google/uuid"
)

// Per-minute log counts (logs_1m): each compacted (L1 or cold) segment gets a
// rollup that the Logs histogram reads instead of its raw rows. A merge gets
// its own rollup and the sources' become orphans to delete. L0 is read raw.
const (
	// rollupBudget / rollupTimeBudget: segments rolled up per project per pass,
	// and when a pass stops starting new ones. Backfilling cold segments means
	// downloads that take minutes; the time budget keeps compaction moving.
	rollupBudget     = 10
	rollupTimeBudget = time.Minute
	rollupSelect     = `project_id, time_bucket(INTERVAL '1 minute', timestamp::TIMESTAMP) AS timestamp,
		level, service_name, k8s_namespace_name, k8s_container_name, k8s_pod_name, `
	rollupGroupBy = "1, 2, 3, 4, 5, 6, 7, 8 ORDER BY 2"
)

var rollupSelectList = rollupSelect + logs_core.StoredPatternHashSQL + " AS pattern_hash, count(*) AS cnt"

// RollupSegments deletes orphaned rollups and builds the missing rollups
// of up to rollupBudget compacted segments, newest first, starting none
// after rollupTimeBudget. Run it after the pass's merges are uploaded: a
// merge is invisible to queries until then.
func (c *LogCompactor) RollupSegments(projectID uuid.UUID) error {
	if c.stream != logs_core.StreamLogs {
		return nil
	}
	records, err := c.segmentRepo.ListByProject(projectID, c.stream)
	if err != nil {
		return fmt.Errorf("failed to list segments: %w", err)
	}
	rollups, err := c.segmentRepo.ListByProject(projectID, logs_core.StreamLogsRollup)
	if err != nil {
		return fmt.Errorf("failed to list log rollups: %w", err)
	}

	present := make(map[string]bool, len(records))
	for _, record := range records {
		present[record.ID.String()] = true
	}
	// Newest rollup first, so a source's duplicate (two passes racing) is
	// the one deleted.
	sort.Slice(rollups, func(i, j int) bool { return rollups[i].CreatedAt.After(rollups[j].CreatedAt) })
	built := map[string]bool{}
	var orphans []*segments.SegmentRecord
	for _, rollup := range rollups {
		source := ""
		if rollup.Stats != nil {
			source = rollup.Stats.Source
		}
		if !present[source] || built[source] {
			orphans = append(orphans, rollup)
			continue
		}
		built[source] = true
	}
	if len(orphans) > 0 {
		if err := c.deleteSegments(orphans); err != nil {
			return fmt.Errorf("failed to delete orphaned log rollups: %w", err)
		}
	}

	sort.Slice(records, func(i, j int) bool { return records[i].MaxTime.After(records[j].MaxTime) })
	started := time.Now()
	done := 0
	for _, record := range records {
		if done >= rollupBudget || (done > 0 && time.Since(started) > rollupTimeBudget) {
			break
		}
		if record.Level < segments.LevelCompacted || built[record.ID.String()] {
			continue
		}
		done++
		if err := c.rollUp(record); err != nil {
			logger.GetLogger().Warn("Failed to build a log segment's per-minute counts - the histogram reads it raw",
				"segmentId", record.ID, "error", err)
		}
	}
	return nil
}

// rollUp builds and records one segment's rollup. A file downloaded for it
// is removed afterwards: workers keep only what they wrote or merged.
func (c *LogCompactor) rollUp(record *segments.SegmentRecord) error {
	_, statErr := os.Stat(record.Path)
	ready, err := segments.EnsureLocalFiles(context.Background(), c.objectStore, []*segments.SegmentRecord{record})
	if err != nil {
		return err
	}
	if !ready {
		return nil // another replica's segment, not uploaded yet: a later pass
	}
	if statErr != nil {
		defer func() { _ = os.Remove(record.Path) }()
	}

	rollup, err := c.compactor.BuildGroupedRollup(record, c.stream, logs_core.StreamLogsRollup,
		rollupSelectList, rollupGroupBy, c.dataDir)
	if err != nil || rollup == nil {
		return err
	}
	if err := c.segmentRepo.Create(segments.FromSegment(*rollup)); err != nil {
		_ = os.Remove(rollup.Path)
		return fmt.Errorf("failed to record log rollup: %w", err)
	}
	return nil
}
