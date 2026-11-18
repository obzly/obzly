package metrics_querier

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	metrics_core "obzly/internal/features/metrics/core"
	metrics_ingester "obzly/internal/features/metrics/ingester"
	"obzly/internal/query"
	"obzly/internal/storage/segments"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"github.com/prometheus/prometheus/tsdb/chunks"
	"github.com/prometheus/prometheus/util/annotations"
	"github.com/stretchr/testify/require"
)

const week = 7 * 24 * time.Hour

// chunkCase is one pushed-down shape over metric (requests_total when
// empty), as PromQL text and as a request.
type chunkCase struct {
	function, aggregation string
	by                    []string
	metric                string
}

// chunkReference holds every sample in memory, for the engine.
type chunkReference struct {
	series map[string]*chunkSeries
}

type chunkSeries struct {
	labels  labels.Labels
	samples []chunks.Sample
}

type chunkPoint struct {
	t int64
	f float64
}

type chunkQuerier struct {
	reference  *chunkReference
	mint, maxt int64
}

type chunkSet struct {
	series []storage.Series
	at     int
}

// referenceEngine evaluates the reference storage the way Obzly's engine
// does (left-open range windows, 5 m lookback).
var referenceEngine = promql.NewEngine(promql.EngineOpts{
	MaxSamples: 50_000_000, Timeout: 2 * time.Minute, LookbackDelta: 5 * time.Minute,
})

// An instant query (and a few steps over long windows) with spare executors
// is cut into time chunks, one per executor, disjoint at the millisecond,
// covering the whole window and cut where segments end; with a step per
// executor it isn't.
func Test_PushdownPlan_InstantWeekWithExecutors_CutsTheWindowInTime(t *testing.T) {
	q := newParityQuerier(t, executorsN(4))
	end := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	source := dailySegments(end)
	request := planRequest(end, end, 0)

	plan, err := q.pushdownPlan(uuid.New(), request, 1, 0, source)
	require.NoError(t, err)
	require.Empty(t, plan.slices(false))
	require.Len(t, plan.slices(true), 4)
	requireCovers(t, plan, end.UnixMilli()-week.Milliseconds()+1, end.UnixMilli())
	edges := fileEdges(source, 0, math.MaxInt64)
	for i, planned := range plan.planned {
		require.NotEmpty(t, planned.slice.Segments)
		require.Less(t, len(planned.slice.Segments), len(source.records), "chunk %d reads only its files", i)
		require.NotContains(t, planned.slice.SQL, "merged", "chunk %d returns partials", i)
		if i > 0 {
			require.Contains(t, edges, planned.window.fromMs, "chunk %d starts where a segment does", i)
			require.LessOrEqual(t, len(planned.slice.Segments), 3, "chunk %d reads whole days", i)
		}
	}

	request.StartMs, request.StepMs = end.Add(-3*time.Hour).UnixMilli(), time.Hour.Milliseconds()
	plan, err = q.pushdownPlan(uuid.New(), request, request.StepMs, 3, source)
	require.NoError(t, err)
	require.Empty(t, plan.slices(true), "a step per executor computes whole slices")
	require.Len(t, plan.slices(false), 4)
}

// With the query-time tail and a segment not uploaded yet, an instant week
// reads in process only from the earliest of them on; everything before
// is cut over the remote executors, which read only uploaded segments.
func Test_PushdownPlan_InstantWeekWithLocalFiles_ReadsOnlyTheRecentPartInProcess(t *testing.T) {
	q := newParityQuerier(t, executorsN(4))
	end := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	source := withLocalFiles(dailySegments(end), end)
	recent := end.Add(-30 * time.Minute).UnixMilli()

	plan, err := q.pushdownPlan(uuid.New(), planRequest(end, end, 0), 1, 0, source)
	require.NoError(t, err)
	require.Empty(t, plan.slices(false))
	requireCovers(t, plan, end.UnixMilli()-week.Milliseconds()+1, end.UnixMilli())
	local, remote := splitByPlacement(plan)
	require.Equal(t, []msWindow{{recent, end.UnixMilli()}}, local)
	require.Len(t, remote, 4)
	require.Equal(t, recent-1, remote[3].toMs)

	q = newParityQuerier(t, nil)
	plan, err = q.pushdownPlan(uuid.New(), planRequest(end, end, 0), 1, 0, source)
	require.NoError(t, err)
	require.Len(t, plan.slices(false), 1, "without remote executors: one whole slice, as before")
}

