// Package metrics_promql adapts metrics storage to Prometheus's
// storage.Queryable so the upstream promql.Engine evaluates the language.
// It is the only package that imports the PromQL library; metrics_core
// stays a plain storage abstraction.
package metrics_promql

import (
	"context"
	"fmt"
	"sort"
	"time"

	metrics_core "obzly/internal/features/metrics/core"

	"github.com/google/uuid"

	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"github.com/prometheus/prometheus/tsdb/chunks"
	"github.com/prometheus/prometheus/util/annotations"
)

// NewQueryable returns a storage.Queryable scoped to one project. The
// project ID is fixed here, never a matcher a crafted query could override.
func NewQueryable(projectID uuid.UUID) storage.Queryable {
	return &projectQueryable{projectID: projectID}
}

type projectQueryable struct {
	projectID uuid.UUID
}

func (q *projectQueryable) Querier(mint, maxt int64) (storage.Querier, error) {
	return &projectQuerier{projectID: q.projectID, mint: mint, maxt: maxt}, nil
}

type projectQuerier struct {
	projectID  uuid.UUID
	mint, maxt int64
}

func (q *projectQuerier) Close() error { return nil }

func (q *projectQuerier) timeRange() *metrics_core.TimeRangeDTO {
	from := time.UnixMilli(q.mint).UTC()
	to := time.UnixMilli(q.maxt).UTC()
	return &metrics_core.TimeRangeDTO{From: &from, To: &to}
}

func (q *projectQuerier) Select(
	ctx context.Context,
	sortSeries bool,
	hints *storage.SelectHints,
	matchers ...*labels.Matcher,
) storage.SeriesSet {
	var stepMs, rangeMs int64
	if hints != nil {
		stepMs, rangeMs = hints.Step, hints.Range
	}
	response, err := metrics_core.GetMetricCoreRepository().
		SelectSeries(ctx, q.projectID, &metrics_core.SelectSeriesRequestDTO{
			Matchers:  toMatcherDTOs(matchers),
			TimeRange: q.timeRange(),
			StepMs:    stepMs,
			RangeMs:   rangeMs,
		})
	if err != nil {
		return storage.ErrSeriesSet(fmt.Errorf("failed to select series: %w", err))
	}

	series := make([]storage.Series, len(response.Series))
	for i, s := range response.Series {
		series[i] = storage.NewListSeries(labelsFor(s), samplesFor(s))
	}

	if sortSeries {
		sortByLabels(series)
	}

	return &sliceSeriesSet{series: series, idx: -1}
}

func (q *projectQuerier) LabelValues(
	ctx context.Context,
	name string,
	_ *storage.LabelHints,
	matchers ...*labels.Matcher,
) ([]string, annotations.Annotations, error) {
	response, err := metrics_core.GetMetricCoreRepository().
		ListLabelValues(ctx, q.projectID, &metrics_core.ListLabelValuesRequestDTO{
			LabelName: name,
			Matchers:  toMatcherDTOs(matchers),
			TimeRange: q.timeRange(),
		})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list label values: %w", err)
	}
	return response.Values, nil, nil
}

func (q *projectQuerier) LabelNames(
	ctx context.Context,
	_ *storage.LabelHints,
	matchers ...*labels.Matcher,
) ([]string, annotations.Annotations, error) {
	response, err := metrics_core.GetMetricCoreRepository().
		ListLabelNames(ctx, q.projectID, &metrics_core.ListLabelNamesRequestDTO{
			Matchers:  toMatcherDTOs(matchers),
			TimeRange: q.timeRange(),
		})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list label names: %w", err)
	}
	return response.Names, nil, nil
}

func toMatcherDTOs(matchers []*labels.Matcher) []metrics_core.MatcherDTO {
	dtos := make([]metrics_core.MatcherDTO, len(matchers))
	for i, m := range matchers {
		dtos[i] = metrics_core.MatcherDTO{Name: m.Name, Type: matchTypeFor(m.Type), Value: m.Value}
	}
	return dtos
}

func matchTypeFor(t labels.MatchType) metrics_core.MatchType {
	switch t {
	case labels.MatchNotEqual:
		return metrics_core.MatchNotEqual
	case labels.MatchRegexp:
		return metrics_core.MatchRegexp
	case labels.MatchNotRegexp:
		return metrics_core.MatchNotRegexp
	default:
		return metrics_core.MatchEqual
	}
}

// labelsFor adds __name__, where PromQL keeps the metric name.
func labelsFor(series metrics_core.SeriesDTO) labels.Labels {
	m := make(map[string]string, len(series.Labels)+1)
	for k, v := range series.Labels {
		m[k] = v
	}
	m["__name__"] = series.MetricName
	return labels.FromMap(m)
}

func samplesFor(series metrics_core.SeriesDTO) []chunks.Sample {
	samples := make([]chunks.Sample, len(series.Samples))
	for i, s := range series.Samples {
		samples[i] = floatSample{t: s.Timestamp.UnixMilli(), f: s.Value}
	}
	return samples
}

func sortByLabels(series []storage.Series) {
	sort.Slice(series, func(i, j int) bool {
		return labels.Compare(series[i].Labels(), series[j].Labels()) < 0
	})
}

// floatSample is a float-only chunks.Sample: native histograms aren't
// stored, so H()/FH() return nil.
type floatSample struct {
	t int64
	f float64
}

func (s floatSample) T() int64 { return s.t }

// ST is 0 ("not set"): start timestamps aren't stored.
func (s floatSample) ST() int64                     { return 0 }
func (s floatSample) F() float64                    { return s.f }
func (s floatSample) H() *histogram.Histogram       { return nil }
func (s floatSample) FH() *histogram.FloatHistogram { return nil }
func (s floatSample) Type() chunkenc.ValueType      { return chunkenc.ValFloat }
func (s floatSample) Copy() chunks.Sample           { return s }

type sliceSeriesSet struct {
	series []storage.Series
	idx    int
}

func (s *sliceSeriesSet) Next() bool {
	s.idx++
	return s.idx < len(s.series)
}

func (s *sliceSeriesSet) At() storage.Series                { return s.series[s.idx] }
func (s *sliceSeriesSet) Err() error                        { return nil }
func (s *sliceSeriesSet) Warnings() annotations.Annotations { return nil }
