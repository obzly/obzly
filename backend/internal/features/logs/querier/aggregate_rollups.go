package logs_querier

import (
	"context"
	"sort"
	"strings"
	"time"

	logs_core "obzly/internal/features/logs/core"
	"obzly/internal/storage/segments"
	"obzly/internal/util/attrnames"

	"github.com/google/uuid"
)

// Bucketed aggregations read rollups where they can (whole minutes or hours,
// counts' dimensions only). Partial edge minutes, segments without rollups
// and the tail are read raw; all inputs are summed per bucket.

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
	if q.stream != logs_core.StreamLogs || request.BucketMs <= 0 || request.BucketMs%time.Minute.Milliseconds() != 0 ||
		!request.UsesCountsOnly() {
		return false
	}
	if request.GroupField != "" {
		for _, field := range strings.Split(request.GroupField, logs_core.GroupFieldsSeparator) {
			if !rollupFields[attrnames.Normalize(field)] {
				return false
			}
		}
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

// computeWithRollups is computePartitions over ranges reading, per segment,
// the smallest rollup tier that can count it (rollup_tiers.go).
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
	var fields []string
	if grouped {
		for _, field := range strings.Split(request.GroupField, logs_core.GroupFieldsSeparator) {
			fields = append(fields, attrnames.Normalize(field))
		}
	}
	fields = append(fields, queryFields(request.Query)...)
	// Hourly counts fit only buckets of whole hours (epoch-aligned, as hours are).
	hoursMerge := request.BucketMs%time.Hour.Milliseconds() == 0
	sources, err := q.tieredSources(projectID, from, to, tail, ordered, countTiersFor(fields, hoursMerge))
	if err != nil {
		return nil, err
	}
	if len(sources) == 1 {
		return q.queryRanges(ctx, q.stream, grouped, tail, ordered, whereClause, whereArgs, plan, size, ranges,
			request.ScanBudget, tokens)
	}

	builder := newWhereBuilder(projectID)
	builder.rollup = true
	querySQL, queryArgs := builder.Build(request.Query)
	rollupWhere := "project_id = ?"
	rollupArgs := append([]any{projectID.String()}, queryArgs...)
	if querySQL != "" {
		rollupWhere += " AND " + querySQL
	}
	rollupPlan := buildAggregationPlanCounting(request, builder, "sum(cnt)")

	totals := map[int64]map[partitionCount]int64{}
	for _, source := range sources {
		sourceRanges := rangesOf(source.kind, ranges)
		if len(sourceRanges) == 0 {
			continue
		}
		var computed map[int64][]partitionCount
		if source.rollup {
			computed, err = q.queryRanges(ctx, source.stream, grouped, nil, source.records, rollupWhere, rollupArgs,
				rollupPlan, size, sourceRanges, request.ScanBudget, nil)
		} else {
			computed, err = q.queryRanges(ctx, q.stream, grouped, source.tail, source.records, whereClause, whereArgs,
				plan, size, sourceRanges, request.ScanBudget, tokens)
		}
		if err != nil {
			return nil, err
		}
		for part, counts := range computed {
			if totals[part] == nil {
				totals[part] = map[partitionCount]int64{}
			}
			for _, count := range counts {
				totals[part][partitionCount{Bucket: count.Bucket, Group: count.Group}] += count.Count
			}
		}
	}

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