// A week of 12600 s steps with local files: only the step at or after the
// recent boundary reads in process, and only from the boundary on; the
// steps before it are whole slices on the remote executors.
func Test_PushdownPlan_RangeWeekWithLocalFiles_KeepsOnlyTheRecentStepInProcess(t *testing.T) {
	q := newParityQuerier(t, executorsN(4))
	end := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	source := withLocalFiles(dailySegments(end), end)
	step := 12600 * time.Second
	start := end.Add(-47 * step)
	request := planRequest(start, end, step)
	request.RangeMs = step.Milliseconds()
	recent := end.Add(-30 * time.Minute).UnixMilli()

	plan, err := q.pushdownPlan(uuid.New(), request, step.Milliseconds(), 47, source)
	require.NoError(t, err)
	require.Len(t, plan.slices(false), 4)
	for _, slice := range plan.slices(false) {
		require.False(t, inProcess(slice), "steps before the boundary read no local file")
	}
	local, remote := splitByPlacement(plan)
	require.Equal(t, []msWindow{{recent, end.UnixMilli()}}, local)
	var chunks []msWindow
	for _, planned := range plan.planned {
		if planned.partial {
			chunks = append(chunks, planned.window)
		}
	}
	require.Equal(t, end.UnixMilli()-step.Milliseconds()+1, chunks[0].fromMs)
	require.Equal(t, end.UnixMilli(), chunks[len(chunks)-1].toMs)
	for i := 1; i < len(chunks); i++ {
		require.Equal(t, chunks[i-1].toMs+1, chunks[i].fromMs)
	}
	require.Len(t, remote, 4+len(chunks)-1)
}

// Time-chunked push-down answers what the Prometheus engine does over the
// same samples, for every function and aggregation: instant queries whose
// week (or 3 day) window spans many files, files that straddle chunk cuts,
// a counter reset and a NaN exactly at a cut and at the recent boundary,
// and 2 steps over 5 executors (2 chunks per step). One process, many
// executors, or one; with the newest segments not uploaded (read in
// process from the recent boundary on) and with all uploaded: the same.
func Test_EvaluateRangeFunction_TimeChunks_MatchTheEngine(t *testing.T) {
	end := time.Now().UTC().Truncate(time.Minute)
	projectID, reference, local := seedChunkedWeek(t, end)
	queriers := map[string]*MetricQuerier{
		"one executor":    newParityQuerier(t, nil),
		"3 executors":     newParityQuerier(t, executorsN(3)),
		"5 executors":     newParityQuerier(t, executorsN(5)),
		"8 executors":     newParityQuerier(t, executorsN(8)),
		"spare executors": newParityQuerier(t, executorsN(7)),
	}
	var cases []chunkCase
	for _, function := range []string{
		"sum_over_time", "count_over_time", "avg_over_time", "min_over_time", "max_over_time", "rate", "increase",
	} {
		cases = append(cases,
			chunkCase{function: function},
			chunkCase{function: function, aggregation: "sum", by: []string{"job"}},
		)
	}
	cases = append(cases,
		chunkCase{function: "avg_over_time", aggregation: "avg", by: []string{"job", "zone"}},
		chunkCase{function: "min_over_time", aggregation: "min", by: []string{"zone"}},
		chunkCase{function: "max_over_time", aggregation: "max"},
		chunkCase{function: "count_over_time", aggregation: "count", by: []string{"job"}},
		chunkCase{function: "rate", aggregation: "max", by: []string{"job"}},
	)

	check := func(variant string) {
		for _, window := range []time.Duration{week, 3 * 24 * time.Hour} {
			for _, shape := range cases {
				text := shape.promQL(window)
				instant := instantVector(t, reference, text, end)
				require.NotEmpty(t, instant, text)
				twoSteps := rangeMatrix(t, reference, text, end.Add(-24*time.Hour), end, 24*time.Hour)
				for name, q := range queriers {
					got := evaluateChunkCase(t, q, projectID, shape, window, end, end, 0)
					requireSameSamples(t, instant, got, "%s, %s instant %s", variant, name, text)
					got = evaluateChunkCase(t, q, projectID, shape, window, end.Add(-24*time.Hour), end, 24*time.Hour)
					requireSameSamples(t, twoSteps, got, "%s, %s 2 steps %s", variant, name, text)
				}
			}
		}
	}
	check("newest segments local")
	markUploaded(t, local)
	check("all uploaded")
}

