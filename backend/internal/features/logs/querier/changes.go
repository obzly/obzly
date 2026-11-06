package logs_querier

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	logs_core "obzly/internal/features/logs/core"
	"obzly/internal/query"

	"github.com/google/uuid"
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

	curFrom, curTo := resolveTimeRange(request.Current)
	baseFrom, baseTo := resolveTimeRange(request.Baseline)
	current, err := q.patternCounts(ctx, projectID, request.Query, curFrom, curTo, request.ScanBudget)
	if err != nil {
		return nil, err
	}
	baseline, err := q.patternCounts(ctx, projectID, request.Query, baseFrom, baseTo, request.ScanBudget)
	if err != nil {
		return nil, err
	}
	scale := 1.0
	if baseLen := baseTo.Sub(baseFrom); baseLen > 0 {
		scale = float64(curTo.Sub(curFrom)) / float64(baseLen)
	}

	response := &logs_core.PatternChangesResponseDTO{Changes: []logs_core.PatternChangeDTO{}}
	var added, rising, falling []logs_core.PatternChangeDTO
	hashes := map[string]bool{}
	for hash := range current {
		hashes[hash] = true
	}
	for hash := range baseline {
		hashes[hash] = true
	}
	for hash := range hashes {
		now, before := current[hash], baseline[hash]
		response.CurrentTotal += now
		response.BaselineTotal += before
		expected := float64(before) * scale
		change := logs_core.PatternChangeDTO{Hash: hash, Current: now, Baseline: before}
		switch {
		case before == 0 && now >= minCount:
			change.Change, change.Ratio = logs_core.PatternChangeNew, NewPatternRatio
			added = append(added, change)
		case before > 0 && now >= minCount && float64(now) >= spikeFactor*expected:
			change.Change, change.Ratio = logs_core.PatternChangeSpike, float64(now)/expected
			rising = append(rising, change)
		case expected >= float64(minCount) && float64(now) <= dropFactor*expected:
			change.Change, change.Ratio = logs_core.PatternChangeDrop, float64(now)/expected
			falling = append(falling, change)
		}
	}
	sort.Slice(added, func(i, j int) bool { return byCountThenHash(added[i], added[j]) })
	sort.Slice(rising, func(i, j int) bool {
		if rising[i].Ratio != rising[j].Ratio {
			return rising[i].Ratio > rising[j].Ratio
		}
		return byCountThenHash(rising[i], rising[j])
	})
	sort.Slice(falling, func(i, j int) bool {
		if falling[i].Ratio != falling[j].Ratio {
			return falling[i].Ratio < falling[j].Ratio
		}
		return falling[i].Baseline > falling[j].Baseline
	})
	added, rising, falling = added[:min(len(added), limit)], rising[:min(len(rising), limit)],
		falling[:min(len(falling), limit)]

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
	response.Changes = append(grown, falling...)
	return response, nil
}

func byCountThenHash(a, b logs_core.PatternChangeDTO) bool {
	if a.Current != b.Current {
		return a.Current > b.Current
	}
	return a.Hash < b.Hash
}

// patternCounts is each pattern's count of logs matching filter in [from, to].
func (q *LogQuerier) patternCounts(
	ctx context.Context, projectID uuid.UUID, filter *logs_core.QueryNode, from, to time.Time, scanBudget int64,
) (map[string]int64, error) {
	// One time bucket as wide as the range, in whole minutes: answered from
	// per-minute counts where segments have them.
	width := to.Sub(from).Truncate(time.Minute) + time.Minute
	response, err := q.ExecuteAggregation(ctx, projectID, &logs_core.AggregationRequestDTO{
		Query: filter, TimeRange: &logs_core.TimeRangeDTO{From: &from, To: &to},
		GroupField: logs_core.PatternHashField, BucketMs: width.Milliseconds(), Limit: maxAggregationLimit,
		ScanBudget: scanBudget,
	})
	if err != nil {
		return nil, err
	}
	counts := map[string]int64{}
	for _, bucket := range response.Buckets {
		counts[bucket.GroupKey] += bucket.Count
	}
	return counts, nil
}

type patternSample struct {
	message, level string
}

// patternSamples is the newest log of each wanted pattern among the logs
// matching filter in [from, to]: windows are read newest first until every
// pattern has one.
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
	for _, window := range searchWindows(from, to, ordered, time.Now().UTC(), true) {
		if len(remaining) == 0 {
			break
		}
		inWindow := overlapping(ordered, window)
		if len(inWindow) == 0 && len(tail) == 0 {
			continue
		}
		hashes := make([]any, 0, len(remaining))
		for hash := range remaining {
			hashes = append(hashes, hash)
		}
		upper := " < ?"
		if window.IncludeTo {
			upper = " <= ?"
		}
		where := "project_id = ? AND timestamp >= ? AND timestamp" + upper + " AND " + storedPatternHashSQL +
			" IN (" + strings.TrimSuffix(strings.Repeat("?, ", len(hashes)), ", ") + ")"
		args := append([]any{projectID.String(), window.From, window.To}, hashes...)
		if querySQL != "" {
			where += " AND " + querySQL
			args = append(args, queryArgs...)
		}
		grouped := "SELECT " + storedPatternHashSQL + " AS hash, arg_max(message, timestamp) AS message, " +
			"arg_max(level, timestamp) AS level, max(timestamp) AS newest FROM " + query.SourcePlaceholder +
			" WHERE " + where + " GROUP BY 1"
		err := q.coordinator.Query(ctx, query.Request{
			Stream: q.stream, Segments: inWindow, LocalFiles: tail,
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
			return nil, fmt.Errorf("failed to read pattern samples: %w", err)
		}
	}
	return samples, nil
}
