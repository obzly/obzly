// Package metrics_compactor is the metrics stream's maintenance path
// (compaction, rollups, retention), mirroring spans_compactor.
package metrics_compactor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"obzly/internal/storage/segments"

	"github.com/google/uuid"
)

// streams: samples and their series, compacted, expired and deleted alike.
var streams = []string{"metrics", "metrics_series"}

// allStreams adds the rollups, which are expired and deleted but
// never merged: each stays tied to the cold segment it was built from.
var allStreams = []string{"metrics", "metrics_series", "metrics_5m", "metrics_1h"}

// rollupBudget: cold segments rolled up per project per pass (each is up to
// L2MaxBytes; the backlog of existing ones drains over passes).
const rollupBudget = 1

type CompactionResult struct {
	MergedGroups    int
	SegmentsRemoved int
	SegmentsCreated int
}

type MetricCompactor struct {
	dataDir     string
	segmentRepo *segments.SegmentRepository
	compactor   *segments.Compactor
	// objectStore is nil unless object storage is configured (WithObjectStore).
	objectStore *segments.ObjectStore
}

type Option func(*options)

type options struct {
	objectStore *segments.ObjectStore
}

// WithObjectStore makes segment deletion also remove the cold-tier copy.
func WithObjectStore(store *segments.ObjectStore) Option {
	return func(o *options) {
		o.objectStore = store
	}
}

func NewMetricCompactor(dataDir string, opts ...Option) (*MetricCompactor, error) {
	var cfg options
	for _, opt := range opts {
		opt(&cfg)
	}

	compactor, err := segments.NewCompactor(segments.WithSpillDir(filepath.Join(dataDir, "spill")))
	if err != nil {
		return nil, fmt.Errorf("failed to create segment compactor: %w", err)
	}

	return &MetricCompactor{
		dataDir:     dataDir,
		segmentRepo: &segments.SegmentRepository{},
		compactor:   compactor,
		objectStore: cfg.objectStore,
	}, nil
}

func (c *MetricCompactor) Close() error {
	return c.compactor.Close()
}

// CompactSmallSegments merges runs of consecutive (by time) small segments
// into fewer, larger ones, then builds missing rollups.
func (c *MetricCompactor) CompactSmallSegments(projectID uuid.UUID) (*CompactionResult, error) {
	result := &CompactionResult{}
	for _, stream := range streams {
		if err := c.compactStream(projectID, stream, result); err != nil {
			return result, err
		}
	}
	return result, c.RollupColdSegments(projectID)
}

// RollupColdSegments builds the 5m and 1h rollups of cold (L2) metrics
// segments that lack them, up to rollupBudget segments a pass. A cold
// segment is final, so its rollups never go stale; a missing one is built
// on a later pass.
func (c *MetricCompactor) RollupColdSegments(projectID uuid.UUID) error {
	records, err := c.segmentRepo.ListByProject(projectID, "metrics")
	if err != nil {
		return fmt.Errorf("failed to list segments: %w", err)
	}
	built, err := c.rollupsBySource(projectID)
	if err != nil {
		return err
	}
	if err := c.deleteOrphanRollups(records, built); err != nil {
		return err
	}

	done := 0
	for _, record := range records {
		if done >= rollupBudget {
			return nil
		}
		if record.Level != segments.LevelCold {
			continue
		}
		if missing := missingRollups(built, record); len(missing) > 0 {
			done++
			if err := c.rollUp(record, missing); err != nil && !errors.Is(err, errNotReadable) {
				return err
			}
		}
	}
	return nil
}