// Two samples in one millisecond can't be ordered: chunked or not, storage
// declines (the engine then reads the samples).
func Test_EvaluateRangeFunction_TimeChunks_DuplicateTimestamp_Declines(t *testing.T) {
	end := time.Now().UTC().Truncate(time.Minute)
	var batch []*metrics_core.MetricSample
	for at := end.Add(-48 * time.Hour); !at.After(end); at = at.Add(10 * time.Minute) {
		batch = append(batch, chunkSample("requests_total", map[string]string{"job": "api"}, at, float64(at.Unix())))
	}
	twice := end.Add(-30*time.Hour + 1234*time.Millisecond)
	batch = append(batch,
		chunkSample("requests_total", map[string]string{"job": "api"}, twice, 1),
		chunkSample("requests_total", map[string]string{"job": "api"}, twice, 2))
	projectID := uuid.New()
	markUploaded(t, storeSegments(t, projectID, [][]*metrics_core.MetricSample{batch[:len(batch)/2], batch[len(batch)/2:]}))
	for _, executors := range []int{0, 3} {
		q := newParityQuerier(t, executorsN(executors))
		for _, function := range []string{"sum_over_time", "rate"} {
			request := chunkCase{function: function, aggregation: "sum"}.request(week, end, end, 0)
			_, ok, err := q.EvaluateRangeFunction(context.Background(), projectID, request)
			require.NoError(t, err)
			require.False(t, ok, "%d executors, %s", executors, function)
		}
	}
}

