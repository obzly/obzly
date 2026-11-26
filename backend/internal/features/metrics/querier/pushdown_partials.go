package metrics_querier

import (
	"fmt"
	"strings"
	"time"

	metrics_core "obzly/internal/features/metrics/core"
	"obzly/internal/query"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
)

// pushdownPartialsSQL is pushdownFragmentSQL's partials (same columns) from
// partial buckets instead of samples: a bucket (e - width, e] goes to every
// step whose window (t - range, t] holds it whole, which with t and range
// multiples of the width is every bucket it overlaps. A step's buckets
// merge as pushdownMergeSQL merges time chunks: counts and sums add, the
// earliest first and latest last win, and a drop from one bucket's last
// value to the next one's first is a counter reset. b is read twice
// (buckets, labels) rather than materialized with every row's labels.
//
// %[1]s: WHERE clause; %[2]d: start ms; %[3]d: step ms; %[4]d: the
// slice's last step index; %[5]d: range ms - bucket width ms; %[6]d: its
// first step index.
const pushdownPartialsSQL = `WITH b AS NOT MATERIALIZED (
	SELECT hash(labels) AS id, labels, epoch_ms(timestamp) AS e,
		n, first_t, first_v, last_t, last_v, resets, sum, min, max, dup, stale_t
	FROM ` + query.SourcePlaceholder + ` WHERE %[1]s
), x AS (
	SELECT *, unnest(generate_series(
		greatest(%[6]d, ceil((e - %[2]d) / %[3]d))::BIGINT,
		least(%[4]d, floor((e + %[5]d - %[2]d) / %[3]d))::BIGINT)) AS k
	FROM b
), p AS (
	SELECT *, lag(last_t) OVER w AS prev_t, lag(last_v) OVER w AS prev_v
	FROM x WINDOW w AS (PARTITION BY id, k ORDER BY first_t, last_t)
), a AS (
	SELECT id, k, sum(n)::BIGINT AS n,
		min(first_t) AS first_t, arg_min(first_v, first_t) AS first_v,
		max(last_t) AS last_t, arg_max(last_v, last_t) AS last_v,
		sum(resets + CASE WHEN first_v < prev_v AND NOT isnan(prev_v) THEN prev_v ELSE 0 END) AS resets,
		sum(sum) AS sum, min(min) AS min, max(max) AS max,
		bool_or(dup OR coalesce(first_t <= prev_t, false)) AS dup, max(stale_t) AS stale_t
	FROM p GROUP BY id, k
), l AS (
	SELECT id, any_value(labels) AS labels FROM b GROUP BY id
)
SELECT l.labels, %[2]d + %[3]d * a.k AS t, n, first_t, first_v, last_t, last_v, resets, sum, min, max, dup, stale_t
FROM a JOIN l USING (id)`

// maxLagBuckets: the most buckets a window may span for the lag fragment
// (pushdownPartialsLagSQL); longer ones copy buckets to steps.
const maxLagBuckets = 8

