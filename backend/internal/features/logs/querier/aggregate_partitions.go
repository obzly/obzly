package logs_querier

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	logs_core "obzly/internal/features/logs/core"
	"obzly/internal/query"
	"obzly/internal/query/partcache"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
)

// Time-bucketed aggregations are computed per time partition so finished
// partitions come from the partition cache. Partitions lie on a fixed epoch
// grid; a bucket split across two is summed back in the merge.
const (
	aggregationPartition = time.Hour
	// maxCachedPartitionRows: a partition grouped by a high-cardinality
	// field isn't worth Valkey memory.
	maxCachedPartitionRows = 20_000
	// maxAggregationPartitions: past it a bucketed aggregation runs as one
	// unpartitioned query. Each partition is a cache key and an OR clause;
	// a range open from 1970 made about 2M of them and stalled Valkey.
	maxAggregationPartitions = 2000
	// maxFutureSkew: how far ahead of now receiving accepts a timestamp.
	maxFutureSkew = 5 * time.Minute
)

// WithPartitionCache caches time-bucketed aggregations per partition.
func WithPartitionCache(cache *partcache.Cache) QuerierOption {
	return func(q *LogQuerier) {
		q.partitionCache = cache
	}
}

type aggregationPart struct {
	id         int64
	start, end time.Time
}

// partitionCount is one (bucket, group) count within a partition.
type partitionCount struct {
	Bucket int64  `json:"b"` // unix ms
	Group  string `json:"g,omitempty"`
	Count  int64  `json:"c"`
}

// aggregateByPartition answers a bucketed aggregation from cached partitions
// plus one query for the rest (partial, too recent, or missed). With emit the
// rest is read in growing groups of partitions, newest first.
func (q *LogQuerier) aggregateByPartition(
	ctx context.Context,
	projectID uuid.UUID,
	request *logs_core.AggregationRequestDTO,
	from, to time.Time,
	tail []segments.ResolvedFile,
	ordered []*segments.SegmentRecord,
	whereClause string,
	whereArgs []any,
	plan aggregationPlan,
	limit int,
	emit logs_core.AggregationEmitter,
) (*logs_core.AggregationResponseDTO, error) {
	size := max(aggregationPartition, time.Duration(request.BucketMs)*time.Millisecond)
	grouped := request.GroupField != ""
	now := time.Now().UTC()
	keyBase := partitionKeyBase(projectID, q.stream, request, size)

	var cacheable []aggregationPart
	var toCompute []aggregationPart
	gridFrom, gridTo := dataSpan(from, to, tail, ordered, now)
	for _, part := range partitionGrid(gridFrom, gridTo, size) {
		whole := !part.start.Before(from) && !part.end.After(to)
		if whole && q.partitionCache != nil && q.partitionCache.Cacheable(part.end, now) {
			cacheable = append(cacheable, part)
		} else {
			toCompute = append(toCompute, part)
		}
	}
	keys := make([]string, len(cacheable))
	for i, part := range cacheable {
		keys[i] = keyBase + strconv.FormatInt(part.id, 10)
	}
	cached := q.partitionCache.Get(ctx, keys)

	totals := map[partitionCount]int64{}
	add := func(counts []partitionCount) {
		for _, count := range counts {
			totals[partitionCount{Bucket: count.Bucket, Group: count.Group}] += count.Count
		}
	}
	var missing []aggregationPart
	for i, part := range cacheable {
		var counts []partitionCount
		if raw, ok := cached[keys[i]]; ok && json.Unmarshal(raw, &counts) == nil {
			add(counts)
			continue
		}
		missing = append(missing, part)
	}
	toCompute = append(toCompute, missing...)
	if emit != nil && len(totals) > 0 {
		if err := emit(mergedBuckets(totals, grouped, limit)); err != nil {
			return nil, err
		}
	}

	isMissing := make(map[int64]bool, len(missing))
	for _, part := range missing {
		isMissing[part.id] = true
	}
	groups := [][]aggregationPart{toCompute}
	if emit != nil {
		groups = newestFirstGroups(toCompute)
	}
	for _, group := range groups {
		if len(group) == 0 {
			continue
		}
		computed, err := q.computePartitions(ctx, projectID, request, from, to, tail, ordered, whereClause,
			whereArgs, plan, size, group)
		if err != nil {
			return nil, err
		}
		groupTotals := map[partitionCount]int64{}
		for _, counts := range computed {
			add(counts)
			for _, count := range counts {
				groupTotals[partitionCount{Bucket: count.Bucket, Group: count.Group}] += count.Count
			}
		}
		for _, part := range group {
			if !isMissing[part.id] {
				continue
			}
			counts := computed[part.id]
			if len(counts) > maxCachedPartitionRows {
				continue
			}
			if counts == nil {
				counts = []partitionCount{}
			}
			if raw, err := json.Marshal(counts); err == nil {
				q.partitionCache.Put(ctx, keyBase+strconv.FormatInt(part.id, 10), raw)
			}
		}
		if emit != nil && len(groupTotals) > 0 {
			if err := emit(mergedBuckets(groupTotals, grouped, limit)); err != nil {
				return nil, err
			}
		}
	}

	return &logs_core.AggregationResponseDTO{Buckets: mergedBuckets(totals, grouped, limit)}, nil
}

