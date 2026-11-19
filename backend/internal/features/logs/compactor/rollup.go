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
//
// logs_1m keeps the message pattern, whose cardinality can leave it only a
// few times smaller than the raw logs, so two smaller tiers are derived from
// each logs_1m file (not the raw segment, which is ~20x larger): logs_1m_pods
// (no pattern) and logs_1h_patterns (per hour, by level, service and
// pattern). They name the raw segment as their source, like logs_1m.
const (
	// rollupBudget / rollupTimeBudget: segments rolled up per project per pass,
	// and when a pass stops starting new ones. Backfilling cold segments means
	// downloads that take minutes; the time budget keeps compaction moving.
	rollupBudget     = 10
	rollupTimeBudget = time.Minute
	// derivedBudget / derivedTimeBudget: segments given their derived tiers
	// per project per pass, after logs_1m's own budget is spent. A logs_1m
	// file is small, so a backfill of weeks of segments takes a few passes.
	derivedBudget     = 50
	derivedTimeBudget = 30 * time.Second
	rollupSelect      = `project_id, time_bucket(INTERVAL '1 minute', timestamp::TIMESTAMP) AS timestamp,
		level, service_name, k8s_namespace_name, k8s_container_name, k8s_pod_name, `
	rollupGroupBy = "1, 2, 3, 4, 5, 6, 7, 8 ORDER BY 2"
)

var rollupSelectList = rollupSelect + logs_core.StoredPatternHashSQL + " AS pattern_hash, count(*) AS cnt"

// derivedTier is a rollup built from a segment's logs_1m rollup.
type derivedTier struct {
	stream, selectList, groupBy string
}

var derivedTiers = []derivedTier{
	{
		stream: logs_core.StreamLogsPodRollup,
		selectList: `project_id, timestamp, level, service_name, k8s_namespace_name, k8s_container_name,
			k8s_pod_name, sum(cnt)::BIGINT AS cnt`,
		groupBy: "1, 2, 3, 4, 5, 6, 7 ORDER BY 2",
	},
	{
		stream: logs_core.StreamLogsPatternRollup,
		selectList: `project_id, time_bucket(INTERVAL '1 hour', timestamp::TIMESTAMP) AS timestamp,
			level, service_name, pattern_hash, sum(cnt)::BIGINT AS cnt`,
		groupBy: "1, 2, 3, 4, 5 ORDER BY 2",
	},
}

// RollupSegments deletes orphaned rollups, builds the missing logs_1m
// rollups of up to rollupBudget compacted segments, newest first, starting
// none after rollupTimeBudget, then the missing derived tiers of up to
// derivedBudget segments within derivedTimeBudget. Run it after the pass's
// merges are uploaded: a merge is invisible to queries until then.
func (c *LogCompactor) RollupSegments(projectID uuid.UUID) error {
	if c.stream != logs_core.StreamLogs {
		return nil
	}
	records, err := c.segmentRepo.ListByProject(projectID, c.stream)
	if err != nil {
		return fmt.Errorf("failed to list segments: %w", err)
	}
	present := make(map[string]bool, len(records))
	for _, record := range records {
		present[record.ID.String()] = true
	}

	rollups, err := c.segmentRepo.ListByProject(projectID, logs_core.StreamLogsRollup)
	if err != nil {
		return fmt.Errorf("failed to list log rollups: %w", err)
	}
	built, orphans := rollupsBySource(rollups, present)
	derived := make(map[string]map[string]*segments.SegmentRecord, len(derivedTiers))
	for _, tier := range derivedTiers {
		tierRollups, err := c.segmentRepo.ListByProject(projectID, tier.stream)
		if err != nil {
			return fmt.Errorf("failed to list %s rollups: %w", tier.stream, err)
		}
		bySource, tierOrphans := rollupsBySource(tierRollups, present)
		derived[tier.stream] = bySource
		orphans = append(orphans, tierOrphans...)
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
		if record.Level < segments.LevelCompacted || built[record.ID.String()] != nil {
			continue
		}
		done++
		rollup, err := c.rollUp(record)
		if err != nil {
			logger.GetLogger().Warn("Failed to build a log segment's per-minute counts - the histogram reads it raw",
				"segmentId", record.ID, "error", err)
		}
		if rollup != nil {
			built[record.ID.String()] = rollup
		}
	}

	started = time.Now()
	done = 0
	for _, record := range records {
		if done >= derivedBudget || (done > 0 && time.Since(started) > derivedTimeBudget) {
			break
		}
		rollup := built[record.ID.String()]
		if rollup == nil {
			continue
		}
		var missing []derivedTier
		for _, tier := range derivedTiers {
			if derived[tier.stream][record.ID.String()] == nil {
				missing = append(missing, tier)
			}
		}
		if len(missing) == 0 {
			continue
		}
		done++
		if err := c.derive(rollup, missing); err != nil {
			logger.GetLogger().Warn("Failed to derive a log segment's smaller counts - queries read its logs_1m",
				"segmentId", record.ID, "error", err)
		}
	}
	return nil
}

