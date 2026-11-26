package logs_querier

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	logs_core "obzly/internal/features/logs/core"
	"obzly/internal/query"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
)

// Log anomalies: each message pattern's count in the current range against a
// baseline scaled to the same length (new, spiking or dropping patterns).
// Templates, samples and levels come from each pattern's newest log.
const (
	defaultChangeMinCount = 5
	defaultChangesLimit   = 50
	maxChangesLimit       = 200
	// spikeFactor / dropFactor: a pattern's count against its scaled
	// baseline to be a spike / drop.
	spikeFactor = 2.0
	dropFactor  = 0.5
	// NewPatternRatio stands for "no baseline at all" where a ratio is
	// needed (an alert's measured value).
	NewPatternRatio = 1e9
)

// ExecutePatternChanges compares message patterns between request.Current
// and request.Baseline.
func (q *LogQuerier) ExecutePatternChanges(
	ctx context.Context,
	projectID uuid.UUID,
	request *logs_core.PatternChangesRequestDTO,
) (*logs_core.PatternChangesResponseDTO, error) {
	minCount := request.MinCount
	if minCount <= 0 {
		minCount = defaultChangeMinCount
	}
	limit := request.Limit
	if limit <= 0 {
		limit = defaultChangesLimit
	}
	limit = min(limit, maxChangesLimit)
	if request.CountOnly && (q.stream != logs_core.StreamLogs || !onRollupFields(request.Query)) {
		return &logs_core.PatternChangesResponseDTO{Changes: []logs_core.PatternChangeDTO{}}, nil
	}

	curFrom, curTo := resolveTimeRange(request.Current)
	baseFrom, baseTo := resolveTimeRange(request.Baseline)
	scale := 1.0
	if baseLen := baseTo.Sub(baseFrom); baseLen > 0 {
		scale = float64(curTo.Sub(curFrom)) / float64(baseLen)
	}
	selectLimit := limit
	if request.CountOnly {
		selectLimit = 0
	}
	// The kinds are told apart in the merge (w0: current, w1: baseline,
	// expected = w1 * scale), so only the listed patterns reach Go.
	expected := "(w1 * ?::DOUBLE)"
	counted, err := q.groupWindows(ctx, projectID, &logs_core.GroupWindowsRequestDTO{
		Query: request.Query, GroupField: logs_core.PatternHashField,
		Windows: []logs_core.CountWindowDTO{{From: curFrom, To: curTo}, {From: baseFrom, To: baseTo}},
		Selections: []logs_core.GroupSelectionDTO{
			{Where: "w1 = 0 AND w0 >= ?", WhereArgs: []any{minCount}, OrderBy: "w0 DESC", Limit: selectLimit},
			{
				Where:     "w1 > 0 AND w0 >= ? AND w0 >= ?::DOUBLE * " + expected,
				WhereArgs: []any{minCount, spikeFactor, scale},
				OrderBy:   "w0 / " + expected + " DESC, w0 DESC", OrderArgs: []any{scale}, Limit: selectLimit,
			},
			{
				Where:     expected + " >= ? AND w0 <= ?::DOUBLE * " + expected,
				WhereArgs: []any{scale, minCount, dropFactor, scale},
				OrderBy:   "w0 / " + expected + " ASC, w1 DESC", OrderArgs: []any{scale}, Limit: selectLimit,
			},
		},
		ScanBudget: request.ScanBudget,
	})
	if err != nil {
		return nil, err
	}

	response := &logs_core.PatternChangesResponseDTO{
		Changes: []logs_core.PatternChangeDTO{}, Counted: true,
		CurrentTotal: counted.Totals[0], BaselineTotal: counted.Totals[1],
		NewCount: int(counted.Selections[0].Matched), SpikeCount: int(counted.Selections[1].Matched),
		DropCount: int(counted.Selections[2].Matched),
	}
	since, regrouped := logs_core.PatternBaselineRegrouped(baseFrom)
	if regrouped {
		response.PatternVersionSince = &since
	}
	if request.CountOnly {
		if regrouped {
			// Without samples a regrouped pattern looks new (or dropped).
			response.NewCount, response.DropCount = 0, 0
		}
		return response, nil
	}
	changes := func(selection logs_core.GroupSelectionResultDTO, kind string) []logs_core.PatternChangeDTO {
		result := make([]logs_core.PatternChangeDTO, 0, len(selection.Rows))
		for _, row := range selection.Rows {
			change := logs_core.PatternChangeDTO{
				Hash: row.Key, Current: row.Counts[0], Baseline: row.Counts[1], Change: kind, Ratio: NewPatternRatio,
			}
			if kind != logs_core.PatternChangeNew {
				change.Ratio = float64(change.Current) / (float64(change.Baseline) * scale)
			}
			result = append(result, change)
		}
		return result
	}
	added := changes(counted.Selections[0], logs_core.PatternChangeNew)
	rising := changes(counted.Selections[1], logs_core.PatternChangeSpike)
	falling := changes(counted.Selections[2], logs_core.PatternChangeDrop)

	// New and rising patterns are described from the current range, dropped
	// ones from the baseline.
	describe := func(changes []logs_core.PatternChangeDTO, from, to time.Time) error {
		wanted := make([]string, len(changes))
		for i, change := range changes {
			wanted[i] = change.Hash
		}
		samples, err := q.patternSamples(ctx, projectID, request.Query, wanted, from, to)
		if err != nil {
			return err
		}
		for i := range changes {
			if sample, ok := samples[changes[i].Hash]; ok {
				changes[i].Sample, changes[i].Level = sample.message, sample.level
				changes[i].Pattern = logs_core.MessagePattern(sample.message)
			}
		}
		return nil
	}
	grown := append(append([]logs_core.PatternChangeDTO{}, added...), rising...)
	if err := describe(grown, curFrom, curTo); err != nil {
		return nil, err
	}
	if err := describe(falling, baseFrom, baseTo); err != nil {
		return nil, err
	}
	if regrouped {
		grown, falling = withoutRegrouped(grown, falling, baseFrom)
		response.NewCount, response.DropCount = 0, len(falling)
		for _, change := range grown {
			if change.Change == logs_core.PatternChangeNew {
				response.NewCount++
			}
		}
	}
	response.Changes = append(grown, falling...)
	return response, nil
}

