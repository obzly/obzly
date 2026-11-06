package metrics_promql_test

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"testing"
	"time"

	metrics_core "obzly/internal/features/metrics/core"
	metrics_promql "obzly/internal/features/metrics/promql"
	obzly_storage "obzly/internal/storage"
	"obzly/internal/telemetry"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/promql/parser"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"github.com/prometheus/prometheus/tsdb/chunks"
	"github.com/prometheus/prometheus/util/annotations"
	"github.com/stretchr/testify/require"
)

// parityQueries cover selectors (equality, regex, negations, absent
// labels, name regex), counters (rate, increase across resets),
// aggregations, *_over_time, topk, binary operators, offset and absent.
var parityQueries = []string{
	`http_requests_total`,
	`http_requests_total{job="api"}`,
	`http_requests_total{code=~"5.."}`,
	`http_requests_total{code!~"2..", job!="batch"}`,
	`{__name__=~"http_.*|mem_.*", job="web"}`,
	`mem_bytes{zone=""}`,
	`mem_bytes{zone=~".+"}`,
	`rate(http_requests_total[5m])`,
	`sum by (job) (rate(http_requests_total[5m]))`,
	`sum by (code) (increase(http_requests_total[10m]))`,
	`max_over_time(mem_bytes[10m])`,
	`avg_over_time(mem_bytes{job="api"}[10m])`,
	`count_over_time(mem_bytes[10m])`,
	`topk(3, sum by (instance) (mem_bytes))`,
	`sum by (job) (mem_bytes) / count by (job) (mem_bytes)`,
	`http_requests_total offset 10m`,
	`delta(mem_bytes[15m])`,
	`count by (code) (http_requests_total)`,
	`absent(nonexistent_metric)`,
	`quantile_over_time(0.9, mem_bytes[15m])`,
	// Push-down shapes: every function and aggregation,
	// ranges that aren't a multiple of the step, and pushed-down operands.
	`sum(increase(http_requests_total[10m]))`,
	`rate(http_requests_total[90s])`,
	`max by (job) (max_over_time(mem_bytes[10m]))`,
	`min by (zone) (min_over_time(mem_bytes[7m]))`,
	`avg by (zone, job) (avg_over_time(mem_bytes[5m]))`,
	`count by (job) (count_over_time(mem_bytes{zone=~"a|b"}[3m]))`,
	`sum by (instance) (sum_over_time(mem_bytes[2m]))`,
	`sum by (job) (rate(http_requests_total[5m])) / sum by (job) (rate(http_requests_total[10m]))`,
	`count(rate(http_requests_total{code="500"}[1m]))`,
	`sum by (missing_label) (rate(http_requests_total[5m]))`,
	`rate(http_requests_total{job="nope"}[5m])`,
	`max by (job) (avg_over_time(mem_bytes[1m]))`,
	`min by (job) (sum_over_time(mem_bytes[1m]))`,
	// Synthetics shapes: a ratio of sums, a count of failing groups, and a
	// quantile over a subquery that merges series first.
	`sum by (job) (sum_over_time(mem_bytes[10m])) / sum by (job) (count_over_time(mem_bytes[10m]))`,
	`sum by (job) (min by (job, zone) (min_over_time(mem_bytes[5m])) > bool 100)`,
	`max by (job) (quantile_over_time(0.95, (max by (job, zone) (max_over_time(mem_bytes[2m])))[30m:2m]))`,
	`min by (job, zone) (last_over_time(mem_bytes[7m]))`,
}

// pushdownTolerance: push-down sums in another order than the engine
// (resets, sum/avg aggregations), so results agree to float rounding.
const pushdownTolerance = 1e-9

