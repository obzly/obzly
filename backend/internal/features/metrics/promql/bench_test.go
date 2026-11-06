package metrics_promql_test

import (
	"context"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	metrics_core "obzly/internal/features/metrics/core"
	metrics_promql "obzly/internal/features/metrics/promql"
	obzly_storage "obzly/internal/storage"

	"github.com/google/uuid"
	"github.com/prometheus/prometheus/promql"
	"github.com/stretchr/testify/require"
)

// Benchmarks rate() over an hour across N series of one metric (a sample
// every 15 s), through both series select paths and pushed down. Skipped
// unless OBZLY_METRICS_BENCH_SERIES is set, e.g. =20000 with -run Benchmark_Rate -v.
func Test_Benchmark_RateAcrossSeries(t *testing.T) {
	seriesCount, err := strconv.Atoi(os.Getenv("OBZLY_METRICS_BENCH_SERIES"))
	if err != nil || seriesCount <= 0 {
		t.Skip("set OBZLY_METRICS_BENCH_SERIES to run")
	}
	end := time.Now().UTC().Truncate(time.Minute)
	start := end.Add(-time.Hour)
	projectID := uuid.New()

	seeded := time.Now()
	const perBatch = 200_000
	var batch []*metrics_core.MetricSample
	store := func() {
		require.NoError(t, metrics_core.GetMetricCoreRepository().StoreSamplesBatch(
			map[uuid.UUID][]*metrics_core.MetricSample{projectID: batch}))
		batch = batch[:0]
	}
	for at := start.Add(-5 * time.Minute); !at.After(end); at = at.Add(15 * time.Second) {
		for i := range seriesCount {
			batch = append(batch, &metrics_core.MetricSample{
				ID: uuid.New(), ProjectID: projectID, MetricName: "bench_requests_total",
				Labels: map[string]string{"instance": "host-" + strconv.Itoa(i), "job": "bench"},
				Value:  float64(at.Unix()-start.Unix()) * float64(i%7+1), Timestamp: at,
				Type: metrics_core.MetricTypeCounter,
			})
			if len(batch) == perBatch {
				store()
			}
		}
	}
	store()
	require.NoError(t, metrics_core.GetMetricCoreRepository().ForceFlush())
	t.Logf("seeded %d series x %d samples in %s", seriesCount, int(65*time.Minute/(15*time.Second))+1,
		time.Since(seeded).Round(time.Second))

	engine := metrics_promql.GetEngine()
	const expression = `sum(rate(bench_requests_total[5m]))`
	run := func(path string) {
		var timings []time.Duration
		for range 5 {
			began := time.Now()
			var query promql.Query
			var err error
			if path == "pushed down" {
				query, err = metrics_promql.NewRangeQuery(
					context.Background(),
					projectID,
					expression,
					start,
					end,
					time.Minute,
				)
			} else {
				query, err = engine.NewRangeQuery(context.Background(), metrics_promql.NewQueryable(projectID), nil,
					expression, start, end, time.Minute)
			}
			require.NoError(t, err)
			result := query.Exec(context.Background())
			if result.Err != nil && strings.Contains(result.Err.Error(), "raw samples") {
				t.Logf("%s: %d series, refused: %v", path, seriesCount, result.Err)
				query.Close()
				return
			}
			require.NoError(t, result.Err)
			timings = append(timings, time.Since(began))
			query.Close()
		}
		sort.Slice(timings, func(i, j int) bool { return timings[i] < timings[j] })
		t.Logf(
			"%s: %d series, p50 %s, p95 %s (of %d runs)",
			path,
			seriesCount,
			timings[len(timings)/2].Round(
				time.Millisecond,
			),
			timings[len(timings)-1].Round(time.Millisecond),
			len(timings),
		)
	}
	run("label predicates")
	require.NoError(t, obzly_storage.GetDb().Exec(
		"UPDATE metric_series_coverage SET since = ? WHERE project_id = ?", start.Add(-time.Hour), projectID).Error)
	run("series ids")
	run("pushed down")
	require.NoError(t, metrics_core.GetMetricCoreRepository().DeleteMetricsByProject(projectID))
}
