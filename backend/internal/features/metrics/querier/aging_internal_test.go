package metrics_querier

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	metrics_compactor "obzly/internal/features/metrics/compactor"
	metrics_core "obzly/internal/features/metrics/core"
	"obzly/internal/storage/segments"
	"obzly/internal/telemetry"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// Rollup-only aging: raw samples age out after their
// rollups are built and marked aged; selects at any step then read the
// rollups - real samples, the maxima intact - until the rollups age out too.
func Test_AgeOut_KeepsRollups_ReadAtAnyStep(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	projectID, _ := seedParitySeries(t, now)
	repo := &segments.SegmentRepository{}
	records, err := repo.ListByProject(projectID, "metrics")
	require.NoError(t, err)
	ids := make([]uuid.UUID, len(records))
	for i, record := range records {
		ids[i] = record.ID
	}
	require.NoError(t, repo.SetLevel(ids, segments.LevelCold))
	compactor := newTestMetricCompactor(t)
	q := newParityQuerier(t, nil)
	from, to := now.Add(-time.Hour), now.Add(time.Hour)
	selectAt := func(stepMs int64) map[string][]metrics_core.SampleDTO {
		response, err := q.SelectSeries(context.Background(), projectID, &metrics_core.SelectSeriesRequestDTO{
			TimeRange: &metrics_core.TimeRangeDTO{From: &from, To: &to}, StepMs: stepMs,
		})
		require.NoError(t, err)
		result := map[string][]metrics_core.SampleDTO{}
		for _, series := range response.Series {
			result[seriesKey(series.MetricName, series.Labels)] = series.Samples
		}
		return result
	}
	raw := selectAt(0)
	require.Len(t, raw, 8)
	seriesRows := countSegments(t, repo, projectID, "metrics_series")
	require.Positive(t, seriesRows)

	// Every raw segment is past its retention; no rollups exist yet.
	retired, err := compactor.AgeOut(projectID, now.Add(time.Hour), now.Add(-10*24*time.Hour))
	require.NoError(t, err)
	require.Equal(t, len(records), retired)
	require.Zero(t, countSegments(t, repo, projectID, "metrics"))
	require.Equal(t, seriesRows, countSegments(t, repo, projectID, "metrics_series"), "series outlive samples")
	for stream := range segments.RollupStreams {
		rollups, err := repo.ListByProject(projectID, stream)
		require.NoError(t, err)
		require.Len(t, rollups, len(records), stream)
		for _, rollup := range rollups {
			require.True(t, rollup.Stats.Aged, stream)
		}
	}

	for _, stepMs := range []int64{0, 60_000, 3_600_000} {
		rolled := selectAt(stepMs)
		require.Len(t, rolled, len(raw), "step %d", stepMs)
		for key, samples := range rolled {
			rawValues := map[int64]float64{}
			maxRaw := math.Inf(-1)
			for _, sample := range raw[key] {
				rawValues[sample.Timestamp.UnixNano()] = sample.Value
				maxRaw = math.Max(maxRaw, sample.Value)
			}
			maxRolled := math.Inf(-1)
			for _, sample := range samples {
				value, ok := rawValues[sample.Timestamp.UnixNano()]
				require.True(t, ok, "%s at step %d: a real sample", key, stepMs)
				require.Equal(t, value, sample.Value)
				maxRolled = math.Max(maxRolled, sample.Value)
			}
			require.Equal(t, maxRaw, maxRolled, "%s at step %d: the max survives", key, stepMs)
		}
	}

	// Then the rollups age out as well.
	_, err = compactor.AgeOut(projectID, now.Add(time.Hour), now.Add(time.Hour))
	require.NoError(t, err)
	require.Empty(t, selectAt(0))
	for _, stream := range []string{"metrics_5m", "metrics_1h", "metrics_series"} {
		require.Zero(t, countSegments(t, repo, projectID, stream), stream)
	}
}