// The PromQL engine answers the same over Obzly's storage as over an
// in-memory reference that selects series with Prometheus's own label
// matchers: identical samples in both, every query
// compared exactly, through both of Obzly's series select paths.
func Test_PromQL_Parity_WithAReferenceStorage(t *testing.T) {
	end := time.Now().UTC().Truncate(time.Minute)
	start := end.Add(-2 * time.Hour)
	reference := &referenceQueryable{}
	projectID := seedParitySeries(t, reference, start, end)
	obzly := metrics_promql.NewQueryable(projectID)
	engine := metrics_promql.GetEngine()

	pushed := func() float64 { return testutil.ToFloat64(telemetry.PromQLPushdown.WithLabelValues("pushed")) }
	fallbacks := func() float64 { return testutil.ToFloat64(telemetry.PromQLPushdown.WithLabelValues("fallback")) }
	check := func(path string) {
		pushedBefore, fallbacksBefore := pushed(), fallbacks()
		for _, q := range parityQueries {
			for _, step := range []time.Duration{30 * time.Second, 2 * time.Minute} {
				wantResult := rangeResult(t, engine, reference, q, end.Add(-time.Hour), end, step)
				want := canonical(t, wantResult)
				if q != `absent(nonexistent_metric)` && q != `rate(http_requests_total{job="nope"}[5m])` {
					require.NotEqual(t, "", want, "%s: the reference answers %s", path, q)
				}
				got := rangeQuery(t, engine, obzly, q, end.Add(-time.Hour), end, step)
				require.Equal(t, want, got, "%s: range %s step %s", path, q, step)

				query, err := metrics_promql.NewRangeQuery(
					context.Background(),
					projectID,
					q,
					end.Add(-time.Hour),
					end,
					step,
				)
				require.NoError(t, err)
				requireClose(
					t,
					wantResult,
					query.Exec(context.Background()),
					"%s, pushed down: range %s step %s",
					path,
					q,
					step,
				)
				query.Close()
			}
			at := end.Add(-17 * time.Minute)
			wantResult := instantResult(t, engine, reference, q, at)
			got := instantQuery(t, engine, obzly, q, at)
			require.Equal(t, canonical(t, wantResult), got, "%s: instant %s", path, q)

			query, err := metrics_promql.NewInstantQuery(context.Background(), projectID, q, at)
			require.NoError(t, err)
			requireClose(t, wantResult, query.Exec(context.Background()), "%s, pushed down: instant %s", path, q)
			query.Close()
		}
		require.GreaterOrEqual(t, pushed()-pushedBefore, float64(3*19), "%s: push-down answered", path)
		require.Zero(t, fallbacks()-fallbacksBefore, "%s: nothing fell back", path)
	}

	// Series tracking started after this data: samples are matched by label.
	check("label predicates")
	require.NoError(t, obzly_storage.GetDb().Exec(
		"UPDATE metric_series_coverage SET since = ? WHERE project_id = ?", start.Add(-time.Hour), projectID).Error)
	// Now the window is covered: series stream, ids, label index.
	check("series ids")
}

// seedParitySeries writes the same 200 series (counters with resets,
// gauges with and without a zone label) every 15 s from start to end into
// Obzly and into reference.
func seedParitySeries(t *testing.T, reference *referenceQueryable, start, end time.Time) uuid.UUID {
	t.Helper()
	random := rand.New(rand.NewSource(42))
	projectID := uuid.New()
	var batch []*metrics_core.MetricSample
	type series struct {
		name   string
		labels map[string]string
		value  float64
		gauge  bool
	}
	var all []*series
	for i := range 100 {
		labelSet := map[string]string{
			"job":      []string{"api", "web", "batch"}[i%3],
			"instance": fmt.Sprintf("host-%d", i%10),
			"code":     []string{"200", "201", "404", "500", "503"}[i%5],
			"path":     fmt.Sprintf("/p%d", i%7),
		}
		all = append(all, &series{name: "http_requests_total", labels: labelSet})
		gaugeLabels := map[string]string{"job": labelSet["job"], "instance": fmt.Sprintf("host-%d-%d", i%10, i)}
		if i%4 != 0 {
			gaugeLabels["zone"] = []string{"a", "b", "c"}[i%3]
		}
		all = append(all, &series{name: "mem_bytes", labels: gaugeLabels, gauge: true, value: 1e6})
	}
	// Every 5th series is a pod that comes and goes (rate extrapolates at
	// its edges), and samples arrive with jitter at millisecond precision.
	for at := start; !at.After(end); at = at.Add(15 * time.Second) {
		for i, s := range all {
			if i%5 == 2 && (at.Minute()/20)%2 == 1 {
				continue
			}
			sampledAt := at.Add(time.Duration(random.Intn(4000)) * time.Millisecond)
			if s.gauge && random.Intn(150) == 0 {
				s.value = math.NaN()
			} else if s.gauge {
				s.value = 1e6 + random.Float64()*1e5
			} else if random.Intn(200) == 0 {
				s.value = random.Float64() // counter reset
			} else {
				s.value += random.Float64() * 20
			}
			reference.add(s.name, s.labels, sampledAt.UnixMilli(), s.value)
			metricType := metrics_core.MetricTypeCounter
			if s.gauge {
				metricType = metrics_core.MetricTypeGauge
			}
			batch = append(batch, &metrics_core.MetricSample{
				ID: uuid.New(), ProjectID: projectID, MetricName: s.name, Labels: s.labels,
				Value: s.value, Timestamp: sampledAt, Type: metricType,
			})
		}
	}
	require.NoError(t, metrics_core.GetMetricCoreRepository().StoreSamplesBatch(
		map[uuid.UUID][]*metrics_core.MetricSample{projectID: batch}))
	require.NoError(t, metrics_core.GetMetricCoreRepository().ForceFlush())
	return projectID
}