// AgeOut retires raw samples older than rawBefore, and rollups and the
// series stream older than rollupsBefore (the same time for no aging). A raw segment aging out gets its rollups first (kept for
// the next pass if that fails), marked aged so queries read them as the
// only data for that time.
func (c *MetricCompactor) AgeOut(projectID uuid.UUID, rawBefore, rollupsBefore time.Time) (int, error) {
	if rollupsBefore.After(rawBefore) {
		rollupsBefore = rawBefore
	}
	var expired, aging []*segments.SegmentRecord
	for _, stream := range allStreams {
		records, err := c.segmentRepo.ListByProject(projectID, stream)
		if err != nil {
			return 0, fmt.Errorf("failed to list segments: %w", err)
		}
		for _, record := range records {
			switch {
			case record.MaxTime.Before(rollupsBefore):
				expired = append(expired, record)
			case stream == "metrics" && record.MaxTime.Before(rawBefore):
				aging = append(aging, record)
			}
		}
	}

	var errs []error
	if len(aging) > 0 {
		built, err := c.rollupsBySource(projectID)
		if err != nil {
			return 0, err
		}
		var aged []string
		for _, record := range aging {
			if missing := missingRollups(built, record); len(missing) > 0 {
				if err := c.rollUp(record, missing); err != nil {
					errs = append(errs, fmt.Errorf("segment %s kept until its rollups exist: %w", record.ID, err))
					continue
				}
			}
			aged = append(aged, record.ID.String())
			expired = append(expired, record)
		}
		if err := c.segmentRepo.MarkRollupsAged(projectID, aged); err != nil {
			return 0, fmt.Errorf("failed to mark rollups aged: %w", err)
		}
	}
	if len(expired) > 0 {
		errs = append(errs, c.deleteSegments(expired))
	}
	return len(expired), errors.Join(errs...)
}

// errNotReadable: a segment neither on this disk nor uploaded yet (another
// replica wrote it); its rollups wait for the upload.
var errNotReadable = errors.New("neither local nor uploaded yet")

// rollupsBySource: per rollup stream, the source ids that have a rollup.
func (c *MetricCompactor) rollupsBySource(projectID uuid.UUID) (map[string]map[string]*segments.SegmentRecord, error) {
	built := map[string]map[string]*segments.SegmentRecord{}
	for stream := range segments.RollupStreams {
		rollups, err := c.segmentRepo.ListByProject(projectID, stream)
		if err != nil {
			return nil, fmt.Errorf("failed to list rollups: %w", err)
		}
		built[stream] = map[string]*segments.SegmentRecord{}
		for _, rollup := range rollups {
			if rollup.Stats != nil {
				built[stream][rollup.Stats.Source] = rollup
			}
		}
	}
	return built, nil
}

// deleteOrphanRollups deletes rollups whose source is gone without aging
// (e.g. rewritten by the data migration), which would otherwise count its
// samples twice once read beside the rewrite's own rollups.
func (c *MetricCompactor) deleteOrphanRollups(
	records []*segments.SegmentRecord, built map[string]map[string]*segments.SegmentRecord,
) error {
	present := make(map[string]bool, len(records))
	for _, record := range records {
		present[record.ID.String()] = true
	}
	var orphans []*segments.SegmentRecord
	for _, bySource := range built {
		for source, rollup := range bySource {
			if !present[source] && !rollup.Stats.Aged {
				orphans = append(orphans, rollup)
				delete(bySource, source)
			}
		}
	}
	if len(orphans) == 0 {
		return nil
	}
	return c.deleteSegments(orphans)
}

func missingRollups(built map[string]map[string]*segments.SegmentRecord, record *segments.SegmentRecord) []string {
	var missing []string
	for stream := range segments.RollupStreams {
		if built[stream][record.ID.String()] == nil {
			missing = append(missing, stream)
		}
	}
	return missing
}

// rollUp builds record's missing rollups.
func (c *MetricCompactor) rollUp(record *segments.SegmentRecord, missing []string) error {
	ready, err := segments.EnsureLocalFiles(context.Background(), c.objectStore, []*segments.SegmentRecord{record})
	if err != nil {
		return err
	}
	if !ready {
		return fmt.Errorf("segment %s: %w", record.ID, errNotReadable)
	}
	for _, stream := range missing {
		rollup, err := c.compactor.BuildRollup(record, stream, c.dataDir)
		if err != nil {
			return err
		}
		if rollup == nil {
			continue
		}
		if err := c.segmentRepo.Create(segments.FromSegment(*rollup)); err != nil {
			_ = os.Remove(rollup.Path)
			return fmt.Errorf("failed to record rollup: %w", err)
		}
	}
	return nil
}

