package metrics_compactor_test

import (
	"database/sql"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	metrics_compactor "obzly/internal/features/metrics/compactor"
	metrics_core "obzly/internal/features/metrics/core"
	metrics_ingester "obzly/internal/features/metrics/ingester"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/prometheus/prometheus/model/value"
	"github.com/stretchr/testify/require"
)

// A closed hour is folded from every segment overlapping it; when that set
// changes (a segment gone, late samples in a new one) the hour is folded
// again and the old partials deleted. The open hour waits.
func Test_BuildPartials_FoldsClosedHours_AndRefoldsWhenTheirSegmentsChange(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()
	hour := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	seedTinySegments(t, dataDir, projectID, 3, hour.Add(10*time.Minute))
	seedTinySegments(t, dataDir, projectID, 1, time.Now().UTC().Add(-time.Minute))
	repository := &segments.SegmentRepository{}
	records, err := repository.ListByProject(projectID, "metrics")
	require.NoError(t, err)
	require.Len(t, records, 4)

	compactor, err := metrics_compactor.NewMetricCompactor(dataDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = compactor.Close() })
	require.NoError(t, compactor.BuildPartials(projectID))
	partials, err := repository.ListByProject(projectID, segments.MetricPartialsStream)
	require.NoError(t, err)
	require.Len(t, partials, 1)
	var folded []*segments.SegmentRecord
	for _, record := range records {
		if record.MinTime.Before(hour.Add(time.Hour)) {
			folded = append(folded, record)
		}
	}
	require.Len(t, folded, 3)
	require.Equal(t, segments.MetricPartialsSourceKey(folded), partials[0].Stats.Source)
	require.True(t, hour.Add(time.Hour).Equal(partials[0].MaxTime))
	require.Equal(t, []string{"cpu_usage"}, partials[0].Stats.MetricNames)
	perMinute, err := repository.ListByProject(projectID, segments.MetricPartials1mStream)
	require.NoError(t, err)
	require.Len(t, perMinute, 1)
	require.Equal(t, partials[0].Stats.Source, perMinute[0].Stats.Source)

	require.NoError(t, repository.DeleteByIDs([]uuid.UUID{folded[0].ID}))
	seedTinySegments(t, dataDir, projectID, 1, hour.Add(50*time.Minute))
	require.NoError(t, compactor.BuildPartials(projectID))
	refolded, err := repository.ListByProject(projectID, segments.MetricPartialsStream)
	require.NoError(t, err)
	require.Len(t, refolded, 1)
	require.NotEqual(t, partials[0].ID, refolded[0].ID)
	require.Len(t, strings.Split(refolded[0].Stats.Source, ","), 3)
	require.NotContains(t, refolded[0].Stats.Source, folded[0].ID.String())
	perMinute, err = repository.ListByProject(projectID, segments.MetricPartials1mStream)
	require.NoError(t, err)
	require.Len(t, perMinute, 1)
	require.Equal(t, refolded[0].Stats.Source, perMinute[0].Stats.Source)

	// A tier missing (as before 1-minute partials existed) refolds both.
	require.NoError(t, repository.DeleteByIDs([]uuid.UUID{perMinute[0].ID}))
	require.NoError(t, compactor.BuildPartials(projectID))
	again, err := repository.ListByProject(projectID, segments.MetricPartialsStream)
	require.NoError(t, err)
	require.Len(t, again, 1)
	require.NotEqual(t, refolded[0].ID, again[0].ID)
	perMinute, err = repository.ListByProject(projectID, segments.MetricPartials1mStream)
	require.NoError(t, err)
	require.Len(t, perMinute, 1)
}