// Timing of the Synthetics page's 7-day uptime ratio over 70 checks x 3
// locations, a sample a minute (210 x 10080), in 28 segments: one slice (as
// before), then cut over N executors, each half alone and both at once, and
// the raw sample read the engine path does instead. Skipped unless
// OBZLY_METRICS_BENCH_WEEK is set (-run Benchmark_InstantWeek -v).
func Test_Benchmark_InstantWeekUptime(t *testing.T) {
	if os.Getenv("OBZLY_METRICS_BENCH_WEEK") == "" {
		t.Skip("set OBZLY_METRICS_BENCH_WEEK to run")
	}
	end := time.Now().UTC().Truncate(time.Minute)
	random := rand.New(rand.NewSource(3))
	batches := make([][]*metrics_core.MetricSample, 28)
	began := time.Now()
	for minute := range 10080 {
		at := end.Add(-week + time.Duration(minute)*time.Minute)
		segment := minute * len(batches) / 10080
		for check := range 70 {
			for location := range 3 {
				value := 1.0
				if random.Intn(50) == 0 {
					value = 0
				}
				batches[segment] = append(batches[segment], chunkSample("synthetics_up", map[string]string{
					"check_id": fmt.Sprintf("check-%02d", check), "location": fmt.Sprintf("loc-%d", location),
				}, at.Add(time.Duration(random.Intn(20_000))*time.Millisecond), value))
			}
		}
	}
	projectID := uuid.New()
	records := storeSegments(t, projectID, batches)
	// The newest segment not uploaded yet, as on a live Site.
	markUploaded(t, records[:len(records)-1])
	t.Logf("seeded 210 series x 10080 samples in 28 segments in %s", time.Since(began).Round(time.Second))

	halves := []chunkCase{
		{function: "sum_over_time", aggregation: "sum", by: []string{"check_id"}, metric: "synthetics_up"},
		{function: "count_over_time", aggregation: "sum", by: []string{"check_id"}, metric: "synthetics_up"},
	}
	measure := func(label string, run func()) {
		run() // warm the page cache and DuckDB
		var timings []time.Duration
		for range 5 {
			started := time.Now()
			run()
			timings = append(timings, time.Since(started))
		}
		sort.Slice(timings, func(i, j int) bool { return timings[i] < timings[j] })
		t.Logf("%-44s p50 %6s  max %6s", label, timings[2].Round(time.Millisecond), timings[4].Round(time.Millisecond))
	}
	for _, executors := range []int{0, 2, 4, 8} {
		q := newParityQuerier(t, executorsN(executors))
		evaluate := func(shape chunkCase) {
			response, ok, err := q.EvaluateRangeFunction(context.Background(), projectID,
				shape.request(week, end, end, 0))
			require.NoError(t, err)
			require.True(t, ok)
			require.Len(t, response.Series, 70)
		}
		measure(fmt.Sprintf("%d executors, sum half", executors), func() { evaluate(halves[0]) })
		measure(fmt.Sprintf("%d executors, both halves one after another", executors), func() {
			evaluate(halves[0])
			evaluate(halves[1])
		})
		measure(fmt.Sprintf("%d executors, both halves at once", executors), func() {
			var wg sync.WaitGroup
			for _, shape := range halves {
				wg.Add(1)
				go func() {
					defer wg.Done()
					evaluate(shape)
				}()
			}
			wg.Wait()
		})
	}
	for _, executors := range []int{0, 4} {
		q := newParityQuerier(t, executorsN(executors))
		source, err := q.instantWeekSource(projectID, "synthetics_up", end)
		require.NoError(t, err)
		request := halves[0].request(week, end, end, 0)
		plan, err := q.pushdownPlan(projectID, request, 1, 0, source)
		require.NoError(t, err)
		var reads []string
		for _, planned := range plan.planned {
			var bytes int64
			for _, record := range planned.slice.Segments {
				bytes += record.SizeBytes
			}
			where := "remote"
			if executors == 0 || inProcess(planned.slice) {
				where = "in process"
			}
			reads = append(reads, fmt.Sprintf("%s %d segments/%d KiB", where, len(planned.slice.Segments), bytes/1024))
		}
		t.Logf("%d executors: %d fragments: %s", executors, len(plan.planned), strings.Join(reads, ", "))
	}

	// The engine path reads every sample into the API process: past the raw
	// sample cap (2M) it is refused; lifted here to time the read alone.
	q := newParityQuerier(t, executorsN(4))
	_, err := q.SelectSeries(context.Background(), projectID, weekSelect(end))
	require.ErrorContains(t, err, "raw samples")
	defer func(previous int) { maxSeriesRows = previous }(maxSeriesRows)
	maxSeriesRows = 10_000_000
	measure("engine path: read every sample (4 executors)", func() {
		response, err := q.SelectSeries(context.Background(), projectID, weekSelect(end))
		require.NoError(t, err)
		require.Len(t, response.Series, 210)
	})
}

func (c chunkCase) promQL(window time.Duration) string {
	text := fmt.Sprintf("%s(%s[%ds])", c.function, c.metricName(), int64(window.Seconds()))
	if c.aggregation != "" {
		text = fmt.Sprintf("%s by (%s) (%s)", c.aggregation, strings.Join(c.by, ", "), text)
	}
	return text
}

func (c chunkCase) request(window time.Duration, start, end time.Time, step time.Duration) *metrics_core.RangeFunctionRequestDTO {
	return &metrics_core.RangeFunctionRequestDTO{
		Matchers: []metrics_core.MatcherDTO{
			{Name: "__name__", Type: metrics_core.MatchEqual, Value: c.metricName()},
		},
		Function: c.function, RangeMs: window.Milliseconds(),
		StartMs: start.UnixMilli(), EndMs: end.UnixMilli(), StepMs: step.Milliseconds(),
		Aggregation: c.aggregation, By: c.by,
	}
}

func (c chunkCase) metricName() string {
	if c.metric == "" {
		return "requests_total"
	}
	return c.metric
}