func (c *MetricCompactor) compactStream(projectID uuid.UUID, stream string, result *CompactionResult) error {
	records, err := c.segmentRepo.ListByProject(projectID, stream)
	if err != nil {
		return fmt.Errorf("failed to list segments: %w", err)
	}

	plan := segments.PlanCompaction(segments.Compactable(records), time.Now().UTC())
	if err := c.segmentRepo.SetLevel(idsOf(plan.Promote), segments.LevelCompacted); err != nil {
		return fmt.Errorf("failed to promote segments: %w", err)
	}

	var mergeErrs []error
	for _, planned := range plan.Merges {
		group := planned.Records
		ready, err := segments.EnsureLocalFiles(context.Background(), c.objectStore, group)
		if err != nil {
			return err
		}
		if !ready {
			// Another replica's segment, not uploaded yet: next pass.
			continue
		}

		if err := c.segmentRepo.RecordCompactionAttempt(idsOf(group)); err != nil {
			return fmt.Errorf("failed to record compaction attempt: %w", err)
		}
		merged, err := c.compactor.MergeSegmentsAt(group, c.dataDir, planned.Level)
		if err != nil {
			// The other groups still merge; this one is retried until
			// MaxCompactionAttempts.
			mergeErrs = append(mergeErrs, fmt.Errorf("failed to merge segment group: %w", err))
			continue
		}

		mergedRecord := segments.FromSegment(*merged)
		if err := c.segmentRepo.ReplaceSegments(idsOf(group), mergedRecord); err != nil {
			// Discard the merged file; its rows are either still in the
			// sources or were deliberately deleted.
			if cleanupErr := c.compactor.DeleteSegmentFiles(
				[]*segments.SegmentRecord{mergedRecord},
			); cleanupErr != nil {
				return fmt.Errorf("failed to discard merged segment: %w", cleanupErr)
			}
			if errors.Is(err, segments.ErrSegmentsGone) {
				continue
			}
			return fmt.Errorf("failed to record merged segment: %w", err)
		}

		if err := c.deleteSegmentData(group); err != nil {
			return fmt.Errorf("failed to clean up merged-away segments: %w", err)
		}

		result.MergedGroups++
		result.SegmentsRemoved += len(group)
		result.SegmentsCreated++
	}

	return errors.Join(mergeErrs...)
}

// EnforceRetention deletes every segment for the project whose newest
// sample is older than maxLifeDays. A no-op if maxLifeDays <= 0 (unlimited).
func (c *MetricCompactor) EnforceRetention(projectID uuid.UUID, maxLifeDays int) (int, error) {
	if maxLifeDays <= 0 {
		return 0, nil
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -maxLifeDays)
	return c.DeleteSegmentsOlderThan(projectID, cutoff)
}

func (c *MetricCompactor) DeleteSegmentsOlderThan(projectID uuid.UUID, cutoff time.Time) (int, error) {
	return c.AgeOut(projectID, cutoff, cutoff)
}

// DeleteAllForProject deletes every segment of a project, regardless of age.
func (c *MetricCompactor) DeleteAllForProject(projectID uuid.UUID) (int, error) {
	var all []*segments.SegmentRecord
	for _, stream := range allStreams {
		records, err := c.segmentRepo.DeleteByProject(projectID, stream)
		if err != nil {
			return 0, fmt.Errorf("failed to delete segments: %w", err)
		}
		all = append(all, records...)
	}
	return len(all), c.deleteSegmentData(all)
}

// deleteSegments removes catalog rows before files: a stale catalog row
// fails every future query, a stale file only leaks disk.
func (c *MetricCompactor) deleteSegments(records []*segments.SegmentRecord) error {
	if err := c.segmentRepo.DeleteByIDs(idsOf(records)); err != nil {
		return err
	}

	return c.deleteSegmentData(records)
}

// deleteSegmentData removes the files and object-storage copies of segments
// whose catalog rows are already gone.
func (c *MetricCompactor) deleteSegmentData(records []*segments.SegmentRecord) error {
	if err := c.compactor.DeleteSegmentFiles(records); err != nil {
		return err
	}

	if c.objectStore != nil {
		var keys []string
		for _, record := range records {
			if record.ObjectKey != "" {
				keys = append(keys, record.ObjectKey)
			}
		}
		if err := c.objectStore.DeleteBatch(context.Background(), keys); err != nil {
			return fmt.Errorf("failed to delete object storage copies of %d segments: %w", len(keys), err)
		}
	}

	return nil
}

func idsOf(records []*segments.SegmentRecord) []uuid.UUID {
	ids := make([]uuid.UUID, len(records))
	for i, record := range records {
		ids[i] = record.ID
	}
	return ids
}

// MigrateLegacySegment rewrites one version 1 segment with normalized names
// (see segments.MigrateLegacySegment).
func (c *MetricCompactor) MigrateLegacySegment(ctx context.Context, record *segments.SegmentRecord) error {
	return segments.MigrateLegacySegment(ctx, c.segmentRepo, c.compactor, c.objectStore, c.dataDir, record)
}
