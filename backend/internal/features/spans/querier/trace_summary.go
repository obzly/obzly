package spans_querier

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	spans_core "obzly/internal/features/spans/core"
	"obzly/internal/query"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
)

// TraceSummary answers the Traces summary in at most two scans: the tiles
// (both periods' totals and the current period's buckets, one GROUPING SETS
// query over [PreviousFrom, To]) and the capped matching-traces count.
func (q *SpanQuerier) TraceSummary(
	ctx context.Context,
	projectID uuid.UUID,
	request *spans_core.TraceSummaryQuery,
) (*spans_core.TraceSummaryResult, error) {
	result := &spans_core.TraceSummaryResult{Buckets: []spans_core.TraceSummaryBucket{}}
	if !request.CountOnly {
		if err := q.traceSummaryTiles(ctx, projectID, request, result); err != nil {
			return nil, err
		}
	}
	count, err := q.matchingTraces(ctx, projectID, request)
	if err != nil {
		return nil, err
	}
	result.MatchingTraces = count
	return result, nil
}

func (q *SpanQuerier) traceSummaryTiles(
	ctx context.Context,
	projectID uuid.UUID,
	request *spans_core.TraceSummaryQuery,
	result *spans_core.TraceSummaryResult,
) error {
	previousFrom, _ := resolveTimeRange(&spans_core.TimeRangeDTO{From: &request.PreviousFrom})
	from, to := resolveTimeRange(&spans_core.TimeRangeDTO{From: &request.From, To: &request.To})
	var hints segments.PruningHints
	if request.ServiceName != "" {
		hints.Services = []string{request.ServiceName}
	}
	tail, records, err := q.inputs(projectID, previousFrom, to, hints)
	if err != nil {
		return err
	}

	where := []string{"project_id = ?", "start_time >= ?", "start_time <= ?"}
	whereArgs := []any{projectID.String(), previousFrom, to}
	if request.RootSpansOnly {
		where = append(where, "parent_span_id = ''")
	}
	if request.ServiceName != "" {
		where = append(where, "service_name = ?")
		whereArgs = append(whereArgs, request.ServiceName)
	}
	if request.OperationName != "" {
		where = append(where, "name = ?")
		whereArgs = append(whereArgs, request.OperationName)
	}
	if request.ErrorsOnly {
		where = append(where, "status_code = 'ERROR'")
	}
	if request.MinDurationMs != nil {
		where = append(where, spanDurationMsExpr+" >= ?")
		whereArgs = append(whereArgs, *request.MinDurationMs)
	}
	if request.MaxDurationMs != nil {
		where = append(where, spanDurationMsExpr+" <= ?")
		whereArgs = append(whereArgs, *request.MaxDurationMs)
	}

	stepNs := max(request.StepMs, 1) * 1_000_000
	// Previous-period rows get a NULL bucket: their (is_current, bucket) set
	// repeats the period total and is skipped.
	sqlText := "SELECT is_current, bucket, GROUPING(bucket) AS is_total, count(*), " +
		"count(*) FILTER (WHERE is_error), approx_quantile(duration_ms, 0.5), approx_quantile(duration_ms, 0.95) " +
		"FROM (SELECT start_time >= ? AS is_current, " +
		"CASE WHEN start_time >= ? THEN epoch_ns(start_time) // ? END AS bucket, " +
		"status_code = 'ERROR' AS is_error, " + spanDurationMsExpr + " AS duration_ms " +
		"FROM " + query.SourcePlaceholder + " WHERE " + strings.Join(where, " AND ") + ") " +
		"GROUP BY GROUPING SETS ((is_current), (is_current, bucket))"
	args := append([]any{from, from, stepNs}, whereArgs...)

	_, err = q.runContext(ctx, query.Request{
		Segments:       records,
		LocalFiles:     tail,
		SingleSQL:      sqlText,
		SingleArgs:     args,
		SingleFragment: true,
		ScanBudget:     request.ScanBudget,
	}, func(rows *sql.Rows) error {
		for rows.Next() {
			var isCurrent bool
			var bucket sql.NullInt64
			var isTotal, count, errors int64
			var p50, p95 sql.NullFloat64
			if err := rows.Scan(&isCurrent, &bucket, &isTotal, &count, &errors, &p50, &p95); err != nil {
				return fmt.Errorf("failed to scan trace summary row: %w", err)
			}
			switch {
			case isTotal == 1 && isCurrent:
				result.Current = summaryPeriod(count, errors, p50, p95)
			case isTotal == 1:
				result.Previous = summaryPeriod(count, errors, p50, p95)
			case isCurrent && bucket.Valid:
				period := summaryPeriod(count, errors, p50, p95)
				result.Buckets = append(result.Buckets, spans_core.TraceSummaryBucket{
					Index: bucket.Int64, Count: count, Errors: errors, P50Ms: period.P50Ms, P95Ms: period.P95Ms,
				})
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to execute trace summary query: %w", err)
	}
	return nil
}

// matchingTraces counts traces whose root span starts in [From, To] and
// that match the filters as ListTraces applies them (service and operation:
// any span; errors: any span), except that the duration bounds apply to the
// root span, which spans the trace in practice. Counting stops at
// MatchCap+1, so a big window costs at most that many matches past the scan.
func (q *SpanQuerier) matchingTraces(
	ctx context.Context,
	projectID uuid.UUID,
	request *spans_core.TraceSummaryQuery,
) (int64, error) {
	from, to := resolveTimeRange(&spans_core.TimeRangeDTO{From: &request.From, To: &request.To})
	tail, records, err := q.inputs(projectID, from, to, segments.PruningHints{})
	if err != nil {
		return 0, err
	}
	limit := request.MatchCap + 1

	var rootWhere []string
	var rootArgs []any
	if request.MinDurationMs != nil {
		rootWhere = append(rootWhere, spanDurationMsExpr+" >= ?")
		rootArgs = append(rootArgs, *request.MinDurationMs)
	}
	if request.MaxDurationMs != nil {
		rootWhere = append(rootWhere, spanDurationMsExpr+" <= ?")
		rootArgs = append(rootArgs, *request.MaxDurationMs)
	}

	var memberships []string
	var membershipArgs []any
	if request.ServiceName != "" {
		memberships = append(memberships, "trace_id IN (SELECT trace_id FROM segs WHERE service_name = ?)")
		membershipArgs = append(membershipArgs, request.ServiceName)
	}
	if request.OperationName != "" {
		memberships = append(memberships, "trace_id IN (SELECT trace_id FROM segs WHERE name = ?)")
		membershipArgs = append(membershipArgs, request.OperationName)
	}
	if request.ErrorsOnly {
		memberships = append(memberships, "trace_id IN (SELECT trace_id FROM segs WHERE status_code = 'ERROR')")
	}

	var count int64
	scan := func(rows *sql.Rows) error {
		for rows.Next() {
			var n sql.NullInt64
			if err := rows.Scan(&n); err != nil {
				return fmt.Errorf("failed to scan matching-traces row: %w", err)
			}
			count += n.Int64
		}
		return nil
	}

	if len(memberships) == 0 {
		// Root spans alone: each fragment counts its own up to the cap.
		where := append([]string{"project_id = ?", "start_time >= ?", "start_time <= ?", "parent_span_id = ''"},
			rootWhere...)
		args := append(append([]any{projectID.String(), from, to}, rootArgs...), limit)
		countSQL := "SELECT count(*) AS n FROM (SELECT 1 FROM " + query.SourcePlaceholder +
			" WHERE " + strings.Join(where, " AND ") + " LIMIT ?)"
		_, err = q.runContext(ctx, query.Request{
			Segments:     records,
			LocalFiles:   tail,
			FragmentSQL:  countSQL,
			FragmentArgs: args,
			FinalSQL:     "SELECT sum(n)::BIGINT FROM " + query.ResultsPlaceholder,
			SingleSQL:    countSQL,
			SingleArgs:   args,
			ScanBudget:   request.ScanBudget,
		}, scan)
	} else {
		// A trace's spans can sit in several fragments: one fragment.
		where := append([]string{"parent_span_id = ''"}, rootWhere...)
		where = append(where, memberships...)
		args := append(append(append([]any{projectID.String(), from, to}, rootArgs...), membershipArgs...), limit)
		countSQL := "WITH segs AS (SELECT trace_id, parent_span_id, service_name, name, status_code, " +
			"start_time, end_time FROM " + query.SourcePlaceholder +
			" WHERE project_id = ? AND start_time >= ? AND start_time <= ?) " +
			"SELECT count(*) FROM (SELECT 1 FROM segs WHERE " + strings.Join(where, " AND ") + " LIMIT ?)"
		_, err = q.runContext(ctx, query.Request{
			Segments:       records,
			LocalFiles:     tail,
			SingleSQL:      countSQL,
			SingleArgs:     args,
			SingleFragment: true,
			ScanBudget:     request.ScanBudget,
		}, scan)
	}
	if err != nil {
		return 0, fmt.Errorf("failed to execute matching-traces query: %w", err)
	}
	return min(count, int64(limit)), nil
}

func summaryPeriod(count, errors int64, p50, p95 sql.NullFloat64) spans_core.TraceSummaryPeriod {
	period := spans_core.TraceSummaryPeriod{Count: count, Errors: errors}
	if p50.Valid {
		period.P50Ms = &p50.Float64
	}
	if p95.Valid {
		period.P95Ms = &p95.Float64
	}
	return period
}