// ExecutePatternSamples is the template, newest log and level of each of
// request.Hashes among the logs matching request.Query in request.TimeRange.
func (q *LogQuerier) ExecutePatternSamples(
	ctx context.Context, projectID uuid.UUID, request *logs_core.PatternSamplesRequestDTO,
) (map[string]logs_core.PatternSampleDTO, error) {
	from, to := resolveTimeRange(request.TimeRange)
	samples, err := q.patternSamples(ctx, projectID, request.Query, request.Hashes, from, to)
	if err != nil {
		return nil, err
	}
	result := make(map[string]logs_core.PatternSampleDTO, len(samples))
	for hash, sample := range samples {
		result[hash] = logs_core.PatternSampleDTO{
			Pattern: logs_core.MessagePattern(sample.message), Sample: sample.message, Level: sample.level,
		}
	}
	return result, nil
}

type patternSample struct {
	message, level string
}

// patternSamples is the newest log of each wanted pattern among the logs
// matching filter in [from, to]. Where the rollups can tell each pattern's
// newest minute (its hour from logs_1h_patterns, then the minute from
// logs_1m), raw logs are read only there; else, and for any pattern not
// found there, windows are read newest first until every pattern has one,
// which reads the whole range when one of them is rare.
func (q *LogQuerier) patternSamples(
	ctx context.Context, projectID uuid.UUID, filter *logs_core.QueryNode, wanted []string, from, to time.Time,
) (map[string]patternSample, error) {
	samples := map[string]patternSample{}
	if len(wanted) == 0 {
		return samples, nil
	}
	records, err := q.segmentRepo.PlanSegments(projectID, q.stream, from, to, hintsFor(filter))
	if err != nil {
		return nil, fmt.Errorf("failed to list segments for project: %w", err)
	}
	tail, ordered := q.searchInputs(ctx, projectID, records, from, to, true)
	if len(tail) == 0 && len(ordered) == 0 {
		return samples, nil
	}
	querySQL, queryArgs := newWhereBuilder(projectID).Build(filter)
	tokens := requiredTokens(filter)

	remaining := map[string]bool{}
	for _, hash := range wanted {
		remaining[hash] = true
	}
	// read looks for hashes in windows: the segments any window reaches, the
	// rows in any window. A pattern's newest line in the union is its newest
	// line overall as long as one window holds it.
	read := func(windows []searchWindow, hashes []string) error {
		var inWindows []*segments.SegmentRecord
		seen := map[*segments.SegmentRecord]bool{}
		for _, window := range windows {
			for _, record := range overlapping(ordered, window) {
				if !seen[record] {
					seen[record] = true
					inWindows = append(inWindows, record)
				}
			}
		}
		if len(hashes) == 0 || (len(inWindows) == 0 && len(tail) == 0) {
			return nil
		}
		ranges := make([]timeRange, len(windows))
		for i, window := range windows {
			ranges[i] = timeRange{start: window.From, end: window.To, includeEnd: window.IncludeTo}
		}
		inRanges, rangeArgs := rangesPredicate(ranges)
		where := "project_id = ? AND " + inRanges + " AND " + inList(storedPatternHashSQL, len(hashes))
		args := append(append([]any{projectID.String()}, rangeArgs...), toAny(hashes)...)
		if querySQL != "" {
			where += " AND " + querySQL
			args = append(args, queryArgs...)
		}
		grouped := "SELECT " + storedPatternHashSQL + " AS hash, arg_max(message, timestamp) AS message, " +
			"arg_max(level, timestamp) AS level, max(timestamp) AS newest FROM " + query.SourcePlaceholder +
			" WHERE " + where + " GROUP BY 1"
		err := q.coordinator.Query(ctx, query.Request{
			Stream: q.stream, Segments: inWindows, LocalFiles: tail,
			FragmentSQL: grouped, FragmentArgs: args,
			FinalSQL: "SELECT hash, arg_max(message, newest), arg_max(level, newest), max(newest) FROM " +
				query.ResultsPlaceholder + " GROUP BY 1",
			SingleSQL: grouped, SingleArgs: args, Tokens: tokens,
		}, func(rows *sql.Rows) error {
			for rows.Next() {
				var hash string
				var message, level sql.NullString
				var newest time.Time
				if err := rows.Scan(&hash, &message, &level, &newest); err != nil {
					return fmt.Errorf("failed to scan pattern sample: %w", err)
				}
				samples[hash] = patternSample{message: message.String, level: level.String}
				delete(remaining, hash)
			}
			return nil
		})
		if err != nil && !errors.Is(err, query.ErrNoData) {
			return fmt.Errorf("failed to read pattern samples: %w", err)
		}
		return nil
	}
	stillWanted := func(hashes []string) []string {
		var left []string
		for _, hash := range hashes {
			if remaining[hash] {
				left = append(left, hash)
			}
		}
		return left
	}

	now := time.Now().UTC()
	if hours, ok := q.newestPatternBuckets(ctx, projectID, filter, wanted, from, to, time.Hour); ok {
		hours = newestFirst(hours)
		// Within each hour logs_1m tells the minute, so one read of a segment
		// or two per located minute answers every pattern at once.
		minutes := q.newestPatternMinutes(ctx, projectID, filter, hours, from, to)
		var windows []searchWindow
		var inMinutes []string
		for i, hour := range hours {
			hourFrom, hourTo := maxTime(hour.at, from), minTime(hour.at.Add(time.Hour), to)
			for _, minute := range minutes[i] {
				windows = append(windows, searchWindow{
					From:      maxTime(minute.at, hourFrom),
					To:        minTime(minute.at.Add(time.Minute), hourTo),
					IncludeTo: true,
				})
				inMinutes = append(inMinutes, minute.hashes...)
			}
		}
		if err := read(windows, inMinutes); err != nil {
			return nil, err
		}
		// What the minutes didn't answer: its hour, newest first.
		for _, hour := range hours {
			hourFrom, hourTo := maxTime(hour.at, from), minTime(hour.at.Add(time.Hour), to)
			inHour := overlapping(ordered, searchWindow{From: hourFrom, To: hourTo, IncludeTo: true})
			for _, window := range searchWindows(hourFrom, hourTo, inHour, now, true) {
				hashes := stillWanted(hour.hashes)
				if len(hashes) == 0 {
					break
				}
				if err := read([]searchWindow{window}, hashes); err != nil {
					return nil, err
				}
			}
		}
		// A pattern without a line in the range has no sample to look for.
		located := map[string]bool{}
		for _, hour := range hours {
			for _, hash := range hour.hashes {
				located[hash] = true
			}
		}
		for hash := range remaining {
			if !located[hash] {
				delete(remaining, hash)
			}
		}
	}
	for _, window := range searchWindows(from, to, ordered, now, true) {
		if len(remaining) == 0 {
			break
		}
		if err := read([]searchWindow{window}, slices.Collect(maps.Keys(remaining))); err != nil {
			return nil, err
		}
	}
	return samples, nil
}

