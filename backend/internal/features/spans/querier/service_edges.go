package spans_querier

import (
	"database/sql"
	"fmt"

	spans_core "obzly/internal/features/spans/core"
	"obzly/internal/query"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
)

// ServiceCallEdges aggregates spans into parent-service -> child-service
// call pairs within the time range, for the Service Map. The self-join goes
// through a CTE so the segment list and its args appear once in the query.
func (q *SpanQuerier) ServiceCallEdges(
	projectID uuid.UUID,
	timeRange *spans_core.TimeRangeDTO,
) ([]spans_core.ServiceCallEdge, error) {
	from, to := resolveTimeRange(timeRange)
	tail, records, err := q.inputs(projectID, from, to, segments.PruningHints{})
	if err != nil {
		return nil, err
	}

	// A parent and its child can be in different segments, so the join
	// can't be split by segment: it runs as one in-process query.
	const durationMsExpr = "(epoch_ns(child.end_time) - epoch_ns(child.start_time)) / 1000000.0"
	sqlText := "WITH segs AS (SELECT * FROM " + query.SourcePlaceholder + ") " +
		"SELECT parent.service_name AS src, child.service_name AS dst, " +
		"COUNT(*) AS call_count, " +
		"SUM(CASE WHEN child.status_code = 'ERROR' THEN 1 ELSE 0 END) AS error_count, " +
		"AVG(" + durationMsExpr + ") AS avg_duration_ms " +
		"FROM segs child JOIN segs parent " +
		"ON child.trace_id = parent.trace_id AND child.parent_span_id = parent.span_id " +
		"WHERE child.project_id = ? AND child.parent_span_id != '' " +
		"AND child.start_time >= ? AND child.start_time <= ? " +
		"GROUP BY 1, 2"
	edges := make([]spans_core.ServiceCallEdge, 0)
	_, err = q.run(query.Request{
		Segments:       records,
		LocalFiles:     tail,
		SingleSQL:      sqlText,
		SingleArgs:     []any{projectID.String(), from, to},
		SingleFragment: true,
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