// computePartitions runs the aggregation over parts only, returning each
// part's counts by part id - from per-minute counts where they can answer
// (aggregate_rollups.go), else from the raw logs.
func (q *LogQuerier) computePartitions(
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
	parts []aggregationPart,
) (map[int64][]partitionCount, error) {
	ranges := make([]timeRange, 0, len(parts))
	for _, part := range parts {
		end := minTime(part.end, to)
		ranges = append(ranges, timeRange{start: maxTime(part.start, from), end: end, includeEnd: end.Equal(to)})
	}
	if q.rollupsApply(request) {
		return q.computeWithRollups(ctx, projectID, request, from, to, tail, ordered, whereClause, whereArgs, plan,
			size, ranges)
	}
	return q.queryRanges(ctx, q.stream, request.GroupField != "", tail, ordered, whereClause, whereArgs, plan, size,
		ranges, request.ScanBudget, requiredTokens(request.Query))
}

// timeRange is [start, end), or [start, end] when includeEnd.
type timeRange struct {
	start, end time.Time
	includeEnd bool
}

// queryRanges runs plan over the rows of stream's records (and tail) that
// fall in ranges, returning each part's counts by part id.
func (q *LogQuerier) queryRanges(
	ctx context.Context,
	stream string,
	grouped bool,
	tail []segments.ResolvedFile,
	ordered []*segments.SegmentRecord,
	whereClause string,
	whereArgs []any,
	plan aggregationPlan,
	size time.Duration,
	ranges []timeRange,
	scanBudget int64,
	tokens []string,
) (map[int64][]partitionCount, error) {
	result := map[int64][]partitionCount{}
	var clauses []string
	var rangeArgs []any
	var inRanges []*segments.SegmentRecord
	seen := map[*segments.SegmentRecord]bool{}
	for _, r := range mergeRanges(ranges) {
		clauses = append(clauses, "(timestamp >= ? AND timestamp "+ternary(r.includeEnd, "<=", "<")+" ?)")
		rangeArgs = append(rangeArgs, r.start, r.end)
		for _, record := range overlapping(ordered, searchWindow{From: r.start, To: r.end, IncludeTo: r.includeEnd}) {
			if !seen[record] {
				seen[record] = true
				inRanges = append(inRanges, record)
			}
		}
	}
	if len(clauses) == 0 || (len(inRanges) == 0 && len(tail) == 0) {
		return result, nil
	}
	where := whereClause + " AND (" + strings.Join(clauses, " OR ") + ")"
	args := append(append([]any{}, whereArgs...), rangeArgs...)
	// Fragments keep the part next to each bucket; the merge sums per part.
	partSelect := "floor(epoch(timestamp) / ?)::BIGINT AS part_id, " + plan.selectClause
	selectArgs := append([]any{size.Seconds()}, plan.selectArgs...)
	groupBy := plan.groupClause + ", part_id"
	fromWhere := " FROM " + query.SourcePlaceholder + " WHERE " + where + groupBy

	err := q.coordinator.Query(ctx, query.Request{
		Stream:       stream,
		Segments:     inRanges,
		LocalFiles:   tail,
		FragmentSQL:  "SELECT " + partSelect + fromWhere,
		FragmentArgs: append(append([]any{}, selectArgs...), args...),
		FinalSQL:     "SELECT part_id, " + plan.mergeClause + " FROM " + query.ResultsPlaceholder + groupBy,
		SingleSQL:    "SELECT " + partSelect + fromWhere,
		SingleArgs:   append(append([]any{}, selectArgs...), args...),
		ScanBudget:   scanBudget,
		Tokens:       tokens,
	}, func(rows *sql.Rows) error {
		for rows.Next() {
			var partID int64
			var bucket time.Time
			var count partitionCount
			dest := []any{&partID, &bucket}
			if grouped {
				dest = append(dest, &count.Group)
			}
			dest = append(dest, &count.Count)
			if err := rows.Scan(dest...); err != nil {
				return fmt.Errorf("failed to scan aggregation row: %w", err)
			}
			count.Bucket = bucket.UnixMilli()
			result[partID] = append(result[partID], count)
		}
		return nil
	})
	if errors.Is(err, query.ErrNoData) {
		return result, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to execute aggregation query: %w", err)
	}
	return result, nil
}

