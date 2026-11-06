// Package logs_compactor is the logs stream's maintenance path on the Parquet
// storage engine: merging small segments and enforcing retention/quotas.
//
// Segments are immutable, so retention and quotas delete whole segments: a
// project can briefly hold up to one segment's rows past its exact limit, in
// exchange for never rewriting files.
package logs_compactor

import (
	logs_core "obzly/internal/features/logs/core"

	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"obzly/internal/storage/segments"

	"github.com/google/uuid"
)

type CompactionResult struct {
	MergedGroups    int // number of merge operations performed
	SegmentsRemoved int // small segments deleted (folded into merged ones)
	SegmentsCreated int // new merged segments written
}

type LogCompactor struct {
	dataDir     string
	segmentRepo *segments.SegmentRepository
	compactor   *segments.Compactor
	// objectStore is nil without object storage (WithObjectStore); deletes then
	// remove only local files and catalog rows.
	objectStore *segments.ObjectStore
	stream      string
}

type Option func(*options)

type options struct {
	objectStore *segments.ObjectStore
	stream      string
}

func WithStream(stream string) Option {
	return func(o *options) {
		o.stream = stream
	}
}

// WithObjectStore makes deleteSegments also remove each segment's object
// storage copy, so uploaded copies don't outlive their deletion.
func WithObjectStore(store *segments.ObjectStore) Option {
	return func(o *options) {
		o.objectStore = store
	}
}

func NewLogCompactor(dataDir string, opts ...Option) (*LogCompactor, error) {
	cfg := options{stream: logs_core.StreamLogs}
	for _, opt := range opts {
		opt(&cfg)
	}

	compactor, err := segments.NewCompactor(segments.WithSpillDir(filepath.Join(dataDir, "spill")))
	if err != nil {
		return nil, fmt.Errorf("failed to create segment compactor: %w", err)
	}

	return &LogCompactor{
		dataDir:     dataDir,
		segmentRepo: &segments.SegmentRepository{},
		compactor:   compactor,
		objectStore: cfg.objectStore,
		stream:      cfg.stream,
	}, nil
}

func (c *LogCompactor) Close() error {
	return c.compactor.Close()
}

// CompactSmallSegments merges runs of consecutive small segments for a
// project up to the Writer's flush size/row targets.
func (c *LogCompactor) CompactSmallSegments(projectID uuid.UUID) (*CompactionResult, error) {
	records, err := c.segmentRepo.ListByProject(projectID, c.stream)
	if err != nil {
		return nil, fmt.Errorf("failed to list segments: %w", err)
	}

	result := &CompactionResult{}

	plan := segments.PlanCompaction(segments.Compactable(records), time.Now().UTC())
	if err := c.segmentRepo.SetLevel(idsOf(plan.Promote), segments.LevelCompacted); err != nil {
		return result, fmt.Errorf("failed to promote segments: %w", err)
	}

	var mergeErrs []error
	for _, planned := range plan.Merges {
		group := planned.Records
		ready, err := segments.EnsureLocalFiles(context.Background(), c.objectStore, group)
		if err != nil {
			return result, err
		}
		if !ready {
			// Another replica's segment, not uploaded yet: next pass.
			continue
		}

		if err := c.segmentRepo.RecordCompactionAttempt(idsOf(group)); err != nil {
			return result, fmt.Errorf("failed to record compaction attempt: %w", err)
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
				return result, fmt.Errorf("failed to discard merged segment: %w", cleanupErr)
			}
			if errors.Is(err, segments.ErrSegmentsGone) {
				continue
			}
			return result, fmt.Errorf("failed to record merged segment: %w", err)
		}

		if err := c.deleteSegmentData(group); err != nil {
			return result, fmt.Errorf("failed to clean up merged-away segments: %w", err)
		}

		result.MergedGroups++
		result.SegmentsRemoved += len(group)
		result.SegmentsCreated++
	}

	return result, errors.Join(mergeErrs...)
}

// EnforceRetention deletes every segment for the project whose newest row is
// older than maxLifeDays. Returns the number of segments deleted. A no-op if
// maxLifeDays <= 0 (unlimited retention).
func (c *LogCompactor) EnforceRetention(projectID uuid.UUID, maxLifeDays int) (int, error) {
	if maxLifeDays <= 0 {
		return 0, nil
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -maxLifeDays)
	return c.DeleteSegmentsOlderThan(projectID, cutoff)
}

// DeleteSegmentsOlderThan deletes every segment for a project whose newest
// row is older than cutoff.
func (c *LogCompactor) DeleteSegmentsOlderThan(projectID uuid.UUID, cutoff time.Time) (int, error) {
	records, err := c.segmentRepo.ListByProject(projectID, c.stream)
	if err != nil {
		return 0, fmt.Errorf("failed to list segments: %w", err)
	}

	var expired []*segments.SegmentRecord
	for _, record := range records {
		if record.MaxTime.Before(cutoff) {
			expired = append(expired, record)
		}
	}
	if len(expired) == 0 {
		return 0, nil
	}

	return len(expired), c.deleteSegments(expired)
}