// pushdownPartialsLagSQL is pushdownPartialsSQL for windows longer than
// the step, of up to maxLagBuckets buckets (rate(x[5m]) at a 1 m step):
// copying each bucket to every step whose window holds it - range/step
// copies - ran a 24 h histogram p95 out of memory, and window frames over
// its 24M 1-minute buckets took minutes. Instead each series' buckets are
// sorted once and each reads the buckets before it with lag(): a series
// has at most one bucket per end, so the window (e - range, e] is the
// current bucket and the lagged ones still inside it. A bucket followed by
// a gap also answers the bucket ends in the gap whose windows still hold
// it (k > 0: a series that stopped). The window's buckets merge as
// pushdownPartialsSQL merges a step's: counts and sums add, the earliest
// non-empty first and latest last win, and a drop between consecutive
// non-empty buckets is a reset. b is read twice (buckets, labels) rather
// than materialized: holding 24M rows' labels took minutes.
//
// %[1]s: WHERE clause; %[2]d: start ms; %[3]d: step ms; %[4]d: the
// slice's last step index; %[5]d: range ms; %[6]d: its first step index;
// %[7]d: bucket width ms; %[8]s: the lagged columns; %[9]s: the window's
// partial columns.
const pushdownPartialsLagSQL = `WITH b AS NOT MATERIALIZED (
	SELECT series_id AS id, labels, epoch_ms(timestamp) AS e,
		n, first_t, first_v, last_t, last_v, resets, sum, min, max, dup, stale_t
	FROM ` + query.SourcePlaceholder + ` WHERE %[1]s
), x AS (
	SELECT id, e, n AS n_0, first_t AS first_t_0, first_v AS first_v_0, last_t AS last_t_0, last_v AS last_v_0,
		resets AS resets_0, sum AS sum_0, min AS min_0, max AS max_0, dup AS dup_0, stale_t AS stale_t_0,
		lead(e) OVER s AS next_e%[8]s
	FROM b WINDOW s AS (PARTITION BY id ORDER BY e)
), y AS (
	SELECT *, unnest(generate_series(0,
		least((coalesce(next_e, e + %[5]d) - e) // %[7]d - 1, (%[5]d - %[7]d) // %[7]d))) AS k
	FROM x
), f AS (
	SELECT id, e + k * %[7]d AS t, %[9]s FROM y
	WHERE e + k * %[7]d >= %[2]d + %[3]d * %[6]d AND e + k * %[7]d <= %[2]d + %[3]d * %[4]d
		AND (e + k * %[7]d - %[2]d) %% %[3]d = 0
), l AS (
	SELECT id, any_value(labels) AS labels FROM b GROUP BY id
)
SELECT l.labels, f.t, n, first_t, first_v, last_t, last_v, resets, sum, min, max, dup, stale_t
FROM f JOIN l USING (id)
WHERE n > 0 OR stale_t IS NOT NULL`

// partialsLagColumns are the bucket columns pushdownPartialsLagSQL lags.
var partialsLagColumns = []string{
	"e", "n", "first_t", "first_v", "last_t", "last_v", "resets", "sum", "min", "max", "dup", "stale_t",
}

