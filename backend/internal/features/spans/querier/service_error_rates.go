package spans_querier

import (
	"context"
	"database/sql"
	"fmt"

	spans_core "obzly/internal/features/spans/core"
	"obzly/internal/query"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
)

// ServiceErrorRates counts entry spans per service over [PreviousFrom, To]
// in one scan, split at From. Counts add up across fragments, so each
// fragment groups its own files and the final query sums them.
func (q *SpanQuerier) ServiceErrorRates(
	ctx context.Context,
	projectID uuid.UUID,
	request *spans_core.ServiceErrorRatesQuery,
) ([]spans_core.ServiceErrorRate, error) {
	result := []spans_core.ServiceErrorRate{}
	tail, records, err := q.inputs(projectID, request.PreviousFrom, request.To, segments.PruningHints{})
	if err != nil {
		return nil, err
	}

	fragmentSQL := "SELECT service_name, " +
		"count(*) FILTER (WHERE start_time >= ?) AS requests, " +
		"count(*) FILTER (WHERE start_time >= ? AND status_code = 'ERROR') AS errors, " +
		"count(*) FILTER (WHERE start_time < ?) AS previous_requests, " +
		"count(*) FILTER (WHERE start_time < ? AND status_code = 'ERROR') AS previous_errors " +
		"FROM " + query.SourcePlaceholder +
		" WHERE project_id = ? AND start_time >= ? AND start_time <= ? AND " + entrySpanPredicate +
		" GROUP BY service_name"
	fragmentArgs := []any{
		request.From, request.From, request.From, request.From,
		projectID.String(), request.PreviousFrom, request.To,
	}
	order := " ORDER BY errors DESC, requests DESC, service_name LIMIT ?"
	finalSQL := "SELECT service_name, sum(requests)::BIGINT AS requests, sum(errors)::BIGINT AS errors, " +
		"sum(previous_requests)::BIGINT, sum(previous_errors)::BIGINT FROM " + query.ResultsPlaceholder +
		" GROUP BY service_name" + order

	_, err = q.runContext(ctx, query.Request{
		Segments:     records,
		LocalFiles:   tail,
		FragmentSQL:  fragmentSQL,
		FragmentArgs: fragmentArgs,
		FinalSQL:     finalSQL,
		FinalArgs:    []any{request.Limit},
		SingleSQL:    fragmentSQL + order,
		SingleArgs:   append(append([]any{}, fragmentArgs...), request.Limit),
		ScanBudget:   request.ScanBudget,
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