// newestFirstGroups splits parts into groups read one after another:
// the newest partition alone, then 4, 16, ... - the first counts come
// fast, and a long range still takes few rounds.
func newestFirstGroups(parts []aggregationPart) [][]aggregationPart {
	sorted := append([]aggregationPart{}, parts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].id > sorted[j].id })
	var groups [][]aggregationPart
	for size := 1; len(sorted) > 0; size *= searchWindowGrowth {
		n := min(size, len(sorted))
		groups = append(groups, sorted[:n])
		sorted = sorted[n:]
	}
	return groups
}

// partitionedAggregationFits reports whether a bucketed aggregation over
// [from, to] stays within maxAggregationPartitions partitions.
func partitionedAggregationFits(
	request *logs_core.AggregationRequestDTO,
	from, to time.Time,
	tail []segments.ResolvedFile,
	ordered []*segments.SegmentRecord,
) bool {
	size := max(aggregationPartition, time.Duration(request.BucketMs)*time.Millisecond)
	gridFrom, gridTo := dataSpan(from, to, tail, ordered, time.Now().UTC())
	step := int64(size / time.Second)
	return floorDiv(gridTo.Unix(), step)-floorDiv(gridFrom.Unix(), step)+1 <= maxAggregationPartitions
}

// dataSpan narrows [from, to] to where planned rows can be: from the oldest
// segment to the newest one's end or a little past now. Pending tail rows
// have no known span, so with any the range is kept as asked.
func dataSpan(
	from, to time.Time, tail []segments.ResolvedFile, ordered []*segments.SegmentRecord, now time.Time,
) (time.Time, time.Time) {
	if len(tail) > 0 || len(ordered) == 0 {
		return from, to
	}
	oldest, newest := ordered[0].MinTime, ordered[0].MaxTime
	for _, record := range ordered[1:] {
		oldest, newest = minTime(oldest, record.MinTime), maxTime(newest, record.MaxTime)
	}
	from = maxTime(from, oldest)
	to = minTime(to, maxTime(newest, now.Add(maxFutureSkew)))
	if to.Before(from) {
		to = from
	}
	return from, to
}

// mergeRanges sorts ranges and joins touching or overlapping ones, so
// adjacent partitions cost one predicate instead of one each.
func mergeRanges(ranges []timeRange) []timeRange {
	sorted := append([]timeRange{}, ranges...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].start.Before(sorted[j].start) })
	merged := make([]timeRange, 0, len(sorted))
	for _, r := range sorted {
		if n := len(merged); n > 0 && !r.start.After(merged[n-1].end) {
			last := &merged[n-1]
			switch {
			case r.end.After(last.end):
				last.end, last.includeEnd = r.end, r.includeEnd
			case r.end.Equal(last.end):
				last.includeEnd = last.includeEnd || r.includeEnd
			}
			continue
		}
		merged = append(merged, r)
	}
	return merged
}

// partitionGrid is the grid partitions of size that [from, to] touches.
func partitionGrid(from, to time.Time, size time.Duration) []aggregationPart {
	step := int64(size / time.Second)
	first, last := floorDiv(from.Unix(), step), floorDiv(to.Unix(), step)
	parts := make([]aggregationPart, 0, last-first+1)
	for id := first; id <= last; id++ {
		parts = append(parts, aggregationPart{
			id:    id,
			start: time.Unix(id*step, 0).UTC(),
			end:   time.Unix((id+1)*step, 0).UTC(),
		})
	}
	return parts
}

// partitionKeyBase identifies an aggregation (everything but the part).
func partitionKeyBase(
	projectID uuid.UUID,
	stream string,
	request *logs_core.AggregationRequestDTO,
	size time.Duration,
) string {
	queryJSON, _ := json.Marshal(request.Query)
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%d|%d", stream, queryJSON, request.GroupField,
		request.BucketMs, int64(size/time.Second))))
	return "logs:" + projectID.String() + ":" + hex.EncodeToString(sum[:16]) + ":"
}

// mergedBuckets orders the summed counts as the plan's ORDER BY does
// (bucket, then group) and applies limit.
func mergedBuckets(totals map[partitionCount]int64, grouped bool, limit int) []logs_core.AggregationBucketDTO {
	keys := make([]partitionCount, 0, len(totals))
	for key := range totals {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Bucket != keys[j].Bucket {
			return keys[i].Bucket < keys[j].Bucket
		}
		return keys[i].Group < keys[j].Group
	})
	buckets := make([]logs_core.AggregationBucketDTO, 0, min(len(keys), limit))
	for _, key := range keys[:min(len(keys), limit)] {
		ts := time.UnixMilli(key.Bucket).UTC()
		bucket := logs_core.AggregationBucketDTO{Timestamp: &ts, Count: totals[key]}
		if grouped {
			bucket.GroupKey = key.Group
		}
		buckets = append(buckets, bucket)
	}
	return buckets
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

func ternary(condition bool, yes, no string) string {
	if condition {
		return yes
	}
	return no
}