// pushdownPartialsLag is pushdownPartialsLagSQL for windows of buckets
// buckets.
func pushdownPartialsLag(
	where string, startMs, stepMs, last, rangeMs, first, widthMs, buckets int64,
) string {
	var lagged strings.Builder
	for j := int64(1); j < buckets; j++ {
		for _, column := range partialsLagColumns {
			fmt.Fprintf(&lagged, ", lag(%[1]s, %[2]d) OVER s AS %[1]s_%[2]d", column, j)
		}
	}
	// in(j): bucket j (0 the current, j the j-th before) is inside the
	// window ending k buckets after the current; full(j): and non-empty.
	in := func(j int64) string {
		if j == 0 {
			return "true"
		}
		return fmt.Sprintf("coalesce(e_%d > e + k * %d - %d, false)", j, widthMs, rangeMs)
	}
	full := func(j int64) string { return fmt.Sprintf("(%s AND coalesce(n_%d, 0) > 0)", in(j), j) }
	each := func(separator string, format func(j int64) string) string {
		parts := make([]string, buckets)
		for j := range buckets {
			parts[j] = format(j)
		}
		return strings.Join(parts, separator)
	}
	pick := func(column string, earliest bool) string {
		var b strings.Builder
		b.WriteString("CASE")
		for i := range buckets {
			j := i
			if earliest {
				j = buckets - 1 - i
			}
			fmt.Fprintf(&b, " WHEN %s THEN %s_%d", full(j), column, j)
		}
		b.WriteString(" END")
		return b.String()
	}
	// The drop into non-empty bucket j from the non-empty one before it.
	drop := func(j int64) string {
		if j == buckets-1 {
			return "0"
		}
		var b strings.Builder
		b.WriteString("CASE")
		for i := j + 1; i < buckets; i++ {
			fmt.Fprintf(&b, " WHEN %[1]s THEN CASE WHEN first_v_%[2]d < last_v_%[3]d AND NOT isnan(last_v_%[3]d)"+
				" THEN last_v_%[3]d ELSE 0 END", full(i), j, i)
		}
		b.WriteString(" ELSE 0 END")
		return b.String()
	}
	columns := []string{
		each(" + ", func(j int64) string {
			return fmt.Sprintf("CASE WHEN %s THEN coalesce(n_%d, 0) ELSE 0 END", in(j), j)
		}) + " AS n",
		pick("first_t", true) + " AS first_t",
		pick("first_v", true) + " AS first_v",
		pick("last_t", false) + " AS last_t",
		pick("last_v", false) + " AS last_v",
		each(" + ", func(j int64) string {
			return fmt.Sprintf("CASE WHEN %s THEN resets_%d + %s ELSE 0 END", full(j), j, drop(j))
		}) + " AS resets",
		each(" + ", func(j int64) string {
			return fmt.Sprintf("coalesce(CASE WHEN %s THEN sum_%d END, 0)", full(j), j)
		}) + " AS sum",
		"least(" + each(", ", func(j int64) string {
			return fmt.Sprintf("CASE WHEN %s THEN min_%d END", full(j), j)
		}) + ") AS min",
		"greatest(" + each(", ", func(j int64) string {
			return fmt.Sprintf("CASE WHEN %s THEN max_%d END", full(j), j)
		}) + ") AS max",
		"(" + each(" OR ", func(j int64) string {
			return fmt.Sprintf("coalesce(%s AND dup_%d, false)", in(j), j)
		}) + ") AS dup",
		"greatest(" + each(", ", func(j int64) string {
			return fmt.Sprintf("CASE WHEN %s THEN stale_t_%d END", in(j), j)
		}) + ") AS stale_t",
	}
	return fmt.Sprintf(pushdownPartialsLagSQL, where, startMs, stepMs, last, rangeMs, first, widthMs,
		lagged.String(), strings.Join(columns, ",\n\t\t"))
}

// partialsTier is the coarsest partials tier whose buckets every step's
// window is made of - start, step (unless there is one step) and range are
// multiples of its width - among those the newest hour of the window still
// has (the 1-minute tier expires first; hours past it are read from
// samples).
func partialsTier(
	request *metrics_core.RangeFunctionRequestDTO, stepMs, lastStep int64,
) (segments.MetricPartialTier, bool) {
	newest := time.UnixMilli(segments.MetricPartialsPeriodEnd(request.StartMs + lastStep*stepMs)).UTC()
	for _, tier := range segments.MetricPartialTiersFor(newest, time.Now().UTC()) {
		width := tier.Width.Milliseconds()
		if request.RangeMs >= width && request.RangeMs%width == 0 && request.StartMs%width == 0 &&
			(lastStep == 0 || stepMs%width == 0) {
			return tier, true
		}
	}
	return segments.MetricPartialTier{}, false
}

// partialsAligned: some partials tier answers request exactly.
func partialsAligned(request *metrics_core.RangeFunctionRequestDTO, stepMs, lastStep int64) bool {
	_, ok := partialsTier(request, stepMs, lastStep)
	return ok
}