// patternLocateConcurrency caps the hours whose newest minutes are counted
// at once.
const patternLocateConcurrency = 8

// patternBucket is a bucket start and the patterns whose newest line is in
// that bucket.
type patternBucket struct {
	at     time.Time
	hashes []string
}

// newestPatternBuckets groups wanted by the bucket (an hour or a minute)
// holding each one's newest line matching filter in [from, to], counted from
// the rollups; patterns without one are left out. ok is false when the
// rollups can't answer (the count would read raw logs) or it was cut short.
func (q *LogQuerier) newestPatternBuckets(
	ctx context.Context, projectID uuid.UUID, filter *logs_core.QueryNode, wanted []string, from, to time.Time,
	bucket time.Duration,
) ([]patternBucket, bool) {
	if len(wanted) == 0 {
		return nil, true
	}
	hashes := logs_core.QueryNode{
		Type: logs_core.QueryNodeTypeCondition,
		Condition: &logs_core.ConditionNode{
			Field: logs_core.PatternHashField, Operator: logs_core.ConditionOperatorIn, Value: toAny(wanted),
		},
	}
	located := &hashes
	if filter != nil {
		located = &logs_core.QueryNode{
			Type: logs_core.QueryNodeTypeLogical,
			Logic: &logs_core.LogicalNode{
				Operator: logs_core.LogicalOperatorAnd, Children: []logs_core.QueryNode{*filter, hashes},
			},
		}
	}
	request := &logs_core.AggregationRequestDTO{
		Query: located, TimeRange: &logs_core.TimeRangeDTO{From: &from, To: &to},
		GroupField: logs_core.PatternHashField, BucketMs: bucket.Milliseconds(), Limit: maxAggregationLimit,
	}
	if !q.rollupsApply(request) {
		return nil, false
	}
	response, err := q.ExecuteAggregation(ctx, projectID, request)
	if err != nil || len(response.Buckets) >= maxAggregationLimit {
		return nil, false
	}
	newest := map[string]time.Time{}
	for _, b := range response.Buckets {
		if b.Count <= 0 || b.Timestamp == nil {
			continue
		}
		if at := b.Timestamp.UTC(); at.After(newest[b.GroupKey]) {
			newest[b.GroupKey] = at
		}
	}
	byStart := map[time.Time][]string{}
	for hash, at := range newest {
		byStart[at] = append(byStart[at], hash)
	}
	buckets := make([]patternBucket, 0, len(byStart))
	for at, hashes := range byStart {
		slices.Sort(hashes)
		buckets = append(buckets, patternBucket{at, hashes})
	}
	return buckets, true
}

