package logs_querying

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	logs_core "obzly/internal/features/logs/core"
	"obzly/internal/storage"
	"obzly/internal/util/attrnames"

	"github.com/google/uuid"
)

const (
	// maxRecentValues bounds FieldValuesResponseDTO.Recent.
	maxRecentValues = 200
	// defaultFacetLimit / maxFacetLimit: the querier's default and cap on a
	// facet's values.
	defaultFacetLimit = 10
	maxFacetLimit     = 1000
	// recentValuesTimeout: the facet answers without Recent rather than wait.
	recentValuesTimeout = 2 * time.Second
)

// errNotOnSources: the query filters on a field log_source_index doesn't
// hold, so it can't narrow the sources.
var errNotOnSources = errors.New("query filters on fields other than log sources")

// recentValues is the answer to a request's IncludeRecent, read while the
// facet query runs.
type recentValues struct {
	values  []logs_core.RecentFieldValueDTO
	skipped bool
}

// readRecentValues reads, for a log source field, the values whose sources
// logged in the last RecentValuesWindow or in the range, newest first, from
// log_source_index. The query narrows them when it filters only on source
// fields; level is ignored, as sources don't record it. It reads enough rows
// that maxRecentValues remain once the facet's own values are left out.
func readRecentValues(
	ctx context.Context,
	projectID uuid.UUID,
	request *logs_core.FieldValuesRequestDTO,
	now time.Time,
) (recentValues, error) {
	field := attrnames.Normalize(strings.TrimSpace(request.Field))
	if !logs_core.IsSourceIndexField(field) {
		return recentValues{}, nil
	}
	where, whereArgs, err := sourceCondition(request.Query)
	if err != nil {
		return recentValues{skipped: true}, nil
	}

	from, to := time.Unix(0, 0).UTC(), now
	if request.TimeRange != nil {
		if request.TimeRange.From != nil {
			from = request.TimeRange.From.UTC()
		}
		if request.TimeRange.To != nil {
			to = request.TimeRange.To.UTC()
		}
	}
	recentFrom := now.Add(-logs_core.RecentValuesWindow)
	facetLimit := min(request.Limit, maxFacetLimit)
	if facetLimit <= 0 {
		facetLimit = defaultFacetLimit
	}

	ctx, cancel := context.WithTimeout(ctx, recentValuesTimeout)
	defer cancel()
	// "last_seen >= recent, or inside the range": the first bound lets the
	// (project_id, last_seen) index narrow the scan.
	sqlText := `
		SELECT value, max(last_seen) AS last_seen
		FROM (SELECT attributes->>? AS value, last_seen FROM log_source_index
			WHERE project_id = ? AND last_seen >= LEAST(?::timestamptz, ?::timestamptz)
				AND (last_seen >= ? OR first_seen <= ?)`
	args := []any{field, projectID, recentFrom, from, recentFrom, to}
	if where != "" {
		sqlText += " AND " + where
		args = append(args, whereArgs...)
	}
	sqlText += `) sources
		WHERE value IS NOT NULL AND value <> ''
		GROUP BY value ORDER BY last_seen DESC, value LIMIT ?`
	args = append(args, maxRecentValues+facetLimit)

	var rows []struct {
		Value    string
		LastSeen time.Time
	}
	if err := storage.GetDb().WithContext(ctx).Raw(sqlText, args...).Scan(&rows).Error; err != nil {
		return recentValues{}, fmt.Errorf("failed to read log source index: %w", err)
	}
	result := recentValues{values: make([]logs_core.RecentFieldValueDTO, 0, len(rows))}
	for _, row := range rows {
		lastSeen := row.LastSeen.UTC()
		result.values = append(result.values, logs_core.RecentFieldValueDTO{
			Value:    row.Value,
			LastSeen: lastSeen,
			InRange:  !lastSeen.Before(from) && !lastSeen.After(to),
		})
	}
	return result, nil
}

// addRecentValues sets response.Recent to recent's values not already in
// response.Values, at most maxRecentValues.
func addRecentValues(response *logs_core.FieldValuesResponseDTO, recent recentValues) {
	response.RecentSkipped = recent.skipped
	known := make(map[string]bool, len(response.Values))
	for _, value := range response.Values {
		known[value.Value] = true
	}
	for _, value := range recent.values {
		if len(response.Recent) == maxRecentValues {
			break
		}
		if !known[value.Value] {
			response.Recent = append(response.Recent, value)
		}
	}
}