// partialsPlan answers an aligned request from the hours whose partials
// are current - folded from exactly the segments overlapping the hour
// now, with no tail file in it - and from samples for the other hours (not
// folded yet, or changed since). Runs of steps whose windows lie in
// current hours are computed whole from partials, split over the
// executors; the other runs merge partials and the other hours' samples in
// the coordinator (they never overlap in time). nil when no hour is
// current.
func (q *MetricQuerier) partialsPlan(
	projectID uuid.UUID,
	request *metrics_core.RangeFunctionRequestDTO,
	stepMs, lastStep int64,
	source sampleSource,
	name string,
) (*pushdownPlan, error) {
	tier, ok := partialsTier(request, stepMs, lastStep)
	if !ok {
		return nil, nil
	}
	period := segments.MetricPartialsPeriod.Milliseconds()
	firstPeriod := segments.MetricPartialsPeriodEnd(request.StartMs - request.RangeMs + 1)
	lastPeriod := segments.MetricPartialsPeriodEnd(request.StartMs + lastStep*stepMs)
	spanFrom, spanTo := time.UnixMilli(firstPeriod-period+1).UTC(), time.UnixMilli(lastPeriod).UTC()
	hints := segments.PruningHints{MetricNames: []string{name}}
	partials, err := q.segmentRepo.PlanSegments(projectID, tier.Stream, spanFrom, spanTo, hints)
	if err != nil {
		return nil, fmt.Errorf("failed to list metric partials for project: %w", err)
	}
	if len(partials) == 0 {
		return nil, nil
	}
	// Every segment of the hours touched, not only of the window: an hour's
	// partials are current only if none of its segments is missing from
	// them.
	records, err := q.segmentRepo.PlanSegments(projectID, "metrics", spanFrom, spanTo, hints)
	if err != nil {
		return nil, fmt.Errorf("failed to list segments for project: %w", err)
	}
	covered := currentPeriods(partials, records, source.tail)
	if len(covered) == 0 {
		return nil, nil
	}
	coveredList := make([]*segments.SegmentRecord, 0, len(covered))
	for _, partial := range covered {
		coveredList = append(coveredList, partial)
	}
	raw := sampleSource{stream: source.stream, records: records, tail: source.tail, from: source.from}
	stepCovered := func(k int64) bool {
		t := request.StartMs + k*stepMs
		last := segments.MetricPartialsPeriodEnd(t)
		for end := segments.MetricPartialsPeriodEnd(t - request.RangeMs + 1); end <= last; end += period {
			if covered[end] == nil {
				return false
			}
		}
		return true
	}

	plan := &pushdownPlan{}
	executors := int64(max(q.coordinator.RemoteExecutors(), 1))
	for first := int64(0); first <= lastStep; {
		whole := stepCovered(first)
		last := first
		for last < lastStep && stepCovered(last+1) == whole {
			last++
		}
		if whole {
			steps := last - first + 1
			slices := min(executors, steps)
			for i := range slices {
				err := q.addPartialsSlice(plan, projectID, request, tier, stepMs, coveredList,
					first+steps*i/slices, first+steps*(i+1)/slices-1, false)
				if err != nil {
					return nil, err
				}
			}
		} else {
			if err := q.addPartialsSlice(
				plan,
				projectID,
				request,
				tier,
				stepMs,
				coveredList,
				first,
				last,
				true,
			); err != nil {
				return nil, err
			}
			builder := planBuilder{q: q, projectID: projectID, request: request, source: raw, stepMs: stepMs}
			window := msWindow{request.StartMs + first*stepMs - request.RangeMs + 1, request.StartMs + last*stepMs}
			for _, gap := range uncoveredWindows(window, covered) {
				if err := builder.add(plan, gap, first, last, true); err != nil {
					return nil, err
				}
			}
		}
		first = last + 1
	}
	return plan, nil
}