// Without aging, rollups go with their samples.
func Test_AgeOut_WithoutAging_DeletesRollupsWithSamples(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	projectID, records := coldRolledUpProject(t, now)
	compactor := newTestMetricCompactor(t)

	retired, err := compactor.DeleteSegmentsOlderThan(projectID, now.Add(time.Hour))
	require.NoError(t, err)
	require.GreaterOrEqual(t, retired, 3*len(records))
	repo := &segments.SegmentRepository{}
	for _, stream := range []string{"metrics", "metrics_5m", "metrics_1h"} {
		require.Zero(t, countSegments(t, repo, projectID, stream), stream)
	}
}

// A rollup whose segment is gone without aging (e.g. rewritten by the data
// migration) is deleted, so it is never read beside the rewrite's own.
func Test_RollupColdSegments_DeletesOrphanRollups(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	projectID, records := coldRolledUpProject(t, now)
	repo := &segments.SegmentRepository{}
	require.NoError(t, repo.DeleteByIDs([]uuid.UUID{records[0].ID}))

	require.NoError(t, newTestMetricCompactor(t).RollupColdSegments(projectID))
	for stream := range segments.RollupStreams {
		rollups, err := repo.ListByProject(projectID, stream)
		require.NoError(t, err)
		require.Len(t, rollups, len(records)-1, stream)
		for _, rollup := range rollups {
			require.NotEqual(t, records[0].ID.String(), rollup.Stats.Source, stream)
		}
	}
}

// An aged rollup whose segment is still there (its retirement failed) is
// not read beside it (duplicate points are dropped, but it costs reads and
// makes push-down decline).
func Test_SelectSeries_AgedRollupOfAPresentSegment_IsNotReadTwice(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	projectID, records := coldRolledUpProject(t, now)
	q := newParityQuerier(t, nil)
	from, to := now.Add(-time.Hour), now.Add(time.Hour)
	count := func() int {
		response, err := q.SelectSeries(context.Background(), projectID, &metrics_core.SelectSeriesRequestDTO{
			TimeRange: &metrics_core.TimeRangeDTO{From: &from, To: &to},
		})
		require.NoError(t, err)
		n := 0
		for _, series := range response.Series {
			n += len(series.Samples)
		}
		return n
	}
	before := count()
	sources := make([]string, len(records))
	for i, record := range records {
		sources[i] = record.ID.String()
	}
	require.NoError(t, (&segments.SegmentRepository{}).MarkRollupsAged(projectID, sources))
	rollupReads := func() float64 {
		total := 0.0
		for _, source := range []string{telemetry.SourceLocal, telemetry.SourceCache, telemetry.SourceObjectStore} {
			total += testutil.ToFloat64(telemetry.QuerySegmentReads.WithLabelValues("metrics_5m", source))
		}
		return total
	}
	readsBefore := rollupReads()
	require.Equal(t, before, count())
	require.Zero(t, rollupReads()-readsBefore, "no rollup opened")
}

// coldRolledUpProject: the parity seed as cold segments with both rollups.
func coldRolledUpProject(t *testing.T, now time.Time) (uuid.UUID, []*segments.SegmentRecord) {
	t.Helper()
	projectID, _ := seedParitySeries(t, now)
	repo := &segments.SegmentRepository{}
	records, err := repo.ListByProject(projectID, "metrics")
	require.NoError(t, err)
	ids := make([]uuid.UUID, len(records))
	for i, record := range records {
		ids[i] = record.ID
	}
	require.NoError(t, repo.SetLevel(ids, segments.LevelCold))
	compactor := newTestMetricCompactor(t)
	for range len(records) {
		require.NoError(t, compactor.RollupColdSegments(projectID))
	}
	return projectID, records
}

func newTestMetricCompactor(t *testing.T) *metrics_compactor.MetricCompactor {
	t.Helper()
	compactor, err := metrics_compactor.NewMetricCompactor(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = compactor.Close() })
	return compactor
}

func countSegments(t *testing.T, repo *segments.SegmentRepository, projectID uuid.UUID, stream string) int {
	t.Helper()
	records, err := repo.ListByProject(projectID, stream)
	require.NoError(t, err, fmt.Sprint(stream))
	return len(records)
}
