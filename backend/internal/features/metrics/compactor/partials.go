package metrics_compactor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"time"

	"obzly/internal/storage/segments"

	"github.com/google/uuid"
)

const (
	// partialsBudget: hours a project gets partials for per pass, newest
	// first (what dashboards read); none is started after
	// partialsPassTime, so a backfill takes a few passes.
	partialsBudget   = 24
	partialsPassTime = time.Minute
	// partialsCloseDelay: an hour is folded once this long has passed
	// since its end, when its samples are flushed; later ones make it
	// stale (a new L0 among its segments) until the next fold.
	partialsCloseDelay = 10 * time.Minute
)

// BuildPartials folds closed hours into partials (every
// segments.MetricPartialTiers stream) from every metrics segment
// overlapping each - any level, so the last day's L0 and L1 hours are
// covered too - and deletes partials whose segments have changed since
// (merged, joined by late samples or gone). An hour is folded again
// whenever its set of segments changes, or a tier it should have is
// missing; past METRICS_PARTIALS_1M_DAYS it has only the 5-minute tier.
func (c *MetricCompactor) BuildPartials(projectID uuid.UUID) error {
	records, err := c.segmentRepo.ListByProject(projectID, "metrics")
	if err != nil {
		return fmt.Errorf("failed to list segments: %w", err)
	}
	now := time.Now().UTC()
	closedBefore := now.Add(-partialsCloseDelay).UnixMilli()
	periods := periodSources(records, closedBefore)
	tiersOf := func(end int64) []segments.MetricPartialTier {
		return segments.MetricPartialTiersFor(time.UnixMilli(end).UTC(), now)
	}

	kept := map[int64][]*segments.SegmentRecord{}
	var stale []*segments.SegmentRecord
	for _, tier := range segments.MetricPartialTiers {
		built, err := c.segmentRepo.ListByProject(projectID, tier.Stream)
		if err != nil {
			return fmt.Errorf("failed to list metric partials: %w", err)
		}
		seen := map[int64]bool{}
		for _, partial := range built {
			end := partial.MaxTime.UnixMilli()
			if !slices.Contains(tiersOf(end), tier) {
				// Past the tier's retention: neither needed nor stale (the
				// retention pass deletes it).
				continue
			}
			sources, ok := periods[end]
			if seen[end] || !ok || partial.Stats == nil ||
				partial.Stats.Source != segments.MetricPartialsSourceKey(sources) {
				stale = append(stale, partial)
				continue
			}
			seen[end] = true
			kept[end] = append(kept[end], partial)
		}
	}
	current := map[int64]bool{}
	for end, partials := range kept {
		if len(partials) == len(tiersOf(end)) {
			current[end] = true
		} else {
			stale = append(stale, partials...)
		}
	}
	if len(stale) > 0 {
		if err := c.deleteSegments(stale); err != nil {
			return err
		}
	}

	var missing []int64
	for end := range periods {
		if !current[end] {
			missing = append(missing, end)
		}
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i] > missing[j] })
	if len(missing) > partialsBudget {
		missing = missing[:partialsBudget]
	}
	downloaded := map[string]string{}
	defer func() {
		for _, path := range downloaded {
			_ = os.Remove(path)
		}
	}()
	started := time.Now()
	for _, end := range missing {
		if time.Since(started) > partialsPassTime {
			return nil
		}
		err := c.buildPeriod(projectID, periods[end], time.UnixMilli(end).UTC(), tiersOf(end), downloaded)
		if err != nil && !errors.Is(err, errNotReadable) {
			return err
		}
	}
	return nil
}

// periodSources maps the end (ms) of every period that closed before
// closedBefore and holds samples to the segments overlapping it.
func periodSources(records []*segments.SegmentRecord, closedBefore int64) map[int64][]*segments.SegmentRecord {
	period := segments.MetricPartialsPeriod.Milliseconds()
	periods := map[int64][]*segments.SegmentRecord{}
	for _, record := range records {
		first := segments.MetricPartialsPeriodEnd(record.MinTime.UnixMilli())
		last := segments.MetricPartialsPeriodEnd(record.MaxTime.UnixMilli())
		for end := first; end <= last && end <= closedBefore; end += period {
			periods[end] = append(periods[end], record)
		}
	}
	return periods
}

// buildPeriod folds one period, downloading the segments not on this disk
// once per pass (downloaded, removed at its end).
func (c *MetricCompactor) buildPeriod(
	projectID uuid.UUID, sources []*segments.SegmentRecord, end time.Time, tiers []segments.MetricPartialTier,
	downloaded map[string]string,
) error {
	for _, source := range sources {
		if _, err := os.Stat(source.Path); err == nil {
			continue
		}
		ready, err := segments.EnsureLocalFiles(context.Background(), c.objectStore, []*segments.SegmentRecord{source})
		if err != nil {
			return err
		}
		if !ready {
			return fmt.Errorf("segment %s: %w", source.ID, errNotReadable)
		}
		downloaded[source.ID.String()] = source.Path
	}
	partials, err := c.compactor.BuildMetricPartials(projectID, sources, end, c.dataDir, tiers)
	if err != nil {
		return err
	}
	// A tier recorded without the other is refolded on the next pass.
	for i, partial := range partials {
		if err := c.segmentRepo.Create(segments.FromSegment(*partial)); err != nil {
			for _, unrecorded := range partials[i:] {
				_ = os.Remove(unrecorded.Path)
			}
			return fmt.Errorf("failed to record metric partials: %w", err)
		}
	}
	return nil
}