// rollUp builds and records one segment's rollup (nil when it has no rows
// or isn't downloadable yet). A file downloaded for it is removed
// afterwards: workers keep only what they wrote or merged.
func (c *LogCompactor) rollUp(record *segments.SegmentRecord) (*segments.SegmentRecord, error) {
	_, statErr := os.Stat(record.Path)
	ready, err := segments.EnsureLocalFiles(context.Background(), c.objectStore, []*segments.SegmentRecord{record})
	if err != nil {
		return nil, err
	}
	if !ready {
		return nil, nil // another replica's segment, not uploaded yet: a later pass
	}
	if statErr != nil {
		defer func() { _ = os.Remove(record.Path) }()
	}

	rollup, err := c.compactor.BuildGroupedRollup(record, c.stream, logs_core.StreamLogsRollup,
		rollupSelectList, rollupGroupBy, c.dataDir)
	if err != nil || rollup == nil {
		return nil, err
	}
	created := segments.FromSegment(*rollup)
	if err := c.segmentRepo.Create(created); err != nil {
		_ = os.Remove(rollup.Path)
		return nil, fmt.Errorf("failed to record log rollup: %w", err)
	}
	return created, nil
}

// derive builds and records tiers from one logs_1m rollup, downloading it
// (and removing the download afterwards) when it isn't local.
func (c *LogCompactor) derive(rollup *segments.SegmentRecord, tiers []derivedTier) error {
	_, statErr := os.Stat(rollup.Path)
	ready, err := segments.EnsureLocalFiles(context.Background(), c.objectStore, []*segments.SegmentRecord{rollup})
	if err != nil {
		return err
	}
	if !ready {
		return nil // another replica's rollup, not uploaded yet: a later pass
	}
	if statErr != nil {
		defer func() { _ = os.Remove(rollup.Path) }()
	}

	for _, tier := range tiers {
		segment, err := c.compactor.BuildGroupedRollup(rollup, logs_core.StreamLogsRollup, tier.stream,
			tier.selectList, tier.groupBy, c.dataDir)
		if err != nil {
			return err
		}
		if segment == nil {
			continue
		}
		segment.Stats = &segments.SegmentStats{Source: rollup.Stats.Source}
		if err := c.segmentRepo.Create(segments.FromSegment(*segment)); err != nil {
			_ = os.Remove(segment.Path)
			return fmt.Errorf("failed to record %s rollup: %w", tier.stream, err)
		}
	}
	return nil
}

// rollupsBySource maps a source segment id to its rollup and returns as
// orphans the rollups whose source is gone, and a source's older duplicates
// (two passes racing).
func rollupsBySource(
	rollups []*segments.SegmentRecord, present map[string]bool,
) (map[string]*segments.SegmentRecord, []*segments.SegmentRecord) {
	sort.Slice(rollups, func(i, j int) bool { return rollups[i].CreatedAt.After(rollups[j].CreatedAt) })
	bySource := make(map[string]*segments.SegmentRecord, len(rollups))
	var orphans []*segments.SegmentRecord
	for _, rollup := range rollups {
		source := ""
		if rollup.Stats != nil {
			source = rollup.Stats.Source
		}
		if !present[source] || bySource[source] != nil {
			orphans = append(orphans, rollup)
			continue
		}
		bySource[source] = rollup
	}
	return bySource, orphans
}
