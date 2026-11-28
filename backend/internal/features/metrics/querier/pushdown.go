package metrics_querier

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	metrics_core "obzly/internal/features/metrics/core"
	"obzly/internal/query"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
)

// msWindow is a time range in Unix ms, both ends included.
type msWindow struct {
	fromMs, toMs int64
}

// pushdownPlan is a request's slices: whole ones (function and aggregation
// computed by the executor) and partial ones (time chunks the coordinator
// merges first), never answering the same step.
type pushdownPlan struct {
	planned []plannedSlice
}

type plannedSlice struct {
	slice   query.Slice
	window  msWindow
	partial bool
}

// planBuilder adds one request's slices to a plan.
type planBuilder struct {
	q         *MetricQuerier
	projectID uuid.UUID
	request   *metrics_core.RangeFunctionRequestDTO
	source    sampleSource
	stepMs    int64
}

// pushdownRun is one coordinator query of a plan and its rows.
type pushdownRun struct {
	slices    []query.Slice
	finalSQL  string
	finalArgs []any
	rows      []pushdownRow
	found     bool
	bad       bool
	err       error
}

// pushdownRow: a series key, its By label values, a step and its value.
type pushdownRow struct {
	key    string
	values []string
	t      int64
	value  float64
}

// Range functions evaluated where the samples are; rate/increase treat the
// series as counters.
var pushdownFunctions = map[string]bool{
	"rate": true, "increase": true,
	"sum_over_time": true, "avg_over_time": true, "count_over_time": true,
	"min_over_time": true, "max_over_time": true, metrics_core.FunctionInstant: true,
}

var pushdownAggregations = map[string]bool{"": true, "sum": true, "avg": true, "min": true, "max": true, "count": true}

// pushdownFragmentSQL turns a fragment's samples into one partial per
// series and step whose window (t - range, t] holds any of them: count,
// first and last samples, counter resets inside the window, sum, min, max,
// whether two samples share a millisecond, and the latest stale marker.
// Each sample is copied to the steps it falls in (at most
// maxStepsPerSample) and grouped; series are keyed by hash(labels) - a
// window frame per row, or keying by the labels string, was 3-5x slower.
// Stale markers are left out of everything else, as the engine leaves them
// out of range vectors; only an instant selector looks at them (one that
// is the latest sample hides the series). A partial can have only a stale
// marker (n = 0). A sample repeated with its timestamp and value (a batch
// redelivered into another segment) counts once, as the engine keeps one;
// a different value at the same millisecond can't be ordered (dup).
//
// s is read twice (samples, labels) rather than materialized with every
// row's labels.
//
// %[1]s: WHERE clause; %[2]d: start ms; %[3]d: step ms; %[4]d: the
// slice's last step index; %[5]d: range ms - 1; %[6]d: its first step index.
const pushdownFragmentSQL = `WITH s AS NOT MATERIALIZED (
	SELECT hash(labels) AS id, labels, epoch_ms(timestamp) AS t, value,
		CASE WHEN isnan(value) THEN obzly_is_stale(value) ELSE false END AS stale
	FROM ` + query.SourcePlaceholder + ` WHERE %[1]s
), r AS (
	SELECT id, t, value, stale,
		CASE WHEN value < lag(value) OVER w AND NOT isnan(lag(value) OVER w) THEN lag(value) OVER w ELSE 0 END AS reset,
		coalesce(t = lag(t) OVER w AND value IS DISTINCT FROM lag(value) OVER w, false) AS dup,
		coalesce(t = lag(t) OVER w AND value IS NOT DISTINCT FROM lag(value) OVER w, false) AS repeat
	FROM s WINDOW w AS (PARTITION BY id, stale ORDER BY t)
), x AS (
	SELECT id, t, value, stale, reset, dup, repeat, unnest(generate_series(
		greatest(%[6]d, ceil((t - %[2]d) / %[3]d))::BIGINT,
		least(%[4]d, floor((t + %[5]d - %[2]d) / %[3]d))::BIGINT)) AS k
	FROM r
), a AS (
	SELECT id, k, count(*) FILTER (WHERE NOT stale AND NOT repeat) AS n,
		min(t) FILTER (WHERE NOT stale) AS first_t, arg_min(value, t) FILTER (WHERE NOT stale) AS first_v,
		max(t) FILTER (WHERE NOT stale) AS last_t, arg_max(value, t) FILTER (WHERE NOT stale) AS last_v,
		coalesce(sum(reset) FILTER (WHERE NOT stale) - arg_min(reset, t) FILTER (WHERE NOT stale), 0) AS resets,
		sum(value) FILTER (WHERE NOT stale AND NOT repeat) AS sum, min(value) FILTER (WHERE NOT stale) AS min,
		max(CASE WHEN isnan(value) THEN NULL ELSE value END) FILTER (WHERE NOT stale) AS max,
		bool_or(dup) AS dup, max(t) FILTER (WHERE stale) AS stale_t
	FROM x GROUP BY id, k
), l AS (
	SELECT id, any_value(labels) AS labels FROM s GROUP BY id
)
SELECT l.labels, %[2]d + %[3]d * a.k AS t, n, first_t, first_v, last_t, last_v, resets, sum, min, max, dup, stale_t
FROM a JOIN l USING (id)`