// seedChunkedWeek writes 6 counters (random resets, one that stops after 3
// days) and 2 NaN-bearing gauges named requests_total too, every 5 minutes
// with millisecond jitter from end - week - 1h to end, into 16 segments
// whose time ranges overlap their neighbours'; the 2 newest are left not
// uploaded (local). Then, at every cut an instant week over 3 executors
// makes - with those 2 local and with all uploaded, the recent boundary
// included - a counter resets on the cut's first millisecond and a gauge
// is NaN just before it, written to one more segment spanning the whole
// window so the cuts stay where they were.
func seedChunkedWeek(t *testing.T, end time.Time) (uuid.UUID, *chunkReference, []*segments.SegmentRecord) {
	t.Helper()
	random := rand.New(rand.NewSource(11))
	reference := &chunkReference{}
	projectID := uuid.New()
	begin := end.Add(-week - time.Hour)
	const files = 16
	batches := make([][]*metrics_core.MetricSample, files)
	values := make([]float64, 10)
	taken := map[string]bool{}
	sample := func(i int, at time.Time, value float64) *metrics_core.MetricSample {
		labelSet := map[string]string{"job": []string{"api", "web"}[i%2], "instance": fmt.Sprintf("i%d", i)}
		if i%3 != 0 {
			labelSet["zone"] = []string{"a", "b"}[i%2]
		}
		reference.add(labelSet, at.UnixMilli(), value)
		taken[fmt.Sprintf("%d/%d", i, at.UnixMilli())] = true
		return chunkSample("requests_total", labelSet, at, value)
	}
	for i := range values {
		values[i] = random.Float64() * 100
	}
	for at := begin; !at.After(end); at = at.Add(5 * time.Minute) {
		for i := range 8 {
			if i == 5 && at.After(begin.Add(72*time.Hour)) {
				continue
			}
			sampledAt := at
			if !at.Equal(begin) && !at.Equal(end) {
				sampledAt = at.Add(-time.Duration(random.Intn(4000)) * time.Millisecond)
			}
			switch {
			case i >= 6:
				values[i] = 1e6 + random.Float64()*1e5
				if random.Intn(100) == 0 {
					values[i] = math.NaN()
				}
			case random.Intn(300) == 0:
				values[i] = random.Float64()
			default:
				values[i] += random.Float64() * 10
			}
			file := min(int(sampledAt.Sub(begin)*files/(end.Sub(begin)+time.Millisecond)), files-1)
			if spill := file + random.Intn(3) - 1; spill >= 0 && spill < files {
				file = spill
			}
			batches[file] = append(batches[file], sample(i, sampledAt, values[i]))
		}
	}
	records := storeSegments(t, projectID, batches)
	sort.Slice(records, func(i, j int) bool { return records[i].MinTime.Before(records[j].MinTime) })
	local := records[len(records)-2:]
	markUploaded(t, records[:len(records)-2])

	q := newParityQuerier(t, executorsN(3))
	source, err := q.instantWeekSource(projectID, "requests_total", end)
	require.NoError(t, err)
	uploaded := source
	uploaded.records = nil
	for _, record := range source.records {
		copied := *record
		copied.ObjectKey = "metrics/" + record.ID.String() + ".parquet"
		uploaded.records = append(uploaded.records, &copied)
	}
	var cuts []int64
	for _, variant := range []sampleSource{source, uploaded} {
		plan, err := q.pushdownPlan(projectID, planRequest(end, end, 0), 1, 0, variant)
		require.NoError(t, err)
		require.Greater(t, len(plan.planned), 2)
		for _, planned := range plan.planned[1:] {
			cuts = append(cuts, planned.window.fromMs)
		}
	}
	extra := []*metrics_core.MetricSample{sample(9, begin, 1), sample(9, end, 2)}
	for _, cut := range cuts {
		for _, boundary := range []struct {
			series        int
			before, after float64
		}{{0, 1e9, 3}, {6, math.NaN(), 5}} {
			if taken[fmt.Sprintf("%d/%d", boundary.series, cut-1)] || taken[fmt.Sprintf("%d/%d", boundary.series, cut)] {
				continue
			}
			extra = append(extra,
				sample(boundary.series, time.UnixMilli(cut-1).UTC(), boundary.before),
				sample(boundary.series, time.UnixMilli(cut).UTC(), boundary.after))
		}
	}
	markUploaded(t, storeSegments(t, projectID, [][]*metrics_core.MetricSample{extra}))
	return projectID, reference, local
}

