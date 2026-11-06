package metrics_querier

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"testing"
	"time"

	metrics_core "obzly/internal/features/metrics/core"
	metrics_ingester "obzly/internal/features/metrics/ingester"
	"obzly/internal/query"
	"obzly/internal/storage/segments"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Time slices over executors give what one pass over every sample does,
// counter resets at a segment boundary included, whether segments follow
// each other in time or interleave (a slice reads all of them).
func Test_EvaluateRangeFunction_SlicedOverExecutors_MatchesOnePass(t *testing.T) {
	end := time.Now().UTC().Truncate(time.Minute)
	start := end.Add(-time.Hour)
	for _, layout := range []string{"by time", "interleaved"} {
		projectID := seedPushdownCounters(t, start, end, layout == "interleaved")
		inProcess := newParityQuerier(t, nil)
		spread := newParityQuerier(t, spreadExecutors(t, projectID))
		for _, request := range []metrics_core.RangeFunctionRequestDTO{
			{Function: "rate", RangeMs: 5 * 60_000},
			{Function: "increase", RangeMs: 10 * 60_000, Aggregation: "sum", By: []string{"job"}},
			{Function: "rate", RangeMs: 90_000, Aggregation: "count"},
			{Function: "sum_over_time", RangeMs: 7 * 60_000},
			{Function: "max_over_time", RangeMs: 3 * 60_000, Aggregation: "max", By: []string{"job"}},
			{Function: "min_over_time", RangeMs: 3 * 60_000},
			{Function: "avg_over_time", RangeMs: 3 * 60_000, Aggregation: "avg", By: []string{"job"}},
			{Function: "count_over_time", RangeMs: 3 * 60_000},
		} {
			request.Matchers = []metrics_core.MatcherDTO{
				{Name: "__name__", Type: metrics_core.MatchEqual, Value: "requests_total"},
			}
			request.StartMs, request.EndMs, request.StepMs = start.UnixMilli(), end.UnixMilli(), 60_000
			want, ok, err := inProcess.EvaluateRangeFunction(context.Background(), projectID, &request)
			require.NoError(t, err)
			require.True(t, ok)
			require.NotEmpty(t, want.Series)
			got, ok, err := spread.EvaluateRangeFunction(context.Background(), projectID, &request)
			require.NoError(t, err)
			require.True(t, ok, "%s %+v", layout, request)
			require.Len(t, got.Series, len(want.Series))
			for i, series := range want.Series {
				require.Equal(t, series.Labels, got.Series[i].Labels)
				require.Len(t, got.Series[i].Points, len(series.Points), "%+v %v", request, series.Labels)
				for j, point := range series.Points {
					other := got.Series[i].Points[j]
					require.Equal(t, point.TimestampMs, other.TimestampMs)
					require.InEpsilon(t, point.Value+1e-300, other.Value+1e-300, 1e-9,
						"%s %+v %v at %d", layout, request, series.Labels, point.TimestampMs)
				}
			}
		}
	}
}

// Storage declines what it can't answer exactly, and an aggregation's
// labels are its non-empty By labels.
func Test_EvaluateRangeFunction_Declines_AndLabelsAggregations(t *testing.T) {
	end := time.Now().UTC().Truncate(time.Minute)
	projectID := seedPushdownCounters(t, end.Add(-time.Hour), end, false)
	q := newParityQuerier(t, nil)
	name := metrics_core.MatcherDTO{Name: "__name__", Type: metrics_core.MatchEqual, Value: "requests_total"}
	base := metrics_core.RangeFunctionRequestDTO{
		Matchers: []metrics_core.MatcherDTO{name}, Function: "rate", RangeMs: 300_000,
		StartMs: end.Add(-time.Hour).UnixMilli(), EndMs: end.UnixMilli(), StepMs: 60_000,
	}
	for label, change := range map[string]func(*metrics_core.RangeFunctionRequestDTO){
		"no metric name": func(r *metrics_core.RangeFunctionRequestDTO) {
			r.Matchers = []metrics_core.MatcherDTO{{Name: "job", Type: metrics_core.MatchEqual, Value: "api"}}
		},
		"unknown function":    func(r *metrics_core.RangeFunctionRequestDTO) { r.Function = "deriv" },
		"unknown aggregation": func(r *metrics_core.RangeFunctionRequestDTO) { r.Aggregation = "stddev" },
		"by without aggregation": func(r *metrics_core.RangeFunctionRequestDTO) {
			r.By = []string{"job"}
		},
	} {
		request := base
		change(&request)
		_, ok, err := q.EvaluateRangeFunction(context.Background(), projectID, &request)
		require.NoError(t, err)
		require.False(t, ok, label)
	}

	request := base
	request.Aggregation, request.By = "sum", []string{"job", "zone"}
	response, ok, err := q.EvaluateRangeFunction(context.Background(), projectID, &request)
	require.NoError(t, err)
	require.True(t, ok)
	var groups []map[string]string
	for _, series := range response.Series {
		groups = append(groups, series.Labels)
	}
	require.ElementsMatch(t, []map[string]string{
		{"job": "api", "zone": "a"}, {"job": "api"}, {"job": "web", "zone": "a"}, {"job": "web"},
	}, groups)
}

