package metrics_promql_test

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"os"
	"testing"
	"time"

	metrics_compactor "obzly/internal/features/metrics/compactor"
	metrics_core "obzly/internal/features/metrics/core"
	metrics_promql "obzly/internal/features/metrics/promql"
	"obzly/internal/storage/segments"
	"obzly/internal/telemetry"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/prometheus/prometheus/model/value"
	"github.com/stretchr/testify/require"
)

// partialsQueries are pushed-down shapes at ranges of whole minutes, so
// aligned steps read partials.
var partialsQueries = []string{
	`rate(http_requests_total[5m])`,
	`sum by (job) (rate(http_requests_total[10m]))`,
	`sum(increase(http_requests_total[15m]))`,
	`max by (job) (max_over_time(mem_bytes[10m]))`,
	`min by (zone) (min_over_time(mem_bytes[5m]))`,
	`avg by (job) (avg_over_time(mem_bytes[15m]))`,
	`count by (job) (count_over_time(mem_bytes{zone=~"a|b"}[5m]))`,
	`sum by (instance) (sum_over_time(mem_bytes[10m]))`,
	`sum by (job) (mem_bytes)`,
	`count by (code) (http_requests_total{job=~".*"})`,
	`max by (zone) (mem_bytes)`,
	`histogram_quantile(0.9, sum by (le) (rate(latency_bucket[5m])))`,
	// Ranges of whole minutes only: the 1-minute partials.
	`sum by (job) (rate(http_requests_total[2m]))`,
	`max by (zone) (max_over_time(mem_bytes[3m]))`,
	`sum(increase(http_requests_total[7m]))`,
}

// Aligned steps answered from hour partials and samples (for the hours
// after them) equal the engine over every sample: counter resets inside
// and across buckets, series that come and go, NaN gauges, stale markers,
// samples stored twice, and one series' samples interleaved across two
// segments (a reset
// between them is only visible to a fold of both). The folded segments'
// sample files are removed, so only the partials can answer for them.
func Test_PromQL_AlignedSteps_FromPartials_MatchAReferenceStorage(t *testing.T) {
	end := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)
	start := end.Add(-3 * time.Hour)
	mid := start.Add(2 * time.Hour)
	reference := &referenceQueryable{}
	projectID := uuid.New()
	random := rand.New(rand.NewSource(7))
	series := partialsSeries()

	seedPartialsSamples(t, reference, projectID, series, random, start, mid, 2)
	repository := &segments.SegmentRepository{}
	records, err := repository.ListByProject(projectID, "metrics")
	require.NoError(t, err)
	require.Len(t, records, 2)
	compactor, err := metrics_compactor.NewMetricCompactor(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = compactor.Close() })
	require.NoError(t, compactor.BuildPartials(projectID))
	partials, err := repository.ListByProject(projectID, segments.MetricPartialsStream)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(partials), 2, "one per hour")
	for _, record := range records {
		require.NoError(t, os.Remove(record.Path))
	}
	seedPartialsSamples(t, reference, projectID, series, random, mid.Add(time.Second), end, 1)

	pushed := func() float64 { return testutil.ToFloat64(telemetry.PromQLPushdown.WithLabelValues("pushed")) }
	fallbacks := func() float64 { return testutil.ToFloat64(telemetry.PromQLPushdown.WithLabelValues("fallback")) }
	engine := metrics_promql.GetEngine()
	pushedBefore, fallbacksBefore := pushed(), fallbacks()
	for _, q := range partialsQueries {
		// 1 m steps read 1-minute partials (window frames: ranges longer
		// than the step), 5 m and 15 m the 5-minute ones where they align.
		for _, step := range []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute} {
			from, to := start.Add(20*time.Minute), end
			want := rangeResult(t, engine, reference, q, from, to, step)
			require.NotEqual(t, "", canonical(t, want), "the reference answers %s", q)
			query, err := metrics_promql.NewRangeQuery(context.Background(), projectID, q, from, to, step)
			require.NoError(t, err)
			requireClose(t, want, query.Exec(context.Background()), "range %s step %s", q, step)
			query.Close()
		}
		for _, at := range []time.Time{start.Add(time.Hour), mid.Add(5 * time.Minute)} {
			want := instantResult(t, engine, reference, q, at)
			query, err := metrics_promql.NewInstantQuery(context.Background(), projectID, q, at)
			require.NoError(t, err)
			requireClose(t, want, query.Exec(context.Background()), "instant %s at %s", q, at)
			query.Close()
		}
	}
	require.GreaterOrEqual(t, pushed()-pushedBefore, float64(4*len(partialsQueries)))
	require.Zero(t, fallbacks()-fallbacksBefore)
}