// maxStepsPerSample: the most steps one sample may be copied to (range /
// step, e.g. rate(x[1h]) at a 1 m step is 61); beyond it the engine reads
// the samples instead.
const maxStepsPerSample = 32

// pushdownSingleSQL names a slice's partials "merged": a slice reads every
// file its steps need, so each series and step has one partial.
const pushdownSingleSQL = `WITH merged AS (
	SELECT labels, t, n, first_t, first_v, last_t, last_v, resets, sum, min, max, stale_t, dup AS bad
	FROM (%s)
)`

// pushdownMergeSQL merges time chunks' partials (disjoint at the
// millisecond) into "merged", one row per series and step: counts and sums
// add, the earliest first and latest last win, and a counter reset where a
// chunk's first value drops below the previous chunk's last counts as the
// lag() inside one chunk would have. Partials that overlap in time (or
// share a millisecond) can't be ordered, so they mark the step bad and
// storage declines.
const pushdownMergeSQL = `WITH p AS (
	SELECT *, lag(last_t) OVER w AS prev_t, lag(last_v) OVER w AS prev_v
	FROM ` + query.ResultsPlaceholder + ` WINDOW w AS (PARTITION BY labels, t ORDER BY first_t, last_t)
), merged AS (
	SELECT labels, t, sum(n)::BIGINT AS n,
		min(first_t) AS first_t, arg_min(first_v, first_t) AS first_v,
		max(last_t) AS last_t, arg_max(last_v, last_t) AS last_v,
		sum(resets + CASE WHEN first_v < prev_v AND NOT isnan(prev_v) THEN prev_v ELSE 0 END) AS resets,
		sum(sum) AS sum, min(min) AS min, max(max) AS max, max(stale_t) AS stale_t,
		bool_or(dup OR coalesce(first_t <= prev_t, false)) AS bad
	FROM p GROUP BY labels, t
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
	// Aligned steps read exact partials, never the approximate rollups.
	aligned := partialsAligned(request, stepMs, lastStep)
	rollupStep, rollupRange := request.StepMs, request.RangeMs
	if aligned {
		rollupStep, rollupRange = 0, 0
	}
	sources, err := q.sampleSources(projectID, from, to, records, tail, rollupStep, rollupRange,
		segments.PruningHints{MetricNames: []string{name}})
	if err != nil {
		return nil, false, err
	}
	if len(sources) != 1 {
		return nil, false, nil
	}
	var plan *pushdownPlan
	if aligned {
		if plan, err = q.partialsPlan(projectID, request, stepMs, lastStep, sources[0], name); err != nil {
			return nil, false, err
		}
	}
	if plan == nil {
		if plan, err = q.rollupPlan(projectID, request, stepMs, lastStep, sources[0], name); err != nil {
			return nil, false, err
		}
	}
	if plan == nil {
		whole, err := q.pushdownPlan(projectID, request, stepMs, lastStep, sources[0])
		if err != nil {
			return nil, false, err
		}
		plan = &whole
	}
	finalSQL, finalArgs := pushdownFinalSQL(pushdownMergeSQL, request)
	requests := []pushdownRun{
		{slices: plan.slices(false), finalSQL: "SELECT * FROM " + query.ResultsPlaceholder},
		{slices: plan.slices(true), finalSQL: finalSQL, finalArgs: finalArgs},
	}
	// Whole slices and merged chunks answer different steps: run both at once.
	var wg sync.WaitGroup
	for i := range requests {
		if len(requests[i].slices) == 0 {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			requests[i].run(ctx, q, len(request.By))
		}()
	}
	wg.Wait()

	found, bad, points := false, false, 0
	for _, run := range requests {
		if run.err != nil {
			return nil, false, run.err
		}
		found, bad, points = found || run.found, bad || run.bad, points+len(run.rows)
	}
	if points > maxSeriesRows {
		return nil, false, tooManyPoints()
	}
	if bad {
		return nil, false, nil
	}
	if !found {
		return &metrics_core.RangeFunctionResponseDTO{Series: []metrics_core.RangeSeriesDTO{}}, true, nil
	}

	bySeries := map[string]*metrics_core.RangeSeriesDTO{}
	for _, run := range requests {
		for _, row := range run.rows {
			series, ok := bySeries[row.key]
			if !ok {
				series = &metrics_core.RangeSeriesDTO{Labels: resultLabels(request, row.key, row.values)}
				bySeries[row.key] = series
			}
			series.Points = append(series.Points, metrics_core.PointDTO{TimestampMs: row.t, Value: row.value})
		}
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

// pushdownPlan splits request over the executors (everything in one whole
// slice without remote ones).
//
// Steps before the recent boundary (recentBoundary) need no local file:
// they split into one contiguous slice per remote executor, each reading
// every file overlapping (its first step - range, its last step] and
// computing its steps whole - function and aggregation - so the
// coordinator only concatenates. With fewer steps than executors (an
// instant query, a few steps over long windows) one slice would read the
// whole window on one executor while the rest idle, so each slice's window
// is also cut in time into chunks, one per spare executor, returning
// partials the coordinator merges (pushdownMergeSQL).
//
// Steps from the boundary on need the local files, which only this process
// reads, and an in-process read of cold segments has no warm cache behind
// it: a week-long window of them outlasted the query. So their window is
// cut at the boundary - in-process only from it on - and what comes before
// is chunked over the remote executors.
func (q *MetricQuerier) pushdownPlan(
	projectID uuid.UUID,
	request *metrics_core.RangeFunctionRequestDTO,
	stepMs, lastStep int64,
	source sampleSource,
) (pushdownPlan, error) {
	builder := planBuilder{q: q, projectID: projectID, request: request, source: source, stepMs: stepMs}
	executors := int64(q.coordinator.RemoteExecutors())
	recent, known := recentBoundary(source)
	if executors == 0 || !known {
		return builder.steps(0, lastStep, max(executors, 1))
	}
	localStep := lastStep + 1
	if recent <= request.StartMs {
		localStep = 0
	} else if recent <= request.EndMs {
		localStep = (recent - request.StartMs + stepMs - 1) / stepMs
	}
	plan := pushdownPlan{}
	if localStep > 0 {
		remote, err := builder.steps(0, localStep-1, executors)
		if err != nil {
			return plan, err
		}
		plan.planned = append(plan.planned, remote.planned...)
	}
	if localStep > lastStep {
		return plan, nil
	}
	fromMs := request.StartMs + localStep*stepMs - request.RangeMs + 1
	toMs := request.StartMs + lastStep*stepMs
	if recent <= fromMs {
		err := builder.add(&plan, msWindow{fromMs, toMs}, localStep, lastStep, false)
		return plan, err
	}
	windows := append(timeChunks(fromMs, recent-1, executors, source), msWindow{recent, toMs})
	for _, window := range windows {
		if err := builder.add(&plan, window, localStep, lastStep, true); err != nil {
			return plan, err
		}
	}
	return plan, nil
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
	case metrics_core.FunctionInstant:
		// A stale marker as the latest sample hides the series at that step.
		sqlText += ", v AS (SELECT labels, t, bad, last_v AS value FROM merged" +
			" WHERE n > 0 AND (stale_t IS NULL OR stale_t < last_t))"
	default:
		value := map[string]string{
			"sum_over_time":   "sum",
			"avg_over_time":   "sum / n::DOUBLE",
			"count_over_time": "n::DOUBLE",
			"min_over_time":   "min",
			"max_over_time":   "coalesce(max, 'NaN'::DOUBLE)",
		}[request.Function]
		sqlText += ", v AS (SELECT labels, t, bad, " + value + " AS value FROM merged WHERE n > 0)"
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

// steps plans firstStep..lastStep over executors slices of contiguous
// steps, chunked in time when there are fewer steps than executors.
func (b planBuilder) steps(firstStep, lastStep, executors int64) (pushdownPlan, error) {
	plan := pushdownPlan{}
	steps := lastStep - firstStep + 1
	count := min(executors, steps)
	chunks := executors / count
	for i := range count {
		sliceFirst, sliceLast := firstStep+steps*i/count, firstStep+steps*(i+1)/count-1
		fromMs := b.request.StartMs + sliceFirst*b.stepMs - b.request.RangeMs + 1
		toMs := b.request.StartMs + sliceLast*b.stepMs
		windows := []msWindow{{fromMs, toMs}}
		if chunks > 1 {
			windows = timeChunks(fromMs, toMs, chunks, b.source)
		}
		for _, window := range windows {
			if err := b.add(&plan, window, sliceFirst, sliceLast, chunks > 1); err != nil {
				return plan, err
			}
		}
	}
	return plan, nil
}

// add plans a slice reading the files overlapping window, its samples
// only, for steps firstStep..lastStep: as partials, or computed whole.
func (b planBuilder) add(plan *pushdownPlan, window msWindow, firstStep, lastStep int64, partial bool) error {
	from := time.UnixMilli(window.fromMs).UTC()
	to := time.UnixMilli(window.toMs + 1).UTC().Add(-time.Microsecond)
	where, args, err := b.q.whereFor(b.projectID, from, to, b.request.Matchers)
	if err != nil {
		return err
	}
	slice := query.Slice{}
	for _, record := range b.source.records {
		if !record.MaxTime.Before(from) && !record.MinTime.After(to) {
			slice.Segments = append(slice.Segments, record)
		}
	}
	for _, file := range b.source.tail {
		if file.MaxTime.IsZero() || (!file.MaxTime.Before(from) && !file.MinTime.After(to)) {
			slice.LocalFiles = append(slice.LocalFiles, file)
		}
	}
	fragmentSQL := fmt.Sprintf(
		pushdownFragmentSQL,
		where,
		b.request.StartMs,
		b.stepMs,
		lastStep,
		b.request.RangeMs-1,
		firstStep,
	)
	if partial {
		slice.SQL, slice.Args = fragmentSQL, args
	} else {
		sliceSQL, finalArgs := pushdownFinalSQL(fmt.Sprintf(pushdownSingleSQL, fragmentSQL), b.request)
		slice.SQL = sliceSQL
		slice.Args = append(append([]any{}, args...), finalArgs...)
	}
	plan.planned = append(plan.planned, plannedSlice{slice: slice, window: window, partial: partial})
	return nil
}

// slices is the plan's partial or whole slices.
func (p pushdownPlan) slices(partial bool) []query.Slice {
	var result []query.Slice
	for _, planned := range p.planned {
		if planned.partial == partial {
			result = append(result, planned.slice)
		}
	}
	return result
}

// run runs r's slices and keeps their rows; bad stops at the first bad row.
func (r *pushdownRun) run(ctx context.Context, q *MetricQuerier, groups int) {
	r.found, r.err = q.run(ctx, query.Request{
		Slices:    r.slices,
		FinalSQL:  r.finalSQL,
		FinalArgs: r.finalArgs,
	}, func(rows *sql.Rows) error {
		for rows.Next() {
			row := pushdownRow{values: make([]string, groups)}
			var rowBad bool
			dest := make([]any, 0, groups+4)
			dest = append(dest, &row.key)
			for i := range row.values {
				dest = append(dest, &row.values[i])
			}
			dest = append(dest, &row.t, &row.value, &rowBad)
			if err := rows.Scan(dest...); err != nil {
				return fmt.Errorf("failed to scan range function row: %w", err)
			}
			if rowBad {
				r.bad = true
				return nil
			}
			r.rows = append(r.rows, row)
			if len(r.rows) > maxSeriesRows {
				return tooManyPoints()
			}
		}
		return nil
	})
}

// recentBoundary is the earliest sample time (ms) of the files only this
// process reads - the query-time tail and segments not uploaded yet - or
// MaxInt64 when there are none; false when a tail file's times are unknown.
func recentBoundary(source sampleSource) (int64, bool) {
	recent := int64(math.MaxInt64)
	for _, file := range source.tail {
		if file.MaxTime.IsZero() {
			return 0, false
		}
		recent = min(recent, file.MinTime.UnixMilli())
	}
	for _, record := range source.records {
		if record.ObjectKey == "" {
			recent = min(recent, record.MinTime.UnixMilli())
		}
	}
	return recent, true
}

// timeChunks cuts [fromMs, toMs] into at most n windows that share no
// millisecond and together cover it. The cuts split the part the files
// actually span evenly, so no executor gets a stretch without samples; no
// more windows than files, since each would read every file it overlaps.
// A cut moves to the nearest file edge within half a chunk of it (past the
// previous cut): a file split between two chunks is read (and cached) by
// two executors, so whole files beat equal lengths.
func timeChunks(fromMs, toMs, n int64, source sampleSource) []msWindow {
	lo, hi := fromMs, toMs
	first, last, files := filesSpan(source, fromMs, toMs)
	if files > 0 {
		lo, hi = max(lo, first), min(hi, last)
		n = min(n, files)
	}
	if hi < lo || n <= 1 {
		return []msWindow{{fromMs, toMs}}
	}
	n = min(n, hi-lo+1)
	edges := fileEdges(source, lo, hi)
	tolerance := (hi - lo + 1) / n / 2
	windows := make([]msWindow, 0, n)
	start := fromMs
	for j := int64(1); j < n; j++ {
		cut := lo + (hi-lo+1)*j/n
		if edge, ok := nearestEdge(edges, cut, tolerance, start); ok {
			cut = edge
		}
		if cut <= start {
			continue
		}
		windows = append(windows, msWindow{start, cut - 1})
		start = cut
	}
	return append(windows, msWindow{start, toMs})
}

// fileEdges is where the files start and end (the millisecond after their
// last sample), ascending, inside (lo, hi].
func fileEdges(source sampleSource, lo, hi int64) []int64 {
	var edges []int64
	add := func(edge int64) {
		if edge > lo && edge <= hi {
			edges = append(edges, edge)
		}
	}
	for _, record := range source.records {
		add(record.MinTime.UnixMilli())
		add(record.MaxTime.UnixMilli() + 1)
	}
	for _, file := range source.tail {
		if !file.MaxTime.IsZero() {
			add(file.MinTime.UnixMilli())
			add(file.MaxTime.UnixMilli() + 1)
		}
	}
	slices.Sort(edges)
	return slices.Compact(edges)
}

// nearestEdge is the edge after start closest to cut, if one is within
// tolerance.
func nearestEdge(edges []int64, cut, tolerance, start int64) (int64, bool) {
	i := sort.Search(len(edges), func(i int) bool { return edges[i] >= cut })
	best, found := int64(0), false
	for _, j := range []int{i - 1, i} {
		if j < 0 || j >= len(edges) || edges[j] <= start {
			continue
		}
		distance := max(edges[j]-cut, cut-edges[j])
		if distance <= tolerance && (!found || distance < max(best-cut, cut-best)) {
			best, found = edges[j], true
		}
	}
	return best, found
}

// filesSpan is the earliest and latest sample time (ms) of the files
// overlapping [fromMs, toMs], and how many there are; 0 files when a file's
// times are unknown.
func filesSpan(source sampleSource, fromMs, toMs int64) (int64, int64, int64) {
	from := time.UnixMilli(fromMs).UTC()
	to := time.UnixMilli(toMs + 1).UTC().Add(-time.Microsecond)
	first, last, files := int64(math.MaxInt64), int64(math.MinInt64), int64(0)
	add := func(minTime, maxTime time.Time) {
		if maxTime.Before(from) || minTime.After(to) {
			return
		}
		first, last = min(first, minTime.UnixMilli()), max(last, maxTime.UnixMilli())
		files++
	}
	for _, record := range source.records {
		add(record.MinTime, record.MaxTime)
	}
	for _, file := range source.tail {
		if file.MaxTime.IsZero() {
			return 0, 0, 0
		}
		add(file.MinTime, file.MaxTime)
	}
	return first, last, files
}

func tooManyPoints() error {
	return &metrics_core.LimitError{Message: fmt.Sprintf(
		"query returns more than %d points; narrow the time range or matchers", maxSeriesRows)}
}