// newestPatternMinutes is, per hour of hours, its patterns grouped by the
// minute holding each one's newest line (nil where logs_1m can't tell).
// The hours are counted concurrently.
func (q *LogQuerier) newestPatternMinutes(
	ctx context.Context, projectID uuid.UUID, filter *logs_core.QueryNode, hours []patternBucket, from, to time.Time,
) [][]patternBucket {
	minutes := make([][]patternBucket, len(hours))
	var group errgroup.Group
	group.SetLimit(patternLocateConcurrency)
	for i, hour := range hours {
		group.Go(func() error {
			hourFrom, hourTo := maxTime(hour.at, from), minTime(hour.at.Add(time.Hour), to)
			if found, ok := q.newestPatternBuckets(ctx, projectID, filter, hour.hashes, hourFrom,
				wholeBucketEnd(hour.at, hourTo, time.Hour), time.Minute); ok {
				minutes[i] = found
			}
			return nil
		})
	}
	_ = group.Wait()
	return minutes
}

// newestFirst sorts buckets by start, newest first.
func newestFirst(buckets []patternBucket) []patternBucket {
	slices.SortFunc(buckets, func(a, b patternBucket) int { return b.at.Compare(a.at) })
	return buckets
}

// wholeBucketEnd is end as an inclusive range end: just before it when it
// closes start's whole bucket, so counts read no raw edge at that instant.
func wholeBucketEnd(start, end time.Time, bucket time.Duration) time.Time {
	if end.Equal(start.Add(bucket)) {
		return end.Add(-time.Nanosecond)
	}
	return end
}

// withoutRegrouped drops, from changes against a baseline starting at
// baseFrom, before the current pattern version, the new patterns a version
// since then regrouped (they had another hash in the baseline) and the
// dropped ones retired (their logs now have another hash). Changes without
// a sample stay.
func withoutRegrouped(grown, falling []logs_core.PatternChangeDTO, baseFrom time.Time) (
	[]logs_core.PatternChangeDTO, []logs_core.PatternChangeDTO,
) {
	grown = slices.DeleteFunc(grown, func(change logs_core.PatternChangeDTO) bool {
		return change.Change == logs_core.PatternChangeNew && change.Sample != "" &&
			logs_core.PatternRegrouped(change.Sample, baseFrom)
	})
	falling = slices.DeleteFunc(falling, func(change logs_core.PatternChangeDTO) bool {
		return change.Sample != "" && logs_core.PatternHash(change.Sample) != change.Hash
	})
	return grown, falling
}
