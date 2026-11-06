package metrics_querier

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	metrics_core "obzly/internal/features/metrics/core"
	"obzly/internal/query"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
)

// Range functions evaluated where the samples are; rate/increase treat the
// series as counters.
var pushdownFunctions = map[string]bool{
	"rate": true, "increase": true,
	"sum_over_time": true, "avg_over_time": true, "count_over_time": true,
	"min_over_time": true, "max_over_time": true,
}

var pushdownAggregations = map[string]bool{"": true, "sum": true, "avg": true, "min": true, "max": true, "count": true}

// pushdownFragmentSQL turns a fragment's samples into one partial per
// series and step whose window (t - range, t] holds any of them: count,
// first and last samples, counter resets inside the window, sum, min, max,
// and whether two samples share a millisecond. Each sample is copied to the
// steps it falls in (at most maxStepsPerSample) and grouped; series are
// keyed by hash(labels) - a window frame per row, or keying by the labels
// string, was 3-5x slower.
//
// %[1]s: WHERE clause; %[2]d: start ms; %[3]d: step ms; %[4]d: the
// slice's last step index; %[5]d: range ms - 1; %[6]d: its first step index.
const pushdownFragmentSQL = `WITH s AS (
	SELECT hash(labels) AS id, labels, epoch_ms(timestamp) AS t, value FROM ` + query.SourcePlaceholder + ` WHERE %[1]s
), r AS (
	SELECT id, t, value,
		CASE WHEN value < lag(value) OVER w AND NOT isnan(lag(value) OVER w) THEN lag(value) OVER w ELSE 0 END AS reset,
		coalesce(t = lag(t) OVER w, false) AS dup
	FROM s WINDOW w AS (PARTITION BY id ORDER BY t)
), x AS (
	SELECT id, t, value, reset, dup, unnest(generate_series(
		greatest(%[6]d, ceil((t - %[2]d) / %[3]d))::BIGINT,
		least(%[4]d, floor((t + %[5]d - %[2]d) / %[3]d))::BIGINT)) AS k
	FROM r
), a AS (
	SELECT id, k, count(*) AS n,
		min(t) AS first_t, arg_min(value, t) AS first_v, max(t) AS last_t, arg_max(value, t) AS last_v,
		sum(reset) - arg_min(reset, t) AS resets, sum(value) AS sum, min(value) AS min,
		max(CASE WHEN isnan(value) THEN NULL ELSE value END) AS max, bool_or(dup) AS dup
	FROM x GROUP BY id, k
), l AS (
	SELECT id, any_value(labels) AS labels FROM s GROUP BY id
)
SELECT l.labels, %[2]d + %[3]d * a.k AS t, n, first_t, first_v, last_t, last_v, resets, sum, min, max, dup
FROM a JOIN l USING (id)`

// maxStepsPerSample: the most steps one sample may be copied to (range /
// step, e.g. rate(x[1h]) at a 1 m step is 61); beyond it the engine reads
// the samples instead.
const maxStepsPerSample = 32

// pushdownSingleSQL names a slice's partials "merged": a slice reads every
// file its steps need, so each series and step has one partial.
const pushdownSingleSQL = `WITH merged AS (
	SELECT labels, t, n, first_t, first_v, last_t, last_v, resets, sum, min, max, dup AS bad
	FROM (%s)
)`

// pushdownRateSQL is Prometheus's extrapolatedRate for float counters
// (promql/functions.go), step by step in the same float order: extrapolate
// to a window edge when the first/last sample is within 1.1 average gaps
// of it, else by half a gap, and never back past the counter's zero.
// %[1]d: range ms; %[2]s: the divisor (range seconds for rate, else 1).
const pushdownRateSQL = `, r AS (
	SELECT labels, t, bad, first_v,
		last_v - first_v + resets AS result,
		(first_t - (t - %[1]d))::DOUBLE / 1000::DOUBLE AS to_start,
		(t - last_t)::DOUBLE / 1000::DOUBLE AS to_end,
		(last_t - first_t)::DOUBLE / 1000::DOUBLE AS sampled,
		((last_t - first_t)::DOUBLE / 1000::DOUBLE) / (n - 1)::DOUBLE AS gap
	FROM merged WHERE n >= 2
), e AS (
	SELECT *,
		CASE WHEN to_start >= gap * 1.1::DOUBLE THEN gap / 2::DOUBLE ELSE to_start END AS start1,
		CASE WHEN to_end >= gap * 1.1::DOUBLE THEN gap / 2::DOUBLE ELSE to_end END AS end1
	FROM r
), z AS (
	SELECT *,
		CASE WHEN result > 0 AND first_v >= 0 AND sampled * (first_v / result) < start1
			THEN sampled * (first_v / result) ELSE start1 END AS start2
	FROM e
), v AS (
	SELECT labels, t, bad,
		result * ((CASE WHEN sampled <> 0 THEN (sampled + start2 + end1) / sampled ELSE 1::DOUBLE END) / %[2]s) AS value
	FROM z
)`