type partialsSeriesState struct {
	name   string
	labels map[string]string
	value  float64
	gauge  bool
}

// partialsSeries: counters, gauges with and without a zone, and a
// histogram's cumulative buckets.
func partialsSeries() []*partialsSeriesState {
	var all []*partialsSeriesState
	for i := range 30 {
		job := []string{"api", "web", "batch"}[i%3]
		all = append(all, &partialsSeriesState{name: "http_requests_total", labels: map[string]string{
			"job": job, "instance": fmt.Sprintf("host-%d", i%5), "code": []string{"200", "404", "500"}[i%3],
			"path": fmt.Sprintf("/p%d", i),
		}})
		gauge := map[string]string{"job": job, "instance": fmt.Sprintf("host-%d", i)}
		if i%4 != 0 {
			gauge["zone"] = []string{"a", "b", "c"}[i%3]
		}
		all = append(all, &partialsSeriesState{name: "mem_bytes", labels: gauge, gauge: true})
	}
	for i := range 3 {
		for _, le := range []string{"0.1", "0.5", "1", "+Inf"} {
			all = append(all, &partialsSeriesState{name: "latency_bucket", labels: map[string]string{
				"instance": fmt.Sprintf("host-%d", i), "le": le,
			}})
		}
	}
	return all
}

// seedPartialsSamples writes every series every 15 s (jittered to the
// millisecond, some on a bucket's edge) in [from, to) and flushes them as
// files segments, a series' consecutive samples in different ones. Every
// 5th series leaves for 20 minutes of each 40, gauges with a stale marker;
// counters reset now and then; a few samples are stored twice.
func seedPartialsSamples(
	t *testing.T, reference *referenceQueryable, projectID uuid.UUID, all []*partialsSeriesState,
	random *rand.Rand, from, to time.Time, files int,
) {
	t.Helper()
	batches := make([][]*metrics_core.MetricSample, files)
	add := func(s *partialsSeriesState, at time.Time, v float64) {
		if !at.Before(to) {
			at = to.Add(-time.Millisecond)
		}
		reference.add(s.name, s.labels, at.UnixMilli(), v)
		metricType := metrics_core.MetricTypeCounter
		if s.gauge {
			metricType = metrics_core.MetricTypeGauge
		}
		file := int(at.UnixMilli()/15_000) % files
		sample := &metrics_core.MetricSample{
			ID: uuid.New(), ProjectID: projectID, MetricName: s.name, Labels: s.labels,
			Value: v, Timestamp: at, Type: metricType,
		}
		batches[file] = append(batches[file], sample)
		if random.Intn(50) == 0 {
			// Redelivered into another segment: the same sample twice.
			again := *sample
			again.ID = uuid.New()
			batches[(file+1)%files] = append(batches[(file+1)%files], &again)
		}
	}
	stale := math.Float64frombits(value.StaleNaN)
	for at := from.Truncate(15 * time.Second); at.Before(to); at = at.Add(15 * time.Second) {
		if at.Before(from) {
			continue
		}
		away := (at.Minute()/20)%2 == 1
		for i, s := range all {
			if i%5 == 2 && away {
				if s.gauge && at.Minute()%20 == 0 && at.Second() == 0 {
					add(s, at.Add(time.Second), stale)
				}
				continue
			}
			sampledAt := at
			if random.Intn(3) != 0 {
				sampledAt = at.Add(time.Duration(random.Intn(4000)) * time.Millisecond)
			}
			switch {
			case s.gauge && random.Intn(150) == 0:
				s.value = math.NaN()
			case s.gauge:
				s.value = 1e6 + random.Float64()*1e5
			case random.Intn(200) == 0:
				s.value = random.Float64()
			default:
				s.value += random.Float64() * 20
			}
			add(s, sampledAt, s.value)
		}
	}
	for _, batch := range batches {
		require.NoError(t, metrics_core.GetMetricCoreRepository().StoreSamplesBatch(
			map[uuid.UUID][]*metrics_core.MetricSample{projectID: batch}))
		require.NoError(t, metrics_core.GetMetricCoreRepository().ForceFlush())
	}
}
