package logs_querier

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	logs_core "obzly/internal/features/logs/core"
	"obzly/internal/query"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
)

// Facets with a sample size read the range window by window from its newest
// end and stop once the matching rows counted reach it, so a busy project's
// 72-hour facet reads minutes, not every file. Without one the range is one window.
const (
	defaultFieldValuesLimit = 10
	maxFieldValuesLimit     = 1000
	// sampledGroupsPerWindow: groups kept per window, so a value just outside one
	// window's top-N still adds up across windows.
	sampledGroupsPerWindow = 10
	minSampledGroups       = 1000
)

// facetGroup is one value (or pattern) and its count.
type facetGroup struct {
	key, sample string
	count       int64
}

// facetQuery is a top-N group-by: groupExpr per matching row, optionally
// with any message from each group as its sample.
type facetQuery struct {
	groupExpr  string
	groupArgs  []any
	withSample bool
	limit      int
	sampleRows int64
	scanBudget int64
	tokens     []string
}

type facetResult struct {
	groups      []facetGroup // most frequent first, ties by key
	sampledFrom *time.Time
	matched     int64
}

// topGroups answers fq over [from, to] - the whole range, or newest
// windows until fq.sampleRows matching rows were counted.
func (q *LogQuerier) topGroups(
	ctx context.Context,
	tail []segments.ResolvedFile,
	ordered []*segments.SegmentRecord,
	from, to time.Time,
	whereClause string,
	whereArgs []any,
	fq facetQuery,
) (facetResult, error) {
	windows := []searchWindow{{From: from, To: to, IncludeTo: true}}
	keep := fq.limit
	if fq.sampleRows > 0 {
		windows = searchWindows(from, to, ordered, time.Now().UTC(), true)
		if len(windows) > 1 {
			keep = max(fq.limit*sampledGroupsPerWindow, minSampledGroups)
		}
	}

	var result facetResult
	totals := map[string]*facetGroup{}
	for i, window := range windows {
		inWindow := overlapping(ordered, window)
		if len(inWindow) == 0 && len(tail) == 0 {
			continue
		}
		clause, args := whereClause, whereArgs
		if len(windows) > 1 {
			upper := " < ?"
			if window.IncludeTo {
				upper = " <= ?"
			}
			clause += " AND timestamp >= ? AND timestamp" + upper
			args = append(append([]any{}, whereArgs...), window.From, window.To)
		}
		matched, err := q.windowGroups(ctx, tail, inWindow, clause, args, fq, keep, func(group facetGroup) {
			if total, ok := totals[group.key]; ok {
				total.count += group.count
				return
			}
			totals[group.key] = &group
		})
		if errors.Is(err, query.ErrNoData) {
			continue
		}
		if err != nil {
			return facetResult{}, err
		}
		result.matched += matched
		if fq.sampleRows > 0 && result.matched >= fq.sampleRows && i < len(windows)-1 {
			sampledFrom := window.From
			result.sampledFrom = &sampledFrom
			break
		}
	}

	result.groups = make([]facetGroup, 0, len(totals))
	for _, group := range totals {
		result.groups = append(result.groups, *group)
	}
	sort.Slice(result.groups, func(i, j int) bool {
		a, b := result.groups[i], result.groups[j]
		if a.count != b.count {
			return a.count > b.count
		}
		return a.key < b.key
	})
	result.groups = result.groups[:min(len(result.groups), fq.limit)]
	return result, nil
}