// EvaluateRangeFunction evaluates request on the executors: each turns its
// samples into per-series, per-step partials and the coordinator merges
// them, applies the function and aggregates, so only results reach Go.
// false when that wouldn't match the engine exactly: no single metric
// name, rollups in the window, or overlapping partials.
func (q *MetricQuerier) EvaluateRangeFunction(
	ctx context.Context,
	projectID uuid.UUID,
	request *metrics_core.RangeFunctionRequestDTO,
) (*metrics_core.RangeFunctionResponseDTO, bool, error) {
	name := nameEqualityOf(request.Matchers)
	if name == "" || !pushdownFunctions[request.Function] || !pushdownAggregations[request.Aggregation] ||
		request.RangeMs <= 0 || request.EndMs < request.StartMs || (request.Aggregation == "" && len(request.By) > 0) {
		return nil, false, nil
	}
	stepMs := request.StepMs
	if stepMs <= 0 {
		stepMs = 1
	}
	lastStep := (request.EndMs - request.StartMs) / stepMs
	if min(lastStep+1, (request.RangeMs+stepMs-1)/stepMs+1) > maxStepsPerSample {
		return nil, false, nil
	}

	from := time.UnixMilli(request.StartMs - request.RangeMs + 1).UTC()
	to := time.UnixMilli(request.EndMs + 1).UTC().Add(-time.Microsecond)
	tail, records, err := q.inputs(projectID, from, to, name)
	if err != nil {
		return nil, false, err
	}
	sources, err := q.sampleSources(projectID, from, to, records, tail, request.StepMs, request.RangeMs,
		segments.PruningHints{MetricNames: []string{name}})
	if err != nil {
		return nil, false, err
	}
	if len(sources) != 1 {
		return nil, false, nil
	}
	slices, err := q.pushdownSlices(projectID, request, stepMs, lastStep, sources[0])
	if err != nil {
		return nil, false, err
	}

	empty := &metrics_core.RangeFunctionResponseDTO{Series: []metrics_core.RangeSeriesDTO{}}
	bySeries := map[string]*metrics_core.RangeSeriesDTO{}
	bad := false
	points := 0
	found, err := q.run(ctx, query.Request{
		Slices:   slices,
		FinalSQL: "SELECT * FROM " + query.ResultsPlaceholder,
	}, func(rows *sql.Rows) error {
		groups := len(request.By)
		for rows.Next() {
			var key string
			var t int64
			var value float64
			var rowBad bool
			values := make([]string, groups)
			dest := make([]any, 0, groups+4)
			dest = append(dest, &key)
			for i := range values {
				dest = append(dest, &values[i])
			}
			dest = append(dest, &t, &value, &rowBad)
			if err := rows.Scan(dest...); err != nil {
				return fmt.Errorf("failed to scan range function row: %w", err)
			}
			if rowBad {
				bad = true
				return nil
			}
			points++
			if points > maxSeriesRows {
				return fmt.Errorf("query returns more than %d points; narrow the time range or matchers", maxSeriesRows)
			}
			series, ok := bySeries[key]
			if !ok {
				series = &metrics_core.RangeSeriesDTO{Labels: resultLabels(request, key, values)}
				bySeries[key] = series
			}
			series.Points = append(series.Points, metrics_core.PointDTO{TimestampMs: t, Value: value})
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if bad {
		return nil, false, nil
	}
	if !found {
		return empty, true, nil
	}

	keys := make([]string, 0, len(bySeries))
	for key := range bySeries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	response := &metrics_core.RangeFunctionResponseDTO{Series: make([]metrics_core.RangeSeriesDTO, 0, len(keys))}
	for _, key := range keys {
		series := bySeries[key]
		sort.Slice(
			series.Points,
			func(i, j int) bool { return series.Points[i].TimestampMs < series.Points[j].TimestampMs },
		)
		response.Series = append(response.Series, *series)
	}
	return response, true, nil
}

// pushdownSlices splits the steps into one contiguous slice per remote
// executor (one in all without any). A slice reads every file overlapping
// (its first step - range, its last step], so it computes its steps whole -
// function and aggregation - and the coordinator only concatenates.
// Interleaved files are fine: they land in the same slice.
func (q *MetricQuerier) pushdownSlices(
	projectID uuid.UUID,
	request *metrics_core.RangeFunctionRequestDTO,
	stepMs, lastStep int64,
	source sampleSource,
) ([]query.Slice, error) {
	count := min(int64(max(q.coordinator.RemoteExecutors(), 1)), lastStep+1)
	var slices []query.Slice
	for i := range count {
		firstStep, endStep := (lastStep+1)*i/count, (lastStep+1)*(i+1)/count-1
		fromMs := request.StartMs + firstStep*stepMs - request.RangeMs + 1
		toMs := request.StartMs + endStep*stepMs
		from := time.UnixMilli(fromMs).UTC()
		to := time.UnixMilli(toMs + 1).UTC().Add(-time.Microsecond)
		where, args, err := q.whereFor(projectID, from, to, request.Matchers)
		if err != nil {
			return nil, err
		}
		slice := query.Slice{}
		for _, record := range source.records {
			if !record.MaxTime.Before(from) && !record.MinTime.After(to) {
				slice.Segments = append(slice.Segments, record)
			}
		}
		for _, file := range source.tail {
			if file.MaxTime.IsZero() || (!file.MaxTime.Before(from) && !file.MinTime.After(to)) {
				slice.LocalFiles = append(slice.LocalFiles, file)
			}
		}
		fragmentSQL := fmt.Sprintf(
			pushdownFragmentSQL,
			where,
			request.StartMs,
			stepMs,
			endStep,
			request.RangeMs-1,
			firstStep,
		)
		sliceSQL, finalArgs := pushdownFinalSQL(fmt.Sprintf(pushdownSingleSQL, fragmentSQL), request)
		slice.SQL = sliceSQL
		slice.Args = append(append([]any{}, args...), finalArgs...)
		slices = append(slices, slice)
	}
	return slices, nil
}

// pushdownFinalSQL follows merge (the CTE "merged") with the function,
// then the aggregation. Rows: a series key, one column per By label, t,
// value, bad.
func pushdownFinalSQL(merge string, request *metrics_core.RangeFunctionRequestDTO) (string, []any) {
	sqlText := merge
	switch request.Function {
	case "rate", "increase":
		divisor := "1::DOUBLE"
		if request.Function == "rate" {
			divisor = strconv.FormatFloat(float64(request.RangeMs)/1000, 'g', -1, 64) + "::DOUBLE"
		}
		sqlText += fmt.Sprintf(pushdownRateSQL, request.RangeMs, divisor)
	default:
		value := map[string]string{
			"sum_over_time":   "sum",
			"avg_over_time":   "sum / n::DOUBLE",
			"count_over_time": "n::DOUBLE",
			"min_over_time":   "min",
			"max_over_time":   "coalesce(max, 'NaN'::DOUBLE)",
		}[request.Function]
		sqlText += ", v AS (SELECT labels, t, bad, " + value + " AS value FROM merged)"
	}

	if request.Aggregation == "" {
		return sqlText + " SELECT labels, t, value, bad FROM v", nil
	}
	var args []any
	groups := make([]string, len(request.By))
	for i, label := range request.By {
		groups[i] = fmt.Sprintf("coalesce(json_extract_string(labels, ?), '') AS g%d", i)
		args = append(args, labelPath(label))
	}
	columns := make([]string, len(request.By))
	for i := range request.By {
		columns[i] = fmt.Sprintf("g%d", i)
	}
	key := "''"
	if len(columns) > 0 {
		key = "concat_ws(chr(0), " + strings.Join(columns, ", ") + ")"
	}
	aggregate := map[string]string{
		"sum":   "fsum(value)",
		"avg":   "fsum(value) / count(*)::DOUBLE",
		"count": "count(*)::DOUBLE",
		"min":   "min(value)",
		"max":   "coalesce(max(CASE WHEN isnan(value) THEN NULL ELSE value END), 'NaN'::DOUBLE)",
	}[request.Aggregation]
	selectGroups := ""
	if len(groups) > 0 {
		selectGroups = strings.Join(groups, ", ") + ", "
	}
	groupBy := ""
	if len(columns) > 0 {
		groupBy = strings.Join(columns, ", ") + ", "
	}
	sqlText += ", a AS (SELECT " + selectGroups + "t, value, bad FROM v)" +
		" SELECT " + key + " AS key, " + groupBy + "t, " + aggregate + " AS value, bool_or(bad) AS bad" +
		" FROM a GROUP BY " + groupBy + "t"
	return sqlText, args
}

// resultLabels: the series' labels (the function drops the name), or the
// non-empty By labels of an aggregation.
func resultLabels(request *metrics_core.RangeFunctionRequestDTO, key string, values []string) map[string]string {
	if request.Aggregation == "" {
		return decodeLabels(key)
	}
	labels := make(map[string]string, len(values))
	for i, value := range values {
		if value != "" {
			labels[request.By[i]] = value
		}
	}
	return labels
}