// DeleteAllForProject deletes every segment (files and catalog rows) of a
// deleted project.
func (c *LogCompactor) DeleteAllForProject(projectID uuid.UUID) (int, error) {
	records, err := c.segmentRepo.DeleteByProject(projectID, c.stream)
	if err != nil {
		return 0, fmt.Errorf("failed to delete segments: %w", err)
	}
	if c.stream == logs_core.StreamLogs {
		rollups, err := c.segmentRepo.DeleteByProject(projectID, logs_core.StreamLogsRollup)
		if err != nil {
			return 0, fmt.Errorf("failed to delete log rollups: %w", err)
		}
		if err := c.deleteSegmentData(rollups); err != nil {
			return 0, err
		}
	}

	return len(records), c.deleteSegmentData(records)
}

// EnforceCountQuota deletes the oldest segments for a project until its
// total row count is at or below maxLogsAmount. A no-op if maxLogsAmount <= 0
// (unlimited).
func (c *LogCompactor) EnforceCountQuota(projectID uuid.UUID, maxLogsAmount int64) (int, error) {
	if maxLogsAmount <= 0 {
		return 0, nil
	}

	// One SQL aggregate for the sum, then only the oldest segments over it are
	// loaded, not the whole catalog.
	stats, err := c.segmentRepo.AggregateStatsForProject(projectID, c.stream)
	if err != nil {
		return 0, fmt.Errorf("failed to sum segments: %w", err)
	}
	if stats.RowCount <= maxLogsAmount {
		return 0, nil
	}

	victims, err := c.segmentRepo.ListOldestCovering(projectID, c.stream, segments.QuotaRows,
		stats.RowCount-maxLogsAmount)
	if err != nil {
		return 0, fmt.Errorf("failed to list segments: %w", err)
	}
	if len(victims) == 0 {
		return 0, nil
	}
	return len(victims), c.deleteSegments(victims)
}

// EnforceSizeQuota deletes the oldest segments until the project is under a
// tiered target derived from maxLogsSizeMB. No-op if maxLogsSizeMB <= 0.
func (c *LogCompactor) EnforceSizeQuota(projectID uuid.UUID, maxLogsSizeMB int) (int, error) {
	if maxLogsSizeMB <= 0 {
		return 0, nil
	}

	stats, err := c.segmentRepo.AggregateStatsForProject(projectID, c.stream)
	if err != nil {
		return 0, fmt.Errorf("failed to sum segments: %w", err)
	}

	maxBytes := int64(maxLogsSizeMB) * 1024 * 1024
	if stats.SizeBytes <= maxBytes {
		return 0, nil
	}

	targetBytes := int64(float64(maxBytes) * calculateCleanupPercentage(maxLogsSizeMB))
	victims, err := c.segmentRepo.ListOldestCovering(projectID, c.stream, segments.QuotaBytes,
		stats.SizeBytes-targetBytes)
	if err != nil {
		return 0, fmt.Errorf("failed to list segments: %w", err)
	}
	if len(victims) == 0 {
		return 0, nil
	}
	return len(victims), c.deleteSegments(victims)
}

// deleteSegments deletes catalog rows FIRST, then files: a row outliving its
// file breaks every later query for the project, while a file outliving its
// row is only an orphan (queries enumerate the catalog).
func (c *LogCompactor) deleteSegments(records []*segments.SegmentRecord) error {
	if err := c.segmentRepo.DeleteByIDs(idsOf(records)); err != nil {
		return err
	}

	return c.deleteSegmentData(records)
}

// deleteSegmentData removes the files and object-storage copies of segments
// whose catalog rows are already gone.
func (c *LogCompactor) deleteSegmentData(records []*segments.SegmentRecord) error {
	if err := c.compactor.DeleteSegmentFiles(records); err != nil {
		return err
	}

	if c.objectStore != nil {
		var keys []string
		for _, record := range records {
			if record.ObjectKey != "" {
				keys = append(keys, record.ObjectKey)
				if record.Stats != nil && record.Stats.TokenBloom {
					keys = append(keys, record.ObjectKey+segments.BloomSuffix)
				}
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

// calculateCleanupPercentage is the size-quota cleanup target, matching
// logs/cleanup: smaller quotas are cleaned harder.
func calculateCleanupPercentage(quotaSizeMB int) float64 {
	switch {
	case quotaSizeMB <= 10:
		return 0.85
	case quotaSizeMB <= 100:
		return 0.90
	case quotaSizeMB <= 500:
		return 0.95
	default:
		return 0.98
	}
}

// MigrateLegacySegment rewrites one version 1 segment with normalized names
// (see segments.MigrateLegacySegment).
func (c *LogCompactor) MigrateLegacySegment(ctx context.Context, record *segments.SegmentRecord) error {
	return segments.MigrateLegacySegment(ctx, c.segmentRepo, c.compactor, c.objectStore, c.dataDir, record)
}
