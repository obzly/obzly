package metrics_querier

import (
	"fmt"
	"time"

	metrics_core "obzly/internal/features/metrics/core"
	"obzly/internal/query"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
)

// rollupFunctions are the range functions a rollup's per-bucket count, sum
// and min answer exactly. Not max (a bucket's max is NaN when any sample
// is, where the function skips NaN) nor rate/increase (resets inside a
// bucket are lost).
var rollupFunctions = map[string]bool{
	"sum_over_time": true, "count_over_time": true, "avg_over_time": true, "min_over_time": true,
}

// minRollupBuckets: a window reads a rollup only when it spans this many of
// its buckets; shorter ones gain little over the raw samples.
const minRollupBuckets = 12

// pushdownRollupSQL is one partial (the columns of pushdownFragmentSQL) per
// series from rollup buckets: counts and sums add, the earliest first and
// latest last sample win. Duplicate timestamps can't be seen in buckets, so
// dup is false. %[1]d: the step's time; %[2]s: WHERE clause.
const pushdownRollupSQL = `SELECT labels, %[1]d::BIGINT AS t, sum("count")::BIGINT AS n,
	epoch_ms(min(first_ts)) AS first_t, arg_min(first_value, first_ts) AS first_v,
	epoch_ms(max(last_ts)) AS last_t, arg_max(last_value, last_ts) AS last_v,
	0::DOUBLE AS resets, sum("sum") AS sum, min(min_value) AS min,
	max(CASE WHEN isnan(max_value) THEN NULL ELSE max_value END) AS max, false AS dup,
	NULL::BIGINT AS stale_t
FROM ` + query.SourcePlaceholder + ` WHERE %[2]s GROUP BY labels`

// rollupWindowStream is the coarsest rollup whose buckets fit
// minRollupBuckets times in rangeMs ("" for none).
func rollupWindowStream(rangeMs int64) (string, int64) {
	for _, stream := range []string{"metrics_1h", "metrics_5m"} {
		width := segments.RollupStreams[stream].Milliseconds()
		if rangeMs >= minRollupBuckets*width {
			return stream, width
		}
	}
	return "", 0
}

// rollupPlan answers a one-step request over a long window (the Synthetics
// 7-day uptime) from rollups where it can: the whole buckets inside the
// window come from the cold segments' rollups, a few hundred rows per
// series a week, and only the edges - under a bucket at the start, the
// part not rolled up yet at the end - from samples. Read raw, a week is
// every sample of every segment, which on executors with a cold cache
// (after a deploy) took ~30 s. All parts are partials the coordinator
// merges (pushdownMergeSQL). nil when there is no such plan: more steps,
// another function, too short a window, or not enough rolled up.
func (q *MetricQuerier) rollupPlan(
	projectID uuid.UUID,
	request *metrics_core.RangeFunctionRequestDTO,
	stepMs, lastStep int64,
	source sampleSource,
	name string,
) (*pushdownPlan, error) {
	if lastStep != 0 || !rollupFunctions[request.Function] {
		return nil, nil
	}
	stream, width := rollupWindowStream(request.RangeMs)
	if stream == "" {
		return nil, nil
	}
	recent, known := recentBoundary(source)
	if !known {
		return nil, nil
	}
	fromMs, toMs := request.StartMs-request.RangeMs+1, request.StartMs
	rollups, err := q.segmentRepo.PlanSegments(projectID, stream,
		time.UnixMilli(fromMs).UTC(), time.UnixMilli(toMs).UTC(), segments.PruningHints{MetricNames: []string{name}})
	if err != nil {
		return nil, fmt.Errorf("failed to list rollup segments for project: %w", err)
	}
	firstBucket, endBucket := rollupSpan(source, rollups, fromMs, min(toMs+1, recent), width)
	if endBucket-firstBucket < 2*width {
		return nil, nil
	}

	builder := planBuilder{q: q, projectID: projectID, request: request, source: source, stepMs: stepMs}
	plan := &pushdownPlan{}
	if firstBucket > fromMs {
		if err := builder.add(plan, msWindow{fromMs, firstBucket - 1}, 0, 0, true); err != nil {
			return nil, err
		}
	}
	err = q.addRollupSlice(plan, projectID, request, source, rollups, msWindow{firstBucket, endBucket - 1})
	if err != nil {
		return nil, err
	}
	if endBucket > toMs {
		return plan, nil
	}
	executors := int64(max(q.coordinator.RemoteExecutors(), 1))
	var windows []msWindow
	if recent > endBucket && recent <= toMs {
		windows = append(timeChunks(endBucket, recent-1, executors, source), msWindow{recent, toMs})
	} else {
		windows = timeChunks(endBucket, toMs, executors, source)
	}
	for _, window := range windows {
		if err := builder.add(plan, window, 0, 0, true); err != nil {
			return nil, err
		}
	}
	return plan, nil
}

// rollupSpan is the buckets [first, end) the rollups can answer inside
// [fromMs, limitMs): whole buckets only, and none holding samples of a
// segment without a rollup (not rolled up yet, or replaced by compaction
// since) - those and everything after come from samples. Each segment's
// rollup is read once, so an extra copy of one can't count twice.
func rollupSpan(source sampleSource, rollups []*segments.SegmentRecord, fromMs, limitMs, width int64) (int64, int64) {
	first := (fromMs + width - 1) / width * width
	end := limitMs / width * width
	rolled := map[string]bool{}
	for _, rollup := range rollups {
		if rollup.Stats != nil && rollup.Stats.Source != "" {
			rolled[rollup.Stats.Source] = true
		}
	}
	for _, record := range source.records {
		if rolled[record.ID.String()] || record.MaxTime.UnixMilli() < first {
			continue
		}
		end = min(end, record.MinTime.UnixMilli()/width*width)
	}
	return first, end
}

// addRollupSlice plans one partial over the buckets in window, from one
// rollup per planned segment (a rollup whose segment compaction replaced
// would count its samples twice).
func (q *MetricQuerier) addRollupSlice(
	plan *pushdownPlan,
	projectID uuid.UUID,
	request *metrics_core.RangeFunctionRequestDTO,
	source sampleSource,
	rollups []*segments.SegmentRecord,
	window msWindow,
) error {
	clause, args, err := matcherSQL(request.Matchers)
	if err != nil {
		return err
	}
	where := "project_id = ? AND epoch_ms(bucket) >= ? AND epoch_ms(bucket) <= ?"
	if clause != "" {
		where += " AND " + clause
	}
	slice := query.Slice{
		SQL:  fmt.Sprintf(pushdownRollupSQL, request.StartMs, where),
		Args: append([]any{projectID.String(), window.fromMs, window.toMs}, args...),
	}
	from, to := time.UnixMilli(window.fromMs).UTC(), time.UnixMilli(window.toMs).UTC()
	planned := make(map[string]bool, len(source.records))
	for _, record := range source.records {
		planned[record.ID.String()] = true
	}
	seen := map[string]bool{}
	for _, rollup := range rollups {
		if rollup.Stats == nil || !planned[rollup.Stats.Source] || seen[rollup.Stats.Source] ||
			rollup.MaxTime.Before(from) || rollup.MinTime.After(to) {
			continue
		}
		seen[rollup.Stats.Source] = true
		slice.Segments = append(slice.Segments, rollup)
	}
	if len(slice.Segments) > 0 {
		plan.planned = append(plan.planned, plannedSlice{slice: slice, window: window, partial: true})
	}
	return nil
}