// seedPushdownCounters writes 6 counters (resets, jitter, one that stops
// halfway) and a NaN-bearing gauge under the same name's job label, as 4
// uploaded segments: consecutive quarters of the hour, or round-robin
// (interleaved in time). One counter resets exactly where a quarter starts.
func seedPushdownCounters(t *testing.T, start, end time.Time, interleaved bool) uuid.UUID {
	t.Helper()
	// These segments are marked uploaded: hold the lock the uploader tests
	// take before resetting every uploaded metrics segment.
	test_utils.AcquireExclusiveGlobalQueue(t, "metrics-uploaded-queue")
	ingester, err := metrics_ingester.NewMetricIngester(t.TempDir(), segments.WithMaxRows(1_000_000))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ingester.Close() })
	projectID := uuid.New()
	random := rand.New(rand.NewSource(7))
	values := make([]float64, 6)
	quarters := make([][]*metrics_core.MetricSample, 4)
	index := 0
	for at := start.Add(-10 * time.Minute); !at.After(end); at = at.Add(15 * time.Second) {
		quarter := min(max(int(at.Sub(start)/(15*time.Minute)), 0), 3)
		for i := range values {
			if i == 5 && at.After(start.Add(30*time.Minute)) {
				continue
			}
			switch {
			case i == 0 && at.Equal(start.Add(30*time.Minute)):
				values[i] = 1 // reset at a quarter's first sample
			case random.Intn(150) == 0:
				values[i] = random.Float64()
			default:
				values[i] += random.Float64() * 10
			}
			labels := map[string]string{"job": []string{"api", "web"}[i%2], "instance": fmt.Sprintf("i%d", i)}
			if i < 2 {
				labels["zone"] = "a"
			}
			sample := &metrics_core.MetricSample{
				ID: uuid.New(), ProjectID: projectID, MetricName: "requests_total", Labels: labels,
				Value:     values[i],
				Timestamp: at.Add(time.Duration(random.Intn(3000)) * time.Millisecond),
				Type:      metrics_core.MetricTypeCounter,
			}
			if i == 4 && random.Intn(20) == 0 {
				sample.Value = math.NaN()
			}
			target := quarter
			if interleaved {
				target = index % 4
			}
			index++
			quarters[target] = append(quarters[target], sample)
		}
	}
	for _, batch := range quarters {
		require.NoError(t, ingester.StoreSamplesBatch(map[uuid.UUID][]*metrics_core.MetricSample{projectID: batch}))
		_, err = ingester.FlushAll()
		require.NoError(t, err)
	}
	repo := &segments.SegmentRepository{}
	records, err := repo.ListByProject(projectID, "metrics")
	require.NoError(t, err)
	require.Len(t, records, 4)
	for _, record := range records {
		require.NoError(t, repo.MarkUploaded(record.ID, "metrics/"+record.ID.String()+".parquet"))
	}
	return projectID
}

// spreadExecutors: 3 local executors standing in for remote ones, so the
// steps split into 3 slices.
func spreadExecutors(t *testing.T, _ uuid.UUID) func(*MetricQuerier) []query.Executor {
	return func(q *MetricQuerier) []query.Executor {
		var executors []query.Executor
		for i := range 3 {
			executors = append(executors, query.NewLocalExecutor(fmt.Sprintf("e%d", i), q.db, nil, ""))
		}
		return executors
	}
}