// sourceCondition is query as Postgres over log_source_index.attributes:
// "" when it doesn't narrow the sources (no query, or only level), and
// errNotOnSources when it filters on anything else.
func sourceCondition(node *logs_core.QueryNode) (string, []any, error) {
	if node == nil {
		return "", nil, nil
	}
	switch node.Type {
	case logs_core.QueryNodeTypeCondition:
		if node.Condition == nil {
			return "", nil, nil
		}
		return sourceFieldCondition(node.Condition)
	case logs_core.QueryNodeTypeLogical:
		if node.Logic == nil {
			return "", nil, nil
		}
		return sourceLogical(node.Logic)
	}
	return "", nil, nil
}

// sourceLogical combines children like the logs where builder. A child that
// doesn't narrow (level) leaves an AND as its other children, and an OR or
// NOT not narrowing at all: ignoring level may offer more values, never fewer.
func sourceLogical(logic *logs_core.LogicalNode) (string, []any, error) {
	var parts []string
	var args []any
	unconstrained := false
	for i := range logic.Children {
		part, partArgs, err := sourceCondition(&logic.Children[i])
		if err != nil {
			return "", nil, err
		}
		if part == "" {
			unconstrained = true
			continue
		}
		parts = append(parts, part)
		args = append(args, partArgs...)
	}
	switch logic.Operator {
	case logs_core.LogicalOperatorAnd:
		if len(parts) == 0 {
			return "", nil, nil
		}
		return "(" + strings.Join(parts, " AND ") + ")", args, nil
	case logs_core.LogicalOperatorOr:
		if unconstrained || len(parts) == 0 {
			return "", nil, nil
		}
		return "(" + strings.Join(parts, " OR ") + ")", args, nil
	case logs_core.LogicalOperatorNot:
		if unconstrained || len(parts) == 0 {
			return "", nil, nil
		}
		return "NOT (" + strings.Join(parts, " OR ") + ")", args, nil
	}
	return "", nil, errNotOnSources
}

func sourceFieldCondition(cond *logs_core.ConditionNode) (string, []any, error) {
	field := attrnames.Normalize(strings.TrimSpace(cond.Field))
	if field == "level" {
		return "", nil, nil
	}
	if !logs_core.IsSourceIndexField(field) {
		return "", nil, errNotOnSources
	}
	value := "COALESCE(attributes->>?, '')"
	switch cond.Operator {
	case logs_core.ConditionOperatorEquals:
		return value + " = ?", []any{field, fmt.Sprintf("%v", cond.Value)}, nil
	case logs_core.ConditionOperatorNotEquals:
		return value + " <> ?", []any{field, fmt.Sprintf("%v", cond.Value)}, nil
	case logs_core.ConditionOperatorContains:
		return "strpos(" + value + ", ?) > 0", []any{field, fmt.Sprintf("%v", cond.Value)}, nil
	case logs_core.ConditionOperatorNotContains:
		return "strpos(" + value + ", ?) = 0", []any{field, fmt.Sprintf("%v", cond.Value)}, nil
	case logs_core.ConditionOperatorIn:
		return value + " = ANY(?::text[])", []any{field, storage.TextArray(stringValues(cond.Value))}, nil
	case logs_core.ConditionOperatorNotIn:
		return "NOT (" + value + " = ANY(?::text[]))", []any{field, storage.TextArray(stringValues(cond.Value))}, nil
	case logs_core.ConditionOperatorExists:
		return value + " <> ''", []any{field}, nil
	case logs_core.ConditionOperatorNotExists:
		return value + " = ''", []any{field}, nil
	}
	return "", nil, errNotOnSources
}

func stringValues(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		values := make([]string, 0, len(typed))
		for _, item := range typed {
			values = append(values, fmt.Sprintf("%v", item))
		}
		return values
	case nil:
		return []string{}
	default:
		return []string{fmt.Sprintf("%v", typed)}
	}
}
