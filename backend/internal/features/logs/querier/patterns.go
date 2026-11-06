package logs_querier

import (
	"context"
	"fmt"

	logs_core "obzly/internal/features/logs/core"

	"github.com/google/uuid"
)

const (
	defaultPatternsLimit = 20
	maxPatternsLimit     = 200
)

// ExecutePatterns returns the most frequent message templates among logs
// matching request.Query, grouped by pattern hash (the value a pattern_hash
// filter matches); each template is derived from the group's sample.
func (q *LogQuerier) ExecutePatterns(
	projectID uuid.UUID,
	request *logs_core.PatternsRequestDTO,
) (*logs_core.PatternsResponseDTO, error) {
	empty := &logs_core.PatternsResponseDTO{Patterns: []logs_core.LogPatternDTO{}}

	from, to := resolveTimeRange(request.TimeRange)
	records, err := q.segmentRepo.PlanSegments(projectID, q.stream, from, to, hintsFor(request.Query))
	if err != nil {
		return nil, fmt.Errorf("failed to list segments for project: %w", err)
	}
	tail, ordered := q.searchInputs(context.Background(), projectID, records, from, to, false)
	if len(tail) == 0 && len(ordered) == 0 {
		return empty, nil
	}

	querySQL, queryArgs := newWhereBuilder(projectID).Build(request.Query)
	whereClause := "project_id = ? AND timestamp >= ? AND timestamp <= ?"
	whereArgs := append([]any{projectID.String(), from, to}, queryArgs...)
	if querySQL != "" {
		whereClause += " AND " + querySQL
	}

	limit := request.Limit
	if limit <= 0 {
		limit = defaultPatternsLimit
	}
	if limit > maxPatternsLimit {
		limit = maxPatternsLimit
	}

	result, err := q.topGroups(context.Background(), tail, ordered, from, to, whereClause, whereArgs, facetQuery{
		groupExpr:  storedPatternHashSQL,
		withSample: true,
		limit:      limit,
		sampleRows: request.SampleRows,
		scanBudget: request.ScanBudget,
		tokens:     requiredTokens(request.Query),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to execute patterns query: %w", err)
	}

	patterns := make([]logs_core.LogPatternDTO, 0, len(result.groups))
	for _, group := range result.groups {
		patterns = append(patterns, logs_core.LogPatternDTO{
			Hash:    group.key,
			Pattern: logs_core.MessagePattern(group.sample),
			Sample:  group.sample,
			Count:   group.count,
		})
	}
	return &logs_core.PatternsResponseDTO{
		Patterns:       patterns,
		FacetSampleDTO: logs_core.FacetSampleDTO{SampledFrom: result.sampledFrom, MatchedRows: result.matched},
	}, nil
}