// windowGroups runs fq over one window, handing add its top keep groups,
// and returns how many rows matched in all.
func (q *LogQuerier) windowGroups(
	ctx context.Context,
	tail []segments.ResolvedFile,
	ordered []*segments.SegmentRecord,
	whereClause string,
	whereArgs []any,
	fq facetQuery,
	keep int,
	add func(facetGroup),
) (int64, error) {
	sampleColumn, mergedSample := "", ""
	if fq.withSample {
		sampleColumn, mergedSample = ", any_value(message) AS sample", ", any_value(sample) AS sample"
	}
	// Fragments return every group's count (a top-N must see full counts);
	// the merge sums them, keeps the top ones and the window's total.
	grouped := "SELECT " + fq.groupExpr + " AS group_key" + sampleColumn + ", count(*) AS cnt FROM " +
		query.SourcePlaceholder + " WHERE " + whereClause + " GROUP BY group_key"
	groupedArgs := append(append([]any{}, fq.groupArgs...), whereArgs...)
	order := " ORDER BY cnt DESC, group_key ASC LIMIT ?"

	var matched int64
	err := q.coordinator.Query(ctx, query.Request{
		Stream:       q.stream,
		Segments:     ordered,
		LocalFiles:   tail,
		FragmentSQL:  grouped,
		FragmentArgs: groupedArgs,
		FinalSQL: "SELECT group_key" + mergedSample + ", sum(cnt)::BIGINT AS cnt, " +
			"(sum(sum(cnt)) OVER ())::BIGINT AS total FROM " + query.ResultsPlaceholder +
			" GROUP BY group_key" + order,
		FinalArgs:  []any{keep},
		SingleSQL:  "SELECT *, (sum(cnt) OVER ())::BIGINT AS total FROM (" + grouped + ")" + order,
		SingleArgs: append(groupedArgs, keep),
		ScanBudget: fq.scanBudget,
		Tokens:     fq.tokens,
	}, func(rows *sql.Rows) error {
		for rows.Next() {
			var group facetGroup
			dest := []any{&group.key}
			if fq.withSample {
				dest = append(dest, &group.sample)
			}
			dest = append(dest, &group.count, &matched)
			if err := rows.Scan(dest...); err != nil {
				return fmt.Errorf("failed to scan facet row: %w", err)
			}
			add(group)
		}
		return nil
	})
	return matched, err
}

// ExecuteFieldValues returns a field's most frequent values among logs
// matching request.Query in request.TimeRange (see topGroups for SampleRows).
func (q *LogQuerier) ExecuteFieldValues(
	ctx context.Context,
	projectID uuid.UUID,
	request *logs_core.FieldValuesRequestDTO,
) (*logs_core.FieldValuesResponseDTO, error) {
	response, err := q.executeFieldValues(ctx, projectID, request)
	if segmentVanished(err) {
		return q.executeFieldValues(ctx, projectID, request)
	}
	return response, err
}

func (q *LogQuerier) executeFieldValues(
	ctx context.Context,
	projectID uuid.UUID,
	request *logs_core.FieldValuesRequestDTO,
) (*logs_core.FieldValuesResponseDTO, error) {
	response := &logs_core.FieldValuesResponseDTO{Values: []logs_core.FieldValueDTO{}}
	from, to := resolveTimeRange(request.TimeRange)
	records, err := q.segmentRepo.PlanSegments(projectID, q.stream, from, to, hintsFor(request.Query))
	if err != nil {
		return nil, fmt.Errorf("failed to list segments for project: %w", err)
	}
	tail, ordered := q.searchInputs(ctx, projectID, records, from, to, false)
	if len(tail) == 0 && len(ordered) == 0 {
		return response, nil
	}

	builder := newWhereBuilder(projectID)
	querySQL, queryArgs := builder.Build(request.Query)
	whereClause := "project_id = ? AND timestamp >= ? AND timestamp <= ?"
	whereArgs := append([]any{projectID.String(), from, to}, queryArgs...)
	if querySQL != "" {
		whereClause += " AND " + querySQL
	}

	limit := request.Limit
	if limit <= 0 {
		limit = defaultFieldValuesLimit
	}
	limit = min(limit, maxFieldValuesLimit)
	fieldExpr, fieldArgs := builder.groupFieldExpression(request.Field)
	result, err := q.topGroups(ctx, tail, ordered, from, to, whereClause, whereArgs, facetQuery{
		// A NULL/absent value is not a value to filter on.
		groupExpr:  "COALESCE(" + fieldExpr + ", '')",
		groupArgs:  fieldArgs,
		limit:      limit + 1,
		sampleRows: request.SampleRows,
		scanBudget: request.ScanBudget,
		tokens:     requiredTokens(request.Query),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to execute field values query: %w", err)
	}

	for _, group := range result.groups {
		if group.key == "" || len(response.Values) == limit {
			continue
		}
		response.Values = append(response.Values, logs_core.FieldValueDTO{Value: group.key, Count: group.count})
	}
	response.SampledFrom, response.MatchedRows = result.sampledFrom, result.matched
	return response, nil
}
