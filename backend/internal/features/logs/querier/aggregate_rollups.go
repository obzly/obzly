package logs_querier

import (
	"context"
	"fmt"
	"sort"
	"time"

	logs_core "obzly/internal/features/logs/core"
	"obzly/internal/storage/segments"
	"obzly/internal/util/attrnames"

	"github.com/google/uuid"
)

// Bucketed aggregations read logs_1m per-minute counts where they can (whole
// minutes, counts' dimensions only). Partial edge minutes, segments without
// counts and the tail are read raw; the three are summed per bucket.

// rollupFields are logs_1m's dimensions.
var rollupFields = map[string]bool{
	"level":                    true,
	"service_name":             true,
	"k8s_namespace_name":       true,
	"k8s_container_name":       true,
	"k8s_pod_name":             true,
	logs_core.PatternHashField: true,
}

// rollupsApply reports whether request can be answered from logs_1m.
func (q *LogQuerier) rollupsApply(request *logs_core.AggregationRequestDTO) bool {
	if q.stream != logs_core.StreamLogs || request.BucketMs <= 0 || request.BucketMs%time.Minute.Milliseconds() != 0 {
		return false
	}
	if request.GroupField != "" && !rollupFields[attrnames.Normalize(request.GroupField)] {
		return false
	}
	return onRollupFields(request.Query)
}

func onRollupFields(node *logs_core.QueryNode) bool {
	switch {
	case node == nil:
		return true
	case node.Condition != nil:
		return rollupFields[attrnames.Normalize(node.Condition.Field)]
	case node.Logic != nil:
		for i := range node.Logic.Children {
			if !onRollupFields(&node.Logic.Children[i]) {
				return false
			}
		}
		return true
	}
	return true
}

// computeWithRollups is computePartitions over ranges using logs_1m for the
// whole minutes of segments that have per-minute counts.
func (q *LogQuerier) computeWithRollups(
	ctx context.Context,
	projectID uuid.UUID,
	request *logs_core.AggregationRequestDTO,
	from, to time.Time,
	tail []segments.ResolvedFile,
	ordered []*segments.SegmentRecord,
	whereClause string,
	whereArgs []any,
	plan aggregationPlan,
	size time.Duration,
	ranges []timeRange,
) (map[int64][]partitionCount, error) {
	grouped := request.GroupField != ""
	tokens := requiredTokens(request.Query)
	rollups, err := q.segmentRepo.PlanSegments(projectID, logs_core.StreamLogsRollup, from, to, segments.PruningHints{})
	if err != nil {
		return nil, fmt.Errorf("failed to list log rollups: %w", err)
	}
	bySource := q.readableRollups(rollups)
	var covered, uncovered, rollupRecords []*segments.SegmentRecord
	for _, record := range ordered {
		if rollup := bySource[record.ID.String()]; rollup != nil {
			covered = append(covered, record)
			rollupRecords = append(rollupRecords, rollup)
		} else {
			uncovered = append(uncovered, record)
		}
	}
	if len(covered) == 0 {
		return q.queryRanges(ctx, q.stream, grouped, tail, ordered, whereClause, whereArgs, plan, size, ranges,
			request.ScanBudget, tokens)
	}

	inner, edges := splitWholeMinutes(ranges)
	totals := map[int64]map[partitionCount]int64{}
	add := func(computed map[int64][]partitionCount) {
		for part, counts := range computed {
			if totals[part] == nil {
				totals[part] = map[partitionCount]int64{}
			}
			for _, count := range counts {
				totals[part][partitionCount{Bucket: count.Bucket, Group: count.Group}] += count.Count
			}
		}
	}

	// Segments without counts, and the tail, over the whole ranges.
	raw, err := q.queryRanges(ctx, q.stream, grouped, tail, uncovered, whereClause, whereArgs, plan, size, ranges,
		request.ScanBudget, tokens)
	if err != nil {
		return nil, err
	}
	add(raw)
	// Segments with counts: their partial edge minutes raw...
	edgeCounts, err := q.queryRanges(ctx, q.stream, grouped, nil, covered, whereClause, whereArgs, plan, size, edges,
		request.ScanBudget, tokens)
	if err != nil {
		return nil, err
	}
	add(edgeCounts)
	// ...and their whole minutes from logs_1m.
	builder := newWhereBuilder(projectID)
	builder.rollup = true
	querySQL, queryArgs := builder.Build(request.Query)
	rollupWhere := "project_id = ?"
	rollupArgs := append([]any{projectID.String()}, queryArgs...)
	if querySQL != "" {
		rollupWhere += " AND " + querySQL
	}
	rollupCounts, err := q.queryRanges(ctx, logs_core.StreamLogsRollup, grouped, nil, rollupRecords, rollupWhere,
		rollupArgs, buildAggregationPlanCounting(request, builder, "sum(cnt)"), size, inner, request.ScanBudget, nil)
	if err != nil {
		return nil, err
	}
	add(rollupCounts)

	result := make(map[int64][]partitionCount, len(totals))
	for part, counts := range totals {
		for key, count := range counts {
			key.Count = count
			result[part] = append(result[part], key)
		}
	}
	return result, nil
}

// readableRollups maps a source segment id to its rollup, one per source
// (the newest), keeping only rollups this process can read.
func (q *LogQuerier) readableRollups(rollups []*segments.SegmentRecord) map[string]*segments.SegmentRecord {
	sort.Slice(rollups, func(i, j int) bool { return rollups[i].CreatedAt.After(rollups[j].CreatedAt) })
	bySource := map[string]*segments.SegmentRecord{}
	for _, rollup := range rollups {
		if rollup.Stats == nil || rollup.Stats.Source == "" || bySource[rollup.Stats.Source] != nil ||
			!q.readable(rollup) {
			continue
		}
		bySource[rollup.Stats.Source] = rollup
	}
	return bySource
}

// splitWholeMinutes splits ranges into the whole minutes they hold (which
// per-minute counts answer exactly) and the partial minutes at their edges.
func splitWholeMinutes(ranges []timeRange) (inner, edges []timeRange) {
	for _, r := range ranges {
		end := r.end
		if r.includeEnd {
			end = end.Add(time.Nanosecond)
		}
		first := r.start.Truncate(time.Minute)
		if first.Before(r.start) {
			first = first.Add(time.Minute)
		}
		last := end.Truncate(time.Minute)
		if !first.Before(last) {
			edges = append(edges, r)
			continue
		}
		inner = append(inner, timeRange{start: first, end: last})
		if r.start.Before(first) {
			edges = append(edges, timeRange{start: r.start, end: first})
		}
		if last.Before(end) {
			edges = append(edges, timeRange{start: last, end: r.end, includeEnd: r.includeEnd})
		}
	}
	return inner, edges
}