func rangeQuery(t *testing.T, engine *promql.Engine, queryable storage.Queryable, q string,
	start, end time.Time, step time.Duration,
) string {
	t.Helper()
	return canonical(t, rangeResult(t, engine, queryable, q, start, end, step))
}

func rangeResult(t *testing.T, engine *promql.Engine, queryable storage.Queryable, q string,
	start, end time.Time, step time.Duration,
) *promql.Result {
	t.Helper()
	query, err := engine.NewRangeQuery(context.Background(), queryable, nil, q, start, end, step)
	require.NoError(t, err)
	t.Cleanup(query.Close)
	return query.Exec(context.Background())
}

func instantQuery(t *testing.T, engine *promql.Engine, queryable storage.Queryable, q string, at time.Time) string {
	t.Helper()
	return canonical(t, instantResult(t, engine, queryable, q, at))
}

func instantResult(
	t *testing.T,
	engine *promql.Engine,
	queryable storage.Queryable,
	q string,
	at time.Time,
) *promql.Result {
	t.Helper()
	query, err := engine.NewInstantQuery(context.Background(), queryable, nil, q, at)
	require.NoError(t, err)
	t.Cleanup(query.Close)
	return query.Exec(context.Background())
}

// requireClose: the same series and timestamps, values within
// pushdownTolerance (relative) or both NaN.
func requireClose(t *testing.T, want, got *promql.Result, format string, args ...any) {
	t.Helper()
	require.NoError(t, want.Err)
	require.NoError(t, got.Err, append([]any{format}, args...)...)
	wantPoints, gotPoints := pointsOf(want.Value), pointsOf(got.Value)
	message := fmt.Sprintf(format, args...)
	require.Equal(t, sortedKeys(wantPoints), sortedKeys(gotPoints), "%s: series", message)
	for key, points := range wantPoints {
		other := gotPoints[key]
		require.Len(t, other, len(points), "%s: %s points", message, key)
		for i, point := range points {
			require.Equal(t, point.T, other[i].T, "%s: %s", message, key)
			if math.IsNaN(point.F) {
				require.True(t, math.IsNaN(other[i].F), "%s: %s at %d", message, key, point.T)
				continue
			}
			require.InEpsilon(t, point.F+1e-300, other[i].F+1e-300, pushdownTolerance,
				"%s: %s at %d", message, key, point.T)
		}
	}
}

func pointsOf(value parser.Value) map[string][]promql.FPoint {
	points := map[string][]promql.FPoint{}
	switch v := value.(type) {
	case promql.Matrix:
		for _, series := range v {
			points[series.Metric.String()] = series.Floats
		}
	case promql.Vector:
		for _, sample := range v {
			points[sample.Metric.String()] = []promql.FPoint{{T: sample.T, F: sample.F}}
		}
	case promql.Scalar:
		points[""] = []promql.FPoint{{T: v.T, F: v.V}}
	}
	return points
}

