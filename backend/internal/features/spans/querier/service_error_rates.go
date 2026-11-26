package spans_querier

import (
	"context"
	"database/sql"
	"fmt"

	spans_core "obzly/internal/features/spans/core"
	"obzly/internal/query"

	"github.com/google/uuid"
)

// ServiceErrorRates counts entry spans per service over [PreviousFrom, To]
// in one span stats query, split at From.
func (q *SpanQuerier) ServiceErrorRates(
	ctx context.Context,
	projectID uuid.UUID,
	request *spans_core.ServiceErrorRatesQuery,
) ([]spans_core.ServiceErrorRate, error) {
	result := []spans_core.ServiceErrorRate{}
	_, err := q.runStats(ctx, projectID, statsQuery{
		schema: spanStats,
		ranges: []interval{
			{start: request.PreviousFrom, end: request.From},
			{start: request.From, end: request.To, includeEnd: true},
		},
		selectSQL: "service_name, " +
			"coalesce(sum(cnt) FILTER (WHERE timestamp >= ?), 0)::BIGINT AS requests, " +
			"coalesce(sum(cnt) FILTER (WHERE timestamp >= ? AND status_code = 'ERROR'), 0)::BIGINT AS errors, " +
			"coalesce(sum(cnt) FILTER (WHERE timestamp < ?), 0)::BIGINT AS previous_requests, " +
			"coalesce(sum(cnt) FILTER (WHERE timestamp < ? AND status_code = 'ERROR'), 0)::BIGINT AS previous_errors",
		selectArgs: []any{request.From, request.From, request.From, request.From},
		whereSQL:   entryStatsPredicate,
		groupSQL:   " GROUP BY service_name",
		finalSQL: "SELECT service_name, sum(requests)::BIGINT AS requests, sum(errors)::BIGINT AS errors, " +
			"sum(previous_requests)::BIGINT, sum(previous_errors)::BIGINT FROM " + query.ResultsPlaceholder +
			" GROUP BY service_name ORDER BY errors DESC, requests DESC, service_name LIMIT ?",
		finalArgs:  []any{request.Limit},
		scanBudget: request.ScanBudget,
	}, func(rows *sql.Rows) error {
		for rows.Next() {
			var row spans_core.ServiceErrorRate
			if err := rows.Scan(
				&row.ServiceName, &row.Requests, &row.Errors, &row.PreviousRequests, &row.PreviousErrors,
			); err != nil {
				return fmt.Errorf("failed to scan service-error-rates row: %w", err)
			}
			result = append(result, row)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to execute service-error-rates query: %w", err)
	}
	return result, nil
}