// currentPeriods maps each period end to its partials when they are
// current: every segment overlapping the period is among the ones they
// were folded from (a segment merged away is replaced by one that isn't),
// and no tail file holds samples there.
func currentPeriods(
	partials, records []*segments.SegmentRecord, tail []segments.ResolvedFile,
) map[int64]*segments.SegmentRecord {
	period := segments.MetricPartialsPeriod.Milliseconds()
	overlapping := func(minMs, maxMs int64, visit func(end int64)) {
		for end := segments.MetricPartialsPeriodEnd(minMs); end <= segments.MetricPartialsPeriodEnd(maxMs); end += period {
			visit(end)
		}
	}
	tailPeriods := map[int64]bool{}
	for _, file := range tail {
		if file.MaxTime.IsZero() {
			return nil
		}
		overlapping(file.MinTime.UnixMilli(), file.MaxTime.UnixMilli(), func(end int64) { tailPeriods[end] = true })
	}
	segmentsIn := map[int64][]string{}
	for _, record := range records {
		overlapping(record.MinTime.UnixMilli(), record.MaxTime.UnixMilli(), func(end int64) {
			segmentsIn[end] = append(segmentsIn[end], record.ID.String())
		})
	}
	covered := map[int64]*segments.SegmentRecord{}
	for _, partial := range partials {
		end := partial.MaxTime.UnixMilli()
		if partial.Stats == nil || tailPeriods[end] || covered[end] != nil {
			continue
		}
		folded := map[string]bool{}
		for _, id := range strings.Split(partial.Stats.Source, ",") {
			folded[id] = true
		}
		current := true
		for _, id := range segmentsIn[end] {
			current = current && folded[id]
		}
		if current {
			covered[end] = partial
		}
	}
	return covered
}

// uncoveredWindows is window without the covered periods, as windows.
func uncoveredWindows(window msWindow, covered map[int64]*segments.SegmentRecord) []msWindow {
	period := segments.MetricPartialsPeriod.Milliseconds()
	var gaps []msWindow
	for end := segments.MetricPartialsPeriodEnd(window.fromMs); end-period < window.toMs; end += period {
		if covered[end] != nil {
			continue
		}
		gap := msWindow{max(window.fromMs, end-period+1), min(window.toMs, end)}
		if n := len(gaps); n > 0 && gaps[n-1].toMs+1 == gap.fromMs {
			gaps[n-1].toMs = gap.toMs
			continue
		}
		gaps = append(gaps, gap)
	}
	return gaps
}

// addPartialsSlice plans steps first..last from the partial buckets they
// hold: as partials, or computed whole.
func (q *MetricQuerier) addPartialsSlice(
	plan *pushdownPlan,
	projectID uuid.UUID,
	request *metrics_core.RangeFunctionRequestDTO,
	tier segments.MetricPartialTier,
	stepMs int64,
	covered []*segments.SegmentRecord,
	first, last int64,
	partial bool,
) error {
	window := msWindow{request.StartMs + first*stepMs - request.RangeMs + 1, request.StartMs + last*stepMs}
	from, to := time.UnixMilli(window.fromMs).UTC(), time.UnixMilli(window.toMs).UTC()
	clause, matcherArgs, err := matcherSQL(request.Matchers)
	if err != nil {
		return err
	}
	where := "project_id = ? AND timestamp >= ? AND timestamp <= ?"
	if clause != "" {
		where += " AND " + clause
	}
	args := append([]any{projectID.String(), from, to}, matcherArgs...)
	slice := query.Slice{}
	for _, record := range covered {
		if !record.MaxTime.Before(from) && !record.MinTime.After(to) {
			slice.Segments = append(slice.Segments, record)
		}
	}
	if len(slice.Segments) == 0 {
		return nil
	}
	width := tier.Width.Milliseconds()
	fragmentSQL := fmt.Sprintf(pushdownPartialsSQL, where, request.StartMs, stepMs, last, request.RangeMs-width, first)
	if buckets := request.RangeMs / width; request.RangeMs > stepMs && buckets <= maxLagBuckets {
		fragmentSQL = pushdownPartialsLag(where, request.StartMs, stepMs, last, request.RangeMs, first, width, buckets)
	}
	if partial {
		slice.SQL, slice.Args = fragmentSQL, args
	} else {
		sliceSQL, finalArgs := pushdownFinalSQL(fmt.Sprintf(pushdownSingleSQL, fragmentSQL), request)
		slice.SQL, slice.Args = sliceSQL, append(args, finalArgs...)
	}
	plan.planned = append(plan.planned, plannedSlice{slice: slice, window: window, partial: partial})
	return nil
}