// storeSegments writes each batch as its own segment of projectID and
// returns them, not uploaded yet.
func storeSegments(
	t *testing.T, projectID uuid.UUID, batches [][]*metrics_core.MetricSample,
) []*segments.SegmentRecord {
	t.Helper()
	repo := &segments.SegmentRepository{}
	before, err := repo.ListByProject(projectID, "metrics")
	require.NoError(t, err)
	known := map[uuid.UUID]bool{}
	for _, record := range before {
		known[record.ID] = true
	}
	ingester, err := metrics_ingester.NewMetricIngester(t.TempDir(), segments.WithMaxRows(10_000_000))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ingester.Close() })
	stored := 0
	for _, batch := range batches {
		if len(batch) == 0 {
			continue
		}
		for _, sample := range batch {
			sample.ProjectID = projectID
		}
		require.NoError(t, ingester.StoreSamplesBatch(map[uuid.UUID][]*metrics_core.MetricSample{projectID: batch}))
		_, err = ingester.FlushAll()
		require.NoError(t, err)
		stored++
	}
	after, err := repo.ListByProject(projectID, "metrics")
	require.NoError(t, err)
	var records []*segments.SegmentRecord
	for _, record := range after {
		if !known[record.ID] {
			records = append(records, record)
		}
	}
	require.Len(t, records, stored)
	sort.Slice(records, func(i, j int) bool { return records[i].MinTime.Before(records[j].MinTime) })
	return records
}

// markUploaded marks records uploaded, so chunks reading only them go to
// the stand-in remote executors.
func markUploaded(t *testing.T, records []*segments.SegmentRecord) {
	t.Helper()
	// Hold the lock the uploader tests take before resetting every uploaded
	// metrics segment.
	test_utils.AcquireExclusiveGlobalQueue(t, "metrics-uploaded-queue")
	repo := &segments.SegmentRepository{}
	for _, record := range records {
		require.NoError(t, repo.MarkUploaded(record.ID, "metrics/"+record.ID.String()+".parquet"))
	}
}

// dailySegments: 8 uploaded day-long segments from end - week - 1h.
func dailySegments(end time.Time) sampleSource {
	source := sampleSource{stream: "metrics", from: query.SourcePlaceholder}
	for day := range 8 {
		begin := end.Add(-week - time.Hour + time.Duration(day)*24*time.Hour)
		source.records = append(source.records, &segments.SegmentRecord{
			ID: uuid.New(), MinTime: begin, MaxTime: begin.Add(24*time.Hour - time.Millisecond),
			ObjectKey: "metrics/x.parquet", SizeBytes: 100 << 20,
		})
	}
	return source
}

// withLocalFiles adds a segment not uploaded yet (from end - 30m) and a
// query-time tail file (from end - 10m).
func withLocalFiles(source sampleSource, end time.Time) sampleSource {
	source.records = append(source.records, &segments.SegmentRecord{
		ID: uuid.New(), MinTime: end.Add(-30 * time.Minute), MaxTime: end.Add(-5 * time.Minute),
	})
	source.tail = []segments.ResolvedFile{{
		Path: "tail.parquet", MinTime: end.Add(-10 * time.Minute), MaxTime: end,
	}}
	return source
}

func planRequest(start, end time.Time, step time.Duration) *metrics_core.RangeFunctionRequestDTO {
	return &metrics_core.RangeFunctionRequestDTO{
		Matchers: []metrics_core.MatcherDTO{{Name: "__name__", Type: metrics_core.MatchEqual, Value: "up"}},
		Function: "sum_over_time", RangeMs: week.Milliseconds(),
		StartMs: start.UnixMilli(), EndMs: end.UnixMilli(), StepMs: step.Milliseconds(),
		Aggregation: "sum", By: []string{"check_id"},
	}
}

// inProcess: the coordinator runs this slice itself (query.planSlices).
func inProcess(slice query.Slice) bool {
	if len(slice.LocalFiles) > 0 {
		return true
	}
	for _, record := range slice.Segments {
		if record.ObjectKey == "" {
			return true
		}
	}
	return false
}

// splitByPlacement is the windows of the plan's in-process and remote
// slices.
func splitByPlacement(plan pushdownPlan) ([]msWindow, []msWindow) {
	var local, remote []msWindow
	for _, planned := range plan.planned {
		if inProcess(planned.slice) {
			local = append(local, planned.window)
		} else {
			remote = append(remote, planned.window)
		}
	}
	return local, remote
}