// A bucket is (end - 5m, end]: a sample on a 5 m edge closes the bucket
// before it. Resets add the value before each drop; a stale marker is
// kept apart from the samples; a sample stored twice counts once, two
// values in one millisecond are flagged.
func Test_BuildMetricPartials_RightClosedBuckets_ResetsStaleAndDuplicates(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()
	periodEnd := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)
	edge := periodEnd.Add(-30 * time.Minute)
	stale := math.Float64frombits(value.StaleNaN)
	points := []struct {
		at    time.Duration
		value float64
	}{
		{-4 * time.Minute, 10}, {-3 * time.Minute, 4}, {-2 * time.Minute, 8}, {-time.Minute, 2}, {0, 5},
		{time.Minute, 6}, {2 * time.Minute, stale}, {6 * time.Minute, 7}, {6 * time.Minute, 7},
		{11 * time.Minute, 3}, {11 * time.Minute, 4},
	}
	ingester, err := metrics_ingester.NewMetricIngester(dataDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ingester.Close() })
	var batch []*metrics_core.MetricSample
	for _, point := range points {
		batch = append(batch, &metrics_core.MetricSample{
			ID: uuid.New(), ProjectID: projectID, MetricName: "requests_total", Value: point.value,
			Timestamp: edge.Add(point.at), Type: metrics_core.MetricTypeCounter,
		})
	}
	require.NoError(t, ingester.StoreSamplesBatch(map[uuid.UUID][]*metrics_core.MetricSample{projectID: batch}))
	_, err = ingester.FlushAll()
	require.NoError(t, err)
	records, err := (&segments.SegmentRepository{}).ListByProject(projectID, "metrics")
	require.NoError(t, err)
	require.Len(t, records, 1)

	compactor, err := segments.NewCompactor()
	require.NoError(t, err)
	t.Cleanup(func() { _ = compactor.Close() })
	tiers, err := compactor.BuildMetricPartials(projectID, records, periodEnd, dataDir, segments.MetricPartialTiers)
	require.NoError(t, err)
	require.Len(t, tiers, 2)
	partials, perMinute := tiers[0], tiers[1]
	require.Equal(t, segments.MetricPartialsStream, partials.Stream)
	require.Equal(t, periodEnd.Add(-55*time.Minute), partials.MinTime)
	require.Equal(t, periodEnd, partials.MaxTime)
	require.Equal(t, segments.MetricPartials1mStream, perMinute.Stream)
	require.Equal(t, periodEnd.Add(-59*time.Minute), perMinute.MinTime)

	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	rows, err := db.Query(`SELECT epoch_ms(timestamp), n, first_v, last_v, resets, sum, dup, stale_t
		FROM read_parquet(?) ORDER BY timestamp`, partials.Path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = rows.Close() })
	type bucket struct {
		end, n      int64
		first, last float64
		resets, sum float64
		dup         bool
		staleAt     sql.NullInt64
	}
	var got []bucket
	for rows.Next() {
		var b bucket
		require.NoError(t, rows.Scan(&b.end, &b.n, &b.first, &b.last, &b.resets, &b.sum, &b.dup, &b.staleAt))
		if b.dup {
			b.first, b.last = 0, 0 // either value
		}
		got = append(got, b)
	}
	require.NoError(t, rows.Err())
	at := func(d time.Duration) int64 { return edge.Add(d).UnixMilli() }
	require.Equal(t, []bucket{
		{end: at(0), n: 5, first: 10, last: 5, resets: 10 + 8, sum: 29},
		{end: at(5 * time.Minute), n: 1, first: 6, last: 6, sum: 6,
			staleAt: sql.NullInt64{Int64: at(2 * time.Minute), Valid: true}},
		{end: at(10 * time.Minute), n: 1, first: 7, last: 7, sum: 7},
		{end: at(15 * time.Minute), n: 2, sum: 7, dup: true},
	}, got, "5-minute buckets, folded from the 1-minute ones")

	minuteRows, err := db.Query(`SELECT epoch_ms(timestamp), n, resets, coalesce(sum, 0), dup
		FROM read_parquet(?) ORDER BY timestamp`, perMinute.Path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = minuteRows.Close() })
	type minute struct {
		end, n      int64
		resets, sum float64
		dup         bool
	}
	var minutes []minute
	for minuteRows.Next() {
		var m minute
		require.NoError(t, minuteRows.Scan(&m.end, &m.n, &m.resets, &m.sum, &m.dup))
		minutes = append(minutes, m)
	}
	require.NoError(t, minuteRows.Err())
	require.Equal(t, []minute{
		{end: at(-4 * time.Minute), n: 1, sum: 10}, {end: at(-3 * time.Minute), n: 1, sum: 4},
		{end: at(-2 * time.Minute), n: 1, sum: 8}, {end: at(-time.Minute), n: 1, sum: 2},
		{end: at(0), n: 1, sum: 5}, {end: at(time.Minute), n: 1, sum: 6}, {end: at(2 * time.Minute)},
		{end: at(6 * time.Minute), n: 1, sum: 7}, {end: at(11 * time.Minute), n: 2, sum: 7, dup: true},
	}, minutes)
}

