package synthetics

import (
	"context"
	"testing"
	"time"

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

type testSeries struct {
	labels  map[string]string
	samples []float64
}

type testSample struct {
	t int64
	f float64
}

type testQueryable struct{ series []storage.Series }

type testSeriesSet struct {
	series []storage.Series
	at     int
}

func Test_FailingQuery_WhenALocationHasSeveralSeries_CountsItOnce(t *testing.T) {
	check := &Check{ID: uuid.New(), IntervalSeconds: 300}
	id := check.ID.String()
	// Mumbai's failing runs landed in two series (older probes labelled
	// runs that reached TLS differently); Frankfurt passes.
	samples := newTestQueryable([]testSeries{
		{map[string]string{"__name__": "synthetics_up", "check_id": id, "location": "mumbai", "tls_verified": "false"}, []float64{0, 0}},
		{map[string]string{"__name__": "synthetics_up", "check_id": id, "location": "mumbai"}, []float64{0, 0}},
		{map[string]string{"__name__": "synthetics_up", "check_id": id, "location": "frankfurt", "tls_verified": "true"}, []float64{1, 1}},
	})

	require.Equal(t, 1.0, evaluateAt(t, samples, failingQuery(check), 5*time.Minute))
}

func evaluateAt(t *testing.T, queryable storage.Queryable, query string, at time.Duration) float64 {
	t.Helper()
	engine := promql.NewEngine(promql.EngineOpts{MaxSamples: 10000, Timeout: time.Minute})
	instant, err := engine.NewInstantQuery(context.Background(), queryable, nil, query, time.Unix(0, 0).Add(at))
	require.NoError(t, err)
	defer instant.Close()
	result := instant.Exec(context.Background())
	require.NoError(t, result.Err)
	vector, err := result.Vector()
	require.NoError(t, err)
	require.Len(t, vector, 1, query)
	return vector[0].F
}

// newTestQueryable holds each series' samples five minutes apart from time 0.
func newTestQueryable(all []testSeries) *testQueryable {
	queryable := &testQueryable{}
	for _, series := range all {
		var samples []chunks.Sample
		for i, value := range series.samples {
			samples = append(samples, testSample{t: int64(i) * 5 * 60 * 1000, f: value})
		}
		queryable.series = append(queryable.series, storage.NewListSeries(labels.FromMap(series.labels), samples))
	}
	return queryable
}

func (q *testQueryable) Querier(int64, int64) (storage.Querier, error) {
	return &storage.MockQuerier{SelectMockFunction: func(
		_ bool, _ *storage.SelectHints, matchers ...*labels.Matcher,
	) storage.SeriesSet {
		var selected []storage.Series
		for _, series := range q.series {
			matches := true
			for _, matcher := range matchers {
				matches = matches && matcher.Matches(series.Labels().Get(matcher.Name))
			}
			if matches {
				selected = append(selected, series)
			}
		}
		return &testSeriesSet{series: selected, at: -1}
	}}, nil
}

func (s *testSeriesSet) Next() bool                        { s.at++; return s.at < len(s.series) }
func (s *testSeriesSet) At() storage.Series                { return s.series[s.at] }
func (s *testSeriesSet) Err() error                        { return nil }
func (s *testSeriesSet) Warnings() annotations.Annotations { return nil }

func (s testSample) T() int64                      { return s.t }
func (s testSample) ST() int64                     { return 0 }
func (s testSample) F() float64                    { return s.f }
func (s testSample) H() *histogram.Histogram       { return nil }
func (s testSample) FH() *histogram.FloatHistogram { return nil }
func (s testSample) Type() chunkenc.ValueType      { return chunkenc.ValFloat }
func (s testSample) Copy() chunks.Sample           { return s }