// requireCovers: the plan's windows, in order, share no millisecond and
// cover [fromMs, toMs].
func requireCovers(t *testing.T, plan pushdownPlan, fromMs, toMs int64) {
	t.Helper()
	require.NotEmpty(t, plan.planned)
	require.Equal(t, fromMs, plan.planned[0].window.fromMs)
	require.Equal(t, toMs, plan.planned[len(plan.planned)-1].window.toMs)
	for i := 1; i < len(plan.planned); i++ {
		require.Equal(t, plan.planned[i-1].window.toMs+1, plan.planned[i].window.fromMs, "window %d", i)
	}
}

func chunkSample(name string, labelSet map[string]string, at time.Time, value float64) *metrics_core.MetricSample {
	return &metrics_core.MetricSample{
		ID: uuid.New(), MetricName: name, Labels: labelSet, Value: value, Timestamp: at,
		Type: metrics_core.MetricTypeCounter,
	}
}

// executorsN: n local executors standing in for remote ones (none for 0).
func executorsN(n int) func(*MetricQuerier) []query.Executor {
	if n == 0 {
		return nil
	}
	return func(q *MetricQuerier) []query.Executor {
		var executors []query.Executor
		for i := range n {
			executors = append(executors, query.NewLocalExecutor(fmt.Sprintf("e%d", i), q.db, nil, ""))
		}
		return executors
	}
}

func evaluateChunkCase(
	t *testing.T, q *MetricQuerier, projectID uuid.UUID, shape chunkCase,
	window time.Duration, start, end time.Time, step time.Duration,
) map[string][]promql.FPoint {
	t.Helper()
	response, ok, err := q.EvaluateRangeFunction(context.Background(), projectID,
		shape.request(window, start, end, step))
	require.NoError(t, err)
	require.True(t, ok, "%s declined", shape.promQL(window))
	got := map[string][]promql.FPoint{}
	for _, series := range response.Series {
		key := labels.FromMap(series.Labels).String()
		for _, point := range series.Points {
			got[key] = append(got[key], promql.FPoint{T: point.TimestampMs, F: point.Value})
		}
	}
	return got
}

func instantVector(t *testing.T, reference *chunkReference, text string, at time.Time) map[string][]promql.FPoint {
	t.Helper()
	q, err := referenceEngine.NewInstantQuery(context.Background(), reference, nil, text, at)
	require.NoError(t, err)
	defer q.Close()
	result := q.Exec(context.Background())
	require.NoError(t, result.Err)
	vector, err := result.Vector()
	require.NoError(t, err)
	want := map[string][]promql.FPoint{}
	for _, sample := range vector {
		want[sample.Metric.String()] = []promql.FPoint{{T: sample.T, F: sample.F}}
	}
	return want
}

func rangeMatrix(
	t *testing.T, reference *chunkReference, text string, start, end time.Time, step time.Duration,
) map[string][]promql.FPoint {
	t.Helper()
	q, err := referenceEngine.NewRangeQuery(context.Background(), reference, nil, text, start, end, step)
	require.NoError(t, err)
	defer q.Close()
	result := q.Exec(context.Background())
	require.NoError(t, result.Err)
	matrix, err := result.Matrix()
	require.NoError(t, err)
	want := map[string][]promql.FPoint{}
	for _, series := range matrix {
		want[series.Metric.String()] = series.Floats
	}
	return want
}

// requireSameSamples: the same series and timestamps, values equal to float
// rounding (push-down adds in another order) or both NaN.
func requireSameSamples(t *testing.T, want, got map[string][]promql.FPoint, format string, args ...any) {
	t.Helper()
	message := fmt.Sprintf(format, args...)
	require.Len(t, got, len(want), message)
	for key, points := range want {
		other, ok := got[key]
		require.True(t, ok, "%s: missing %s", message, key)
		require.Len(t, other, len(points), "%s: %s", message, key)
		for i, point := range points {
			require.Equal(t, point.T, other[i].T, "%s: %s", message, key)
			if math.IsNaN(point.F) {
				require.True(t, math.IsNaN(other[i].F), "%s: %s at %d is %v", message, key, point.T, other[i].F)
				continue
			}
			require.InEpsilon(t, point.F+1e-300, other[i].F+1e-300, 1e-9, "%s: %s at %d", message, key, point.T)
		}
	}
}