func sortedKeys(points map[string][]promql.FPoint) []string {
	keys := make([]string, 0, len(points))
	for key := range points {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// canonical is a result as text with series in label order, so the
// comparison doesn't depend on series order.
func canonical(t *testing.T, result *promql.Result) string {
	t.Helper()
	require.NoError(t, result.Err)
	switch value := result.Value.(type) {
	case promql.Matrix:
		sort.Slice(value, func(i, j int) bool { return labels.Compare(value[i].Metric, value[j].Metric) < 0 })
		return value.String()
	case promql.Vector:
		sort.Slice(value, func(i, j int) bool { return labels.Compare(value[i].Metric, value[j].Metric) < 0 })
		return value.String()
	default:
		return result.Value.String()
	}
}

// referenceQueryable is the oracle: every sample in memory, series picked
// with Prometheus's own labels.Matcher.
type referenceQueryable struct {
	series map[string]*referenceSeries
}

type referenceSeries struct {
	labels  labels.Labels
	samples []referencePoint
}

type referencePoint struct {
	t int64
	f float64
}

func (r *referenceQueryable) add(name string, labelSet map[string]string, t int64, f float64) {
	if r.series == nil {
		r.series = map[string]*referenceSeries{}
	}
	withName := map[string]string{"__name__": name}
	for key, value := range labelSet {
		withName[key] = value
	}
	lbls := labels.FromMap(withName)
	key := lbls.String()
	series, ok := r.series[key]
	if !ok {
		series = &referenceSeries{labels: lbls}
		r.series[key] = series
	}
	series.samples = append(series.samples, referencePoint{t, f})
}

func (r *referenceQueryable) Querier(mint, maxt int64) (storage.Querier, error) {
	return &referenceQuerier{reference: r, mint: mint, maxt: maxt}, nil
}

type referenceQuerier struct {
	reference  *referenceQueryable
	mint, maxt int64
}

func (q *referenceQuerier) Select(
	_ context.Context, sortSeries bool, _ *storage.SelectHints, matchers ...*labels.Matcher,
) storage.SeriesSet {
	var selected []storage.Series
	for _, series := range q.reference.series {
		matches := true
		for _, matcher := range matchers {
			if !matcher.Matches(series.labels.Get(matcher.Name)) {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		var samples []chunks.Sample
		for _, point := range series.samples {
			if point.t >= q.mint && point.t <= q.maxt {
				samples = append(samples, referenceSample{point})
			}
		}
		if len(samples) > 0 {
			selected = append(selected, storage.NewListSeries(series.labels, samples))
		}
	}
	sort.Slice(selected, func(i, j int) bool { return labels.Compare(selected[i].Labels(), selected[j].Labels()) < 0 })
	return &referenceSet{series: selected, at: -1}
}

func (q *referenceQuerier) LabelValues(
	context.Context, string, *storage.LabelHints, ...*labels.Matcher,
) ([]string, annotations.Annotations, error) {
	return nil, nil, nil
}

func (q *referenceQuerier) LabelNames(
	context.Context, *storage.LabelHints, ...*labels.Matcher,
) ([]string, annotations.Annotations, error) {
	return nil, nil, nil
}

func (q *referenceQuerier) Close() error { return nil }

type referenceSet struct {
	series []storage.Series
	at     int
}

func (s *referenceSet) Next() bool                        { s.at++; return s.at < len(s.series) }
func (s *referenceSet) At() storage.Series                { return s.series[s.at] }
func (s *referenceSet) Err() error                        { return nil }
func (s *referenceSet) Warnings() annotations.Annotations { return nil }

type referenceSample struct{ point referencePoint }

func (s referenceSample) T() int64                      { return s.point.t }
func (s referenceSample) F() float64                    { return s.point.f }
func (s referenceSample) H() *histogram.Histogram       { return nil }
func (s referenceSample) FH() *histogram.FloatHistogram { return nil }
func (s referenceSample) Type() chunkenc.ValueType      { return chunkenc.ValFloat }
func (s referenceSample) Copy() chunks.Sample           { return s }
func (s referenceSample) ST() int64                     { return 0 }
