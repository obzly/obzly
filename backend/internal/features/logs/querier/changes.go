package logs_querier

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	if request.CountOnly {
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