func weekSelect(end time.Time) *metrics_core.SelectSeriesRequestDTO {
	from := end.Add(-week)
	return &metrics_core.SelectSeriesRequestDTO{
		Matchers:  []metrics_core.MatcherDTO{{Name: "__name__", Type: metrics_core.MatchEqual, Value: "synthetics_up"}},
		TimeRange: &metrics_core.TimeRangeDTO{From: &from, To: &end},
	}
}

// instantWeekSource is what EvaluateRangeFunction reads for an instant
// week query of name at end.
func (q *MetricQuerier) instantWeekSource(projectID uuid.UUID, name string, end time.Time) (sampleSource, error) {
	from := end.Add(-week + time.Millisecond)
	tail, records, err := q.inputs(projectID, from, end, name)
	if err != nil {
		return sampleSource{}, err
	}
	sources, err := q.sampleSources(projectID, from, end, records, tail, 0, week.Milliseconds(),
		segments.PruningHints{MetricNames: []string{name}})
	if err != nil {
		return sampleSource{}, err
	}
	return sources[0], nil
}

func (r *chunkReference) add(labelSet map[string]string, t int64, f float64) {
	if r.series == nil {
		r.series = map[string]*chunkSeries{}
	}
	withName := map[string]string{"__name__": "requests_total"}
	for key, value := range labelSet {
		withName[key] = value
	}
	lbls := labels.FromMap(withName)
	series, ok := r.series[lbls.String()]
	if !ok {
		series = &chunkSeries{labels: lbls}
		r.series[lbls.String()] = series
	}
	series.samples = append(series.samples, chunkPoint{t, f})
}

func (r *chunkReference) Querier(mint, maxt int64) (storage.Querier, error) {
	return &chunkQuerier{reference: r, mint: mint, maxt: maxt}, nil
}

func (q *chunkQuerier) Select(
	_ context.Context, _ bool, _ *storage.SelectHints, matchers ...*labels.Matcher,
) storage.SeriesSet {
	var selected []storage.Series
	for _, series := range q.reference.series {
		matches := true
		for _, matcher := range matchers {
			matches = matches && matcher.Matches(series.labels.Get(matcher.Name))
		}
		if !matches {
			continue
		}
		samples := append([]chunks.Sample(nil), series.samples...)
		sort.Slice(samples, func(i, j int) bool { return samples[i].T() < samples[j].T() })
		var inRange []chunks.Sample
		for _, sample := range samples {
			if sample.T() >= q.mint && sample.T() <= q.maxt {
				inRange = append(inRange, sample)
			}
		}
		if len(inRange) > 0 {
			selected = append(selected, storage.NewListSeries(series.labels, inRange))
		}
	}
	sort.Slice(selected, func(i, j int) bool { return labels.Compare(selected[i].Labels(), selected[j].Labels()) < 0 })
	return &chunkSet{series: selected, at: -1}
}

func (q *chunkQuerier) LabelValues(
	context.Context, string, *storage.LabelHints, ...*labels.Matcher,
) ([]string, annotations.Annotations, error) {
	return nil, nil, nil
}

func (q *chunkQuerier) LabelNames(
	context.Context, *storage.LabelHints, ...*labels.Matcher,
) ([]string, annotations.Annotations, error) {
	return nil, nil, nil
}

func (q *chunkQuerier) Close() error { return nil }

func (s *chunkSet) Next() bool                        { s.at++; return s.at < len(s.series) }
func (s *chunkSet) At() storage.Series                { return s.series[s.at] }
func (s *chunkSet) Err() error                        { return nil }
func (s *chunkSet) Warnings() annotations.Annotations { return nil }

func (p chunkPoint) T() int64                      { return p.t }
func (p chunkPoint) F() float64                    { return p.f }
func (p chunkPoint) H() *histogram.Histogram       { return nil }
func (p chunkPoint) FH() *histogram.FloatHistogram { return nil }
func (p chunkPoint) Type() chunkenc.ValueType      { return chunkenc.ValFloat }
func (p chunkPoint) Copy() chunks.Sample           { return p }
func (p chunkPoint) ST() int64                     { return 0 }
