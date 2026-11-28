package metrics_querier

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"testing"
	"time"

	metrics_core "obzly/internal/features/metrics/core"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// A one-step week (the Synthetics 7-day uptime) reads the hourly rollups of
// the cold segments for the whole hours inside the window and samples only
// for the first partial hour and what isn't rolled up yet, and answers what
// the engine does over every sample: sum, count, avg and min, aggregated
// or not, with NaNs, over one process or several executors. Proof the
// rollups answer: with the sample files of the segments wholly inside the
// rolled-up hours removed, the answers don't change.
func Test_EvaluateRangeFunction_InstantWeek_ReadsHourlyRollups_MatchesTheEngine(t *testing.T) {
	end := time.Now().UTC().Truncate(time.Minute).Add(-17*time.Second - 345*time.Millisecond)
	projectID, reference, records := seedRolledUpWeek(t, end)
	queriers := map[string]*MetricQuerier{
		"one process": newParityQuerier(t, nil),
		"4 executors": newParityQuerier(t, executorsN(4)),
	}
	cases := []chunkCase{
		{function: "sum_over_time", aggregation: "sum", by: []string{"job"}},
		{function: "count_over_time", aggregation: "sum", by: []string{"job"}},
		{function: "sum_over_time"},
		{function: "count_over_time"},
		{function: "avg_over_time", aggregation: "avg", by: []string{"zone"}},
		{function: "min_over_time"},
		{function: "min_over_time", aggregation: "min", by: []string{"job"}},
	}

	q := queriers["4 executors"]
	source, err := q.instantWeekSource(projectID, "requests_total", end)
	require.NoError(t, err)
	request := cases[0].request(week, end, end, 0)
	plan, err := q.rollupPlan(projectID, request, 1, 0, source, "requests_total")
	require.NoError(t, err)
	require.NotNil(t, plan, "a week with rolled-up segments reads rollups")
	requireCovers(t, *plan, end.Add(-week).UnixMilli()+1, end.UnixMilli())
	rolled := map[uuid.UUID]bool{}
	var rollupWindow msWindow
	for _, planned := range plan.planned {
		require.True(t, planned.partial)
		for _, record := range planned.slice.Segments {
			if record.Stream == "metrics_1h" {
				rolled[record.ID] = true
				rollupWindow = planned.window
			}
		}
	}
	require.NotEmpty(t, rolled)
	hour := time.Hour.Milliseconds()
	require.Zero(t, rollupWindow.fromMs%hour)
	require.Zero(t, (rollupWindow.toMs+1)%hour)
	require.Less(t, rollupWindow.fromMs-(end.Add(-week).UnixMilli()+1), hour, "under an hour of samples at the start")

	check := func(variant string) {
		for _, shape := range cases {
			text := shape.promQL(week)
			want := instantVector(t, reference, text, end)
			require.NotEmpty(t, want, text)
			for name, q := range queriers {
				got := evaluateChunkCase(t, q, projectID, shape, week, end, end, 0)
				requireSameSamples(t, want, got, "%s, %s %s", variant, name, text)
			}
		}
	}
	check("samples present")
	removed := 0
	for _, record := range records {
		if record.MinTime.UnixMilli() >= rollupWindow.fromMs && record.MaxTime.UnixMilli() <= rollupWindow.toMs {
			require.NoError(t, os.Remove(record.Path))
			removed++
		}
	}
	require.Greater(t, removed, 5)
	check("rolled-up samples removed")

	// Not answered from rollups: more steps, max, rate, and a short window.
	for _, other := range []*metrics_core.RangeFunctionRequestDTO{
		cases[0].request(week, end.Add(-24*time.Hour), end, 24*time.Hour),
		chunkCase{function: "max_over_time"}.request(week, end, end, 0),
		chunkCase{function: "rate"}.request(week, end, end, 0),
		cases[0].request(30*time.Minute, end, end, 0),
	} {
		lastStep := int64(0)
		if other.StepMs > 0 {
			lastStep = (other.EndMs - other.StartMs) / other.StepMs
		}
		plan, err := q.rollupPlan(projectID, other, max(other.StepMs, 1), lastStep, source, "requests_total")
		require.NoError(t, err)
		require.Nil(t, plan, "%s over %d ms", other.Function, other.RangeMs)
	}
}

