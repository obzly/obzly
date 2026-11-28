package spans_querier

import (
	"context"
	"database/sql"
	"fmt"

	spans_core "obzly/internal/features/spans/core"
	"obzly/internal/query"

	"github.com/google/uuid"
)

// ServiceCallEdges aggregates parent-service -> child-service call pairs
// within the time range, for the Service Map, from edge stats: rollups where
// built, else a join of each raw input's spans in one fragment.
func (q *SpanQuerier) ServiceCallEdges(
	ctx context.Context,
	projectID uuid.UUID,
	timeRange *spans_core.TimeRangeDTO,
	scanBudget int64,
) ([]spans_core.ServiceCallEdge, error) {
	from, to := resolveTimeRange(timeRange)
	edges := make([]spans_core.ServiceCallEdge, 0)
	_, err := q.runStats(ctx, projectID, statsQuery{
		scanBudget: scanBudget,
		schema:     edgeStats,
		ranges:     []interval{{start: from, end: to, includeEnd: true}},
		selectSQL:  "src, dst, sum(cnt)::BIGINT AS cnt, sum(err_cnt)::BIGINT AS errs, sum(duration_sum) AS duration_sum",
		whereSQL:   "NOT peer",
		groupSQL:   " GROUP BY src, dst",
		finalSQL: "SELECT src, dst, sum(cnt)::BIGINT, sum(errs)::BIGINT, sum(duration_sum) / sum(cnt) FROM " +
			query.ResultsPlaceholder + " GROUP BY src, dst",
	}, func(rows *sql.Rows) error {
		for rows.Next() {
			var edge spans_core.ServiceCallEdge
			if err := rows.Scan(
				&edge.SrcService, &edge.DstService, &edge.CallCount, &edge.ErrorCount, &edge.AvgDurationMs,
			); err != nil {
				return fmt.Errorf("failed to scan service-call-edge row: %w", err)
			}
			edges = append(edges, edge)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to execute service-call-edges query: %w", err)
	}

	return edges, nil
}
