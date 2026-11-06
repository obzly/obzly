// Package spans_compactor merges and retires the spans stream's segments,
// as logs_compactor does. There is no count/size quota: retention alone
// bounds span disk use.
package spans_compactor

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	spans_core "obzly/internal/features/spans/core"
	"obzly/internal/storage/segments"
	"obzly/internal/telemetry"
	obzly_sampling "obzly/internal/util/sampling"

	"github.com/google/uuid"
)

const streamSpans = "spans"

type CompactionResult struct {
	MergedGroups    int
	SegmentsRemoved int
	SegmentsCreated int
}

type SpanCompactor struct {
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

func NewSpanCompactor(dataDir string, opts ...Option) (*SpanCompactor, error) {
	var cfg options
	for _, opt := range opts {
		opt(&cfg)
	}

	compactor, err := segments.NewCompactor(segments.WithSpillDir(filepath.Join(dataDir, "spill")))
	if err != nil {
		return nil, fmt.Errorf("failed to create segment compactor: %w", err)
	}

	return &SpanCompactor{
		dataDir:     dataDir,
		segmentRepo: &segments.SegmentRepository{},
		compactor:   compactor,
		objectStore: cfg.objectStore,
	}, nil
}

func (c *SpanCompactor) Close() error {
	return c.compactor.Close()
}

// CompactSmallSegments merges runs of consecutive (by time) small segments
// for a project into fewer, larger ones.
func (c *SpanCompactor) CompactSmallSegments(
	projectID uuid.UUID, sampling spans_core.SpanSampling,
) (*CompactionResult, error) {
	records, err := c.segmentRepo.ListByProject(projectID, streamSpans)
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

		var where string
		var whereArgs []any
		if planned.Level == segments.LevelCold && sampling.Active() {
			day := dayOf(group[0])
			var siblings []*segments.SegmentRecord
			for _, record := range records {
				if dayOf(record).Equal(day) {
					siblings = append(siblings, record)
				}
			}
			ready, err := segments.EnsureLocalFiles(context.Background(), c.objectStore, siblings)
			if err != nil {
				return result, err
			}
			if !ready {
				continue
			}
			where, whereArgs = samplingFilter(siblings, sampling)
		}

		if err := c.segmentRepo.RecordCompactionAttempt(idsOf(group)); err != nil {
			return result, fmt.Errorf("failed to record compaction attempt: %w", err)
		}
		merged, err := c.compactor.MergeSegmentsWhere(group, c.dataDir, planned.Level, where, whereArgs)
		if err != nil {
			// The other groups still merge; this one is retried until
			// MaxCompactionAttempts.
			mergeErrs = append(mergeErrs, fmt.Errorf("failed to merge segment group: %w", err))
			continue
		}
		if where != "" {
			var inputRows int64
			for _, record := range group {
				inputRows += record.RowCount
			}
			telemetry.SpansSampledOut.Add(float64(max(inputRows-int64(merged.RowCount), 0)))
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

// EnforceRetention deletes every segment for the project whose newest span
// is older than maxLifeDays. A no-op if maxLifeDays <= 0 (unlimited).
func (c *SpanCompactor) EnforceRetention(projectID uuid.UUID, maxLifeDays int) (int, error) {
	if maxLifeDays <= 0 {
		return 0, nil
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -maxLifeDays)
	return c.DeleteSegmentsOlderThan(projectID, cutoff)
}

func (c *SpanCompactor) DeleteSegmentsOlderThan(projectID uuid.UUID, cutoff time.Time) (int, error) {
	records, err := c.segmentRepo.ListByProject(projectID, streamSpans)
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

// DeleteAllForProject deletes every segment of a project being deleted.
func (c *SpanCompactor) DeleteAllForProject(projectID uuid.UUID) (int, error) {
	records, err := c.segmentRepo.DeleteByProject(projectID, streamSpans)
	if err != nil {
		return 0, fmt.Errorf("failed to delete segments: %w", err)
	}

	return len(records), c.deleteSegmentData(records)
}

// deleteSegments removes catalog rows before files: a stale catalog row
// fails every query, a stale file only leaks disk.
func (c *SpanCompactor) deleteSegments(records []*segments.SegmentRecord) error {
	if err := c.segmentRepo.DeleteByIDs(idsOf(records)); err != nil {
		return err
	}

	return c.deleteSegmentData(records)
}

// deleteSegmentData removes the files and object-storage copies of segments
// whose catalog rows are already gone.
func (c *SpanCompactor) deleteSegmentData(records []*segments.SegmentRecord) error {
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
func (c *SpanCompactor) MigrateLegacySegment(ctx context.Context, record *segments.SegmentRecord) error {
	return segments.MigrateLegacySegment(ctx, c.segmentRepo, c.compactor, c.objectStore, c.dataDir, record)
}

// samplingFilter keeps a span when its trace hashes under the rate, has no
// id, or - anywhere in its day's files (siblings, every level) - has an
// error or slow span. Every merge of that day, in any pass or replica,
// decides each trace the same way, so a trace is kept or dropped whole.
// The hash (util/sampling) is the one log head sampling uses, and unlike
// DuckDB's hash() the same on every version.
func samplingFilter(siblings []*segments.SegmentRecord, sampling spans_core.SpanSampling) (string, []any) {
	paths := make([]string, len(siblings))
	for i, record := range siblings {
		paths[i] = record.Path
	}
	dayFiles, dayArgs := segments.ParquetFilesExpr(paths)
	where := "coalesce(trace_id, '') = '' OR " + obzly_sampling.SQLBucket("trace_id") + " < ? " +
		"OR trace_id IN (SELECT trace_id FROM " + dayFiles + " WHERE status_code = ?"
	args := []any{obzly_sampling.Threshold(sampling.Rate)}
	args = append(args, dayArgs...)
	args = append(args, string(spans_core.StatusCodeError))
	if sampling.SlowMs > 0 {
		where += " OR date_diff('millisecond', start_time, end_time) >= ?"
		args = append(args, sampling.SlowMs)
	}
	return where + ")", args
}

// dayOf is the UTC day a cold file holds: that of its earliest row.
func dayOf(record *segments.SegmentRecord) time.Time {
	return record.MinTime.UTC().Truncate(24 * time.Hour)
}