// 1-minute partials are built and kept METRICS_PARTIALS_1M_DAYS: older
// hours get only the 5-minute tier, the retention pass drops their
// 1-minute partials, and neither makes the hour stale (no refold). 0 builds
// and keeps none.
func Test_Partials1m_ExpireAfterTheirDays_WithoutRefolding(t *testing.T) {
	t.Cleanup(func() { segments.SetMetricPartials1mDays(segments.DefaultMetricPartials1mDays) })
	dataDir := t.TempDir()
	projectID := uuid.New()
	old := time.Now().UTC().Truncate(time.Hour).Add(-72 * time.Hour)
	recent := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	seedTinySegments(t, dataDir, projectID, 1, old.Add(10*time.Minute))
	seedTinySegments(t, dataDir, projectID, 1, recent.Add(10*time.Minute))
	compactor, err := metrics_compactor.NewMetricCompactor(dataDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = compactor.Close() })
	repository := &segments.SegmentRepository{}
	list := func(stream string) []*segments.SegmentRecord {
		records, err := repository.ListByProject(projectID, stream)
		require.NoError(t, err)
		return records
	}
	ends := func(records []*segments.SegmentRecord) []time.Time {
		var result []time.Time
		for _, record := range records {
			result = append(result, record.MaxTime.UTC())
		}
		slices.SortFunc(result, time.Time.Compare)
		return result
	}

	// Kept 7 days: both hours get both tiers.
	require.NoError(t, compactor.BuildPartials(projectID))
	require.Equal(t, []time.Time{old.Add(time.Hour), recent.Add(time.Hour)}, ends(list(segments.MetricPartials1mStream)))
	coarse := list(segments.MetricPartialsStream)
	require.Len(t, coarse, 2)

	// Kept 1 day: the retention pass drops the old hour's 1-minute
	// partials, and the fold leaves the hour as it is.
	segments.SetMetricPartials1mDays(1)
	_, err = compactor.AgeOut(projectID, time.Time{}, time.Time{})
	require.NoError(t, err)
	require.Equal(t, []time.Time{recent.Add(time.Hour)}, ends(list(segments.MetricPartials1mStream)))
	require.NoError(t, compactor.BuildPartials(projectID))
	require.ElementsMatch(t, idsOf(coarse), idsOf(list(segments.MetricPartialsStream)), "nothing refolded")
	require.Len(t, list(segments.MetricPartials1mStream), 1)

	// A changed old hour is refolded with the 5-minute tier only.
	seedTinySegments(t, dataDir, projectID, 1, old.Add(40*time.Minute))
	require.NoError(t, compactor.BuildPartials(projectID))
	require.Equal(t, []time.Time{recent.Add(time.Hour)}, ends(list(segments.MetricPartials1mStream)))
	require.Len(t, list(segments.MetricPartialsStream), 2)

	// 0: none kept, none built, nothing refolded.
	segments.SetMetricPartials1mDays(0)
	coarse = list(segments.MetricPartialsStream)
	_, err = compactor.AgeOut(projectID, time.Time{}, time.Time{})
	require.NoError(t, err)
	require.Empty(t, list(segments.MetricPartials1mStream))
	require.NoError(t, compactor.BuildPartials(projectID))
	require.Empty(t, list(segments.MetricPartials1mStream))
	require.ElementsMatch(t, idsOf(coarse), idsOf(list(segments.MetricPartialsStream)))
}

func idsOf(records []*segments.SegmentRecord) []uuid.UUID {
	ids := make([]uuid.UUID, len(records))
	for i, record := range records {
		ids[i] = record.ID
	}
	return ids
}
