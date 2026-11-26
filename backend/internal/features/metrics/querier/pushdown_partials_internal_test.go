package metrics_querier

import (
	"testing"
	"time"

	metrics_core "obzly/internal/features/metrics/core"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// An hour's partials answer only while they hold every segment that
// overlaps it now and no tail file does: a merge or late samples in a new
// segment make it stale (read from samples instead).
func Test_CurrentPeriods_PartialsMissingASegmentOrUnderTheTail_AreStale(t *testing.T) {
	hour := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	segment := func(from, to time.Time) *segments.SegmentRecord {
		return &segments.SegmentRecord{ID: uuid.New(), MinTime: from, MaxTime: to}
	}
	a := segment(hour.Add(time.Minute), hour.Add(70*time.Minute))
	b := segment(hour.Add(65*time.Minute), hour.Add(150*time.Minute))
	late := segment(hour.Add(130*time.Minute), hour.Add(131*time.Minute))
	partials := func(end time.Time, sources ...*segments.SegmentRecord) *segments.SegmentRecord {
		return &segments.SegmentRecord{
			ID: uuid.New(), MaxTime: end,
			Stats: &segments.SegmentStats{Source: segments.MetricPartialsSourceKey(sources)},
		}
	}
	first := partials(hour.Add(time.Hour), a)
	second := partials(hour.Add(2*time.Hour), a, b)
	third := partials(hour.Add(3*time.Hour), b)

	covered := currentPeriods([]*segments.SegmentRecord{first, second, third},
		[]*segments.SegmentRecord{a, b, late}, []segments.ResolvedFile{{
			Path: "tail", MinTime: hour.Add(90 * time.Minute), MaxTime: hour.Add(91 * time.Minute),
		}})

	require.Equal(t, map[int64]*segments.SegmentRecord{hour.Add(time.Hour).UnixMilli(): first}, covered,
		"the second hour is under the tail, the third has a late segment")

	gaps := uncoveredWindows(msWindow{hour.Add(30 * time.Minute).UnixMilli(), hour.Add(4 * time.Hour).UnixMilli()}, covered)
	require.Equal(t, []msWindow{{hour.Add(time.Hour).UnixMilli() + 1, hour.Add(4 * time.Hour).UnixMilli()}}, gaps)
}

// The planner reads 1-minute partials only while the window's newest hour
// still has them (METRICS_PARTIALS_1M_DAYS), and never with 0; a step of
// whole 5 minutes reads the 5-minute ones regardless.
func Test_PartialsTier_1mTierOnlyWithinItsDays(t *testing.T) {
	t.Cleanup(func() { segments.SetMetricPartials1mDays(segments.DefaultMetricPartials1mDays) })
	segments.SetMetricPartials1mDays(1)
	hour := time.Now().UTC().Truncate(time.Hour)
	minute := time.Minute.Milliseconds()
	request := func(end time.Time, stepMs int64) (*metrics_core.RangeFunctionRequestDTO, int64, int64) {
		start := end.Add(-time.Hour).UnixMilli()
		return &metrics_core.RangeFunctionRequestDTO{
			Function: "rate", RangeMs: 5 * minute, StartMs: start, EndMs: end.UnixMilli(), StepMs: stepMs,
		}, stepMs, (end.UnixMilli() - start) / stepMs
	}

	tier, ok := partialsTier(request(hour.Add(-2*time.Hour), minute))
	require.True(t, ok)
	require.Equal(t, segments.MetricPartials1mStream, tier.Stream)

	_, ok = partialsTier(request(hour.Add(-72*time.Hour), minute))
	require.False(t, ok, "past the 1-minute tier's days: samples")

	tier, ok = partialsTier(request(hour.Add(-72*time.Hour), 5*minute))
	require.True(t, ok)
	require.Equal(t, segments.MetricPartialsStream, tier.Stream)

	segments.SetMetricPartials1mDays(0)
	_, ok = partialsTier(request(hour.Add(-2*time.Hour), minute))
	require.False(t, ok, "1-minute tier off")
}
