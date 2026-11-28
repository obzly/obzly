package spans_querier

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	spans_core "obzly/internal/features/spans/core"
	"obzly/internal/query"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
)

// TraceSummary answers the Traces summary: the tiles (both periods' totals
// and the current period's buckets) from span stats, rollups where they
// answer, and the capped matching-traces count.
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

// traceSummaryTiles reads [PreviousFrom, From) and [From, To] in one stats
// query. The duration bounds apply to each span: exactly where spans are read
// raw, at histogram resolution (about 9%) on rollup rows.
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
	where, whereArgs := summaryFilters(request)
	stepMs := max(request.StepMs, 1)

	// Previous-period rows get a NULL bucket; totals are per period, buckets
	// only for the current one.
	finalSQL := "WITH m AS (SELECT is_current, bucket, duration_bucket, sum(cnt) AS cnt, sum(errs) AS errs " +
		"FROM " + query.ResultsPlaceholder + " GROUP BY ALL), " +
		"g AS (SELECT is_current, NULL::BIGINT AS bucket, 1 AS is_total, duration_bucket, sum(cnt) AS cnt, " +
		"sum(errs) AS errs FROM m GROUP BY is_current, duration_bucket " +
		"UNION ALL SELECT is_current, bucket, 0 AS is_total, duration_bucket, cnt, errs FROM m WHERE bucket IS NOT NULL) " +
		histogramSQL("g", "cnt", []string{"is_current", "bucket", "is_total"},
			[]string{"sum(errs)::BIGINT AS errs"}, []float64{0.5, 0.95})
	_, err := q.runStats(ctx, projectID, statsQuery{
		schema: spanStats,
		ranges: []interval{{start: previousFrom, end: from}, {start: from, end: to, includeEnd: true}},
		hints:  hints,
		grain:  time.Duration(stepMs) * time.Millisecond,
		selectSQL: "timestamp >= ? AS is_current, CASE WHEN timestamp >= ? THEN epoch_ns(timestamp) // ? END AS bucket, " +
			"duration_bucket, sum(cnt)::BIGINT AS cnt, (sum(cnt) FILTER (WHERE status_code = 'ERROR'))::BIGINT AS errs",
		selectArgs: []any{from, from, stepMs * 1_000_000},
		whereSQL:   where,
		whereArgs:  whereArgs,
		groupSQL:   " GROUP BY 1, 2, 3",
		finalSQL:   finalSQL,
		scanBudget: request.ScanBudget,
	}, func(rows *sql.Rows) error {
		for rows.Next() {
			var isCurrent bool
			var bucket, errors sql.NullInt64
			var isTotal, count int64
			var p50, p95 sql.NullFloat64
			if err := rows.Scan(&isCurrent, &bucket, &isTotal, &count, &errors, &p50, &p95); err != nil {
				return fmt.Errorf("failed to scan trace summary row: %w", err)
			}
			period := summaryPeriod(count, errors.Int64, p50, p95)
			switch {
			case isTotal == 1 && isCurrent:
				result.Current = period
			case isTotal == 1:
				result.Previous = period
			case isCurrent && bucket.Valid:
				result.Buckets = append(result.Buckets, spans_core.TraceSummaryBucket{
					Index: bucket.Int64, Count: count, Errors: errors.Int64, P50Ms: period.P50Ms, P95Ms: period.P95Ms,
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
// MatchCap+1. Without a membership filter it's a count of root spans (span
// stats); with a service and/or errors filter, of root spans by their
// trace's services and errors (trace stats); with an operation filter, see
// matchingTracesRaw. Duration bounds are exact on raw spans and at
// histogram resolution on rollup rows, as in the tiles.
func (q *SpanQuerier) matchingTraces(
	ctx context.Context,
	projectID uuid.UUID,
	request *spans_core.TraceSummaryQuery,
) (int64, error) {
	if request.OperationName != "" {
		return q.matchingTracesRaw(ctx, projectID, request)
	}
	schema := spanStats
	where := []string{"root"}
	var whereArgs []any
	if request.ServiceName != "" || request.ErrorsOnly {
		schema = traceStats
		where = []string{"service_name IS NULL"}
		if request.ServiceName != "" {
			where = []string{"service_name = ?"}
			whereArgs = append(whereArgs, request.ServiceName)
		}
		if request.ErrorsOnly {
			where = append(where, "errors")
		}
	}
	if request.MinDurationMs != nil {
		where = append(where, "duration_ms >= ?")
		whereArgs = append(whereArgs, *request.MinDurationMs)
	}
	if request.MaxDurationMs != nil {
		where = append(where, "duration_ms <= ?")
		whereArgs = append(whereArgs, *request.MaxDurationMs)
	}
	from, to := resolveTimeRange(&spans_core.TimeRangeDTO{From: &request.From, To: &request.To})
	var count int64
	_, err := q.runStats(ctx, projectID, statsQuery{
		schema:     schema,
		ranges:     []interval{{start: from, end: to, includeEnd: true}},
		selectSQL:  "sum(cnt)::BIGINT AS n",
		whereSQL:   strings.Join(where, " AND "),
		whereArgs:  whereArgs,
		finalSQL:   "SELECT sum(n)::BIGINT FROM " + query.ResultsPlaceholder,
		scanBudget: request.ScanBudget,
	}, func(rows *sql.Rows) error {
		for rows.Next() {
			var n sql.NullInt64
			if err := rows.Scan(&n); err != nil {
				return fmt.Errorf("failed to scan matching-traces row: %w", err)
			}
			count += n.Int64
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("failed to execute matching-traces query: %w", err)
	}
	return min(count, int64(request.MatchCap+1)), nil
}

// matchingTracesRaw counts with an operation filter (any span of the
// trace with that name; the other filters apply too), which needs raw
// spans. It counts newest first, window by window (newestFirstWindows), and stops once the cap is
// reached: a root's children start after it, so a window holding the root
// holds every span that can make its trace match.
func (q *SpanQuerier) matchingTracesRaw(
	ctx context.Context,
	projectID uuid.UUID,
	request *spans_core.TraceSummaryQuery,
) (int64, error) {
	from, to := resolveTimeRange(&spans_core.TimeRangeDTO{From: &request.From, To: &request.To})
	limit := request.MatchCap + 1

	where := []string{"parent_span_id = ''"}
	var whereArgs []any
	if request.MinDurationMs != nil {
		where = append(where, spans_core.SpanDurationMsSQL+" >= ?")
		whereArgs = append(whereArgs, *request.MinDurationMs)
	}
	if request.MaxDurationMs != nil {
		where = append(where, spans_core.SpanDurationMsSQL+" <= ?")
		whereArgs = append(whereArgs, *request.MaxDurationMs)
	}
	if request.ServiceName != "" {
		where = append(where, "trace_id IN (SELECT trace_id FROM segs WHERE service_name = ?)")
		whereArgs = append(whereArgs, request.ServiceName)
	}
	if request.OperationName != "" {
		where = append(where, "trace_id IN (SELECT trace_id FROM segs WHERE name = ?)")
		whereArgs = append(whereArgs, request.OperationName)
	}
	if request.ErrorsOnly {
		where = append(where, "trace_id IN (SELECT trace_id FROM segs WHERE status_code = 'ERROR')")
	}
	// A trace's spans can sit in several fragments: one fragment.
	countSQL := "WITH segs AS (SELECT trace_id, parent_span_id, service_name, name, status_code, " +
		"start_time, end_time FROM " + query.SourcePlaceholder +
		" WHERE project_id = ? AND start_time >= ? AND start_time <= ?) " +
		"SELECT count(*) FROM (SELECT 1 FROM segs WHERE " + strings.Join(where, " AND ") + " LIMIT ?)"

	var count int64
	for _, windowFrom := range newestFirstWindows(from, to) {
		tail, records, err := q.inputs(projectID, windowFrom, to, segments.PruningHints{})
		if err != nil {
			return 0, err
		}
		args := append(append([]any{projectID.String(), windowFrom, to}, whereArgs...), limit)
		count = 0
		_, err = q.runContext(ctx, query.Request{
			Segments:       records,
			LocalFiles:     tail,
			SingleSQL:      countSQL,
			SingleArgs:     args,
			SingleFragment: true,
			ScanBudget:     request.ScanBudget,
		}, func(rows *sql.Rows) error {
			for rows.Next() {
				var n sql.NullInt64
				if err := rows.Scan(&n); err != nil {
					return fmt.Errorf("failed to scan matching-traces row: %w", err)
				}
				count += n.Int64
			}
			return nil
		})
		if err != nil {
			return 0, fmt.Errorf("failed to execute matching-traces query: %w", err)
		}
		if count >= int64(limit) {
			break
		}
	}
	return min(count, int64(limit)), nil
}

// summaryFilters are the summary's span filters over spanStats columns.
func summaryFilters(request *spans_core.TraceSummaryQuery) (string, []any) {
	var where []string
	var args []any
	if request.RootSpansOnly {
		where = append(where, "root")
	}
	if request.ServiceName != "" {
		where = append(where, "service_name = ?")
		args = append(args, request.ServiceName)
	}
	if request.OperationName != "" {
		where = append(where, "name = ?")
		args = append(args, request.OperationName)
	}
	if request.ErrorsOnly {
		where = append(where, "status_code = 'ERROR'")
	}
	if request.MinDurationMs != nil {
		where = append(where, "duration_ms >= ?")
		args = append(args, *request.MinDurationMs)
	}
	if request.MaxDurationMs != nil {
		where = append(where, "duration_ms <= ?")
		args = append(args, *request.MaxDurationMs)
	}
	return strings.Join(where, " AND "), args
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