// Rollups stop at the first segment not rolled up: everything from its
// first hour on is read from samples, even if later segments have rollups.
func Test_RollupSpan_StopsAtASegmentWithoutRollup(t *testing.T) {
	hour := time.Hour.Milliseconds()
	base := int64(1_800_000_000_000) / hour * hour
	record := func(fromHour, toHour int64) *segments.SegmentRecord {
		return &segments.SegmentRecord{
			ID:      uuid.New(),
			MinTime: time.UnixMilli(base + fromHour*hour + 1000).UTC(),
			MaxTime: time.UnixMilli(base + toHour*hour - 1000).UTC(),
		}
	}
	rollupOf := func(record *segments.SegmentRecord) *segments.SegmentRecord {
		return &segments.SegmentRecord{ID: uuid.New(), Stats: &segments.SegmentStats{Source: record.ID.String()}}
	}
	a, b, c, d := record(0, 24), record(24, 48), record(48, 72), record(72, 96)
	source := sampleSource{records: []*segments.SegmentRecord{a, b, c, d}}
	from := base + 30*60_000

	first, end := rollupSpan(source, []*segments.SegmentRecord{rollupOf(a), rollupOf(b), rollupOf(d)},
		from, base+100*hour, hour)
	require.Equal(t, base+hour, first, "the first whole hour")
	require.Equal(t, base+48*hour, end, "c isn't rolled up")

	first, end = rollupSpan(source, []*segments.SegmentRecord{rollupOf(a), rollupOf(b), rollupOf(c), rollupOf(d)},
		from, base+90*hour+123, hour)
	require.Equal(t, base+hour, first)
	require.Equal(t, base+90*hour, end, "up to the last whole hour before the limit")
}

// seedRolledUpWeek writes 6 series named requests_total - 0/1 like an
// uptime, gauges, one with NaNs - every 5 minutes with millisecond jitter
// from end - week - 2h to end, into 15 half-day segments; all but the 2
// newest are cold and rolled up, and those 2 are left not uploaded.
func seedRolledUpWeek(t *testing.T, end time.Time) (uuid.UUID, *chunkReference, []*segments.SegmentRecord) {
	t.Helper()
	random := rand.New(rand.NewSource(7))
	reference := &chunkReference{}
	projectID := uuid.New()
	begin := end.Add(-week - 2*time.Hour)
	const files = 15
	batches := make([][]*metrics_core.MetricSample, files)
	for at := begin; !at.After(end); at = at.Add(5 * time.Minute) {
		for i := range 6 {
			labelSet := map[string]string{"job": []string{"api", "web"}[i%2], "instance": fmt.Sprintf("i%d", i)}
			if i%3 != 0 {
				labelSet["zone"] = []string{"a", "b"}[i%2]
			}
			sampledAt := at.Add(-time.Duration(random.Intn(4000)) * time.Millisecond)
			value := 1.0
			switch {
			case i < 3 && random.Intn(20) == 0:
				value = 0
			case i >= 3:
				value = random.Float64() * 1000
				if i == 5 && random.Intn(50) == 0 {
					value = math.NaN()
				}
			}
			reference.add(labelSet, sampledAt.UnixMilli(), value)
			file := min(int(sampledAt.Sub(begin)*files/(end.Sub(begin)+time.Millisecond)), files-1)
			batches[file] = append(batches[file], chunkSample("requests_total", labelSet, sampledAt, value))
		}
	}
	records := storeSegments(t, projectID, batches)
	markUploaded(t, records[:len(records)-2])
	cold := make([]uuid.UUID, 0, len(records)-2)
	for _, record := range records[:len(records)-2] {
		cold = append(cold, record.ID)
	}
	repo := &segments.SegmentRepository{}
	require.NoError(t, repo.SetLevel(cold, segments.LevelCold))
	compactor := newTestMetricCompactor(t)
	for range len(cold) {
		require.NoError(t, compactor.RollupColdSegments(projectID))
	}
	require.Equal(t, len(cold), countSegments(t, repo, projectID, "metrics_1h"))
	return projectID, reference, records
}
