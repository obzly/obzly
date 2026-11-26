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

// maxPeerStatRows bounds the peer query's result: every server_address an
// app calls is a row, and the Service Map keeps only its top nodes anyway.
const maxPeerStatRows = 5000

// entryStatsPredicate is a request in the RED sense, over span stats: a
// span that receives work from outside the service, or starts a trace.
const entryStatsPredicate = "(kind IN ('SERVER', 'CONSUMER') OR root)"

// ServiceNodeStats returns each service's RED numbers (span stats) and the
// untraced peers its client spans call (edge stats).
func (q *SpanQuerier) ServiceNodeStats(
	ctx context.Context,
	projectID uuid.UUID,
	timeRange *spans_core.TimeRangeDTO,
	scanBudget int64,
) (*spans_core.ServiceNodeStats, error) {
	from, to := resolveTimeRange(timeRange)
	ranges := []interval{{start: from, end: to, includeEnd: true}}
	result := &spans_core.ServiceNodeStats{
		Services:  []spans_core.ServiceNodeStat{},
		PeerEdges: []spans_core.PeerCallStat{},
		Peers:     []spans_core.PeerNodeStat{},
	}

	_, err := q.runStats(ctx, projectID, statsQuery{
		scanBudget: scanBudget,
		schema:     spanStats,
		ranges:     ranges,
		selectSQL: "service_name, duration_bucket, " +
			"coalesce(sum(cnt) FILTER (WHERE " + entryStatsPredicate + "), 0)::BIGINT AS requests, " +
			"coalesce(sum(cnt) FILTER (WHERE " + entryStatsPredicate + " AND status_code = 'ERROR'), 0)::BIGINT " +
			"AS errors",
		groupSQL: " GROUP BY service_name, duration_bucket",
		finalSQL: "WITH r AS (SELECT * FROM " + query.ResultsPlaceholder + ") " +
			histogramSQL("r", "requests", []string{"service_name"}, []string{"sum(errors)::BIGINT AS errors"},
				[]float64{0.95}),
	}, func(rows *sql.Rows) error {
		for rows.Next() {
			var stat spans_core.ServiceNodeStat
			var p95 sql.NullFloat64
			if err := rows.Scan(&stat.ServiceName, &stat.Requests, &stat.Errors, &p95); err != nil {
				return fmt.Errorf("failed to scan service-node-stats row: %w", err)
			}
			if p95.Valid {
				stat.P95Ms = &p95.Float64
			}
			result.Services = append(result.Services, stat)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to execute service-node-stats query: %w", err)
	}

	// Per caller and peer, and per peer (src NULL, peer_total 1).
	peersSQL := "WITH r AS (SELECT * FROM " + query.ResultsPlaceholder + "), " +
		"m AS (SELECT src, dst, max(db_system) AS db_system, max(messaging_system) AS messaging_system, " +
		"duration_bucket, sum(cnt) AS cnt, sum(errs) AS errs, sum(duration_sum) AS duration_sum " +
		"FROM r GROUP BY src, dst, duration_bucket), " +
		"g AS (SELECT src, dst, 0 AS peer_total, db_system, messaging_system, duration_bucket, cnt, errs, " +
		"duration_sum FROM m UNION ALL SELECT NULL, dst, 1, max(db_system), max(messaging_system), duration_bucket, " +
		"sum(cnt), sum(errs), sum(duration_sum) FROM m GROUP BY dst, duration_bucket) " +
		"SELECT src, dst, peer_total, db_system, messaging_system, cnt, errs, avg_ms, p95 FROM (" +
		histogramSQL("g", "cnt", []string{"src", "dst", "peer_total"}, []string{
			"COALESCE(max(db_system), '') AS db_system", "COALESCE(max(messaging_system), '') AS messaging_system",
			"sum(errs)::BIGINT AS errs", "sum(duration_sum) / sum(cnt) AS avg_ms",
		}, []float64{0.95}) + ") ORDER BY cnt DESC LIMIT ?"
	_, err = q.runStats(ctx, projectID, statsQuery{
		scanBudget: scanBudget,
		schema:     edgeStats,
		ranges:     ranges,
		selectSQL: "src, dst, max(db_system) AS db_system, max(messaging_system) AS messaging_system, " +
			"duration_bucket, sum(cnt)::BIGINT AS cnt, sum(err_cnt)::BIGINT AS errs, sum(duration_sum) AS duration_sum",
		whereSQL:  "peer",
		groupSQL:  " GROUP BY src, dst, duration_bucket",
		finalSQL:  peersSQL,
		finalArgs: []any{maxPeerStatRows},
	}, func(rows *sql.Rows) error {
		for rows.Next() {
			var src sql.NullString
			var peer, dbSystem, messagingSystem string
			var peerTotal, calls, errors int64
			var avgMs float64
			var p95 sql.NullFloat64
			if err := rows.Scan(
				&src, &peer, &peerTotal, &dbSystem, &messagingSystem, &calls, &errors, &avgMs, &p95,
			); err != nil {
				return fmt.Errorf("failed to scan peer-call row: %w", err)
			}
			if peerTotal == 1 {
				stat := spans_core.PeerNodeStat{
					Peer: peer, DBSystem: dbSystem, MessagingSystem: messagingSystem,
					Requests: calls, Errors: errors,
				}
				if p95.Valid {
					stat.P95Ms = &p95.Float64
				}
				result.Peers = append(result.Peers, stat)
				continue
			}
			result.PeerEdges = append(result.PeerEdges, spans_core.PeerCallStat{
				SrcService: src.String, Peer: peer, DBSystem: dbSystem, MessagingSystem: messagingSystem,
				CallCount: calls, ErrorCount: errors, AvgDurationMs: avgMs,
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to execute peer-calls query: %w", err)
	}

	return result, nil
}

// ServiceErrorSpanCounts counts error spans per service within the range,
// over services plus the services owning spanIDs (both may be empty). A
// listed service with spans but no errors comes back as 0; one with no
// spans at all is absent.
func (q *SpanQuerier) ServiceErrorSpanCounts(
	projectID uuid.UUID,
	timeRange *spans_core.TimeRangeDTO,
	services []string,
	spanIDs []string,
) (map[string]int64, error) {
	result := make(map[string]int64)
	if len(services) == 0 && len(spanIDs) == 0 {
		return result, nil
	}
	from, to := resolveTimeRange(timeRange)
	tail, records, err := q.inputs(projectID, from, to, segments.PruningHints{})
	if err != nil {
		return nil, err
	}

	args := []any{projectID.String(), from, to}
	var members []string
	if len(services) > 0 {
		members = append(members, "service_name IN ("+placeholders(len(services))+")")
		for _, service := range services {
			args = append(args, service)
		}
	}
	if len(spanIDs) > 0 {
		members = append(members,
			"service_name IN (SELECT service_name FROM segs WHERE span_id IN ("+placeholders(len(spanIDs))+"))")
		for _, spanID := range spanIDs {
			args = append(args, spanID)
		}
	}

	// One fragment: a span id's owner may sit in another fragment's files.
	sqlText := "WITH segs AS (SELECT service_name, span_id, status_code FROM " + query.SourcePlaceholder +
		" WHERE project_id = ? AND start_time >= ? AND start_time <= ?) " +
		"SELECT service_name, count(*) FILTER (WHERE status_code = 'ERROR') FROM segs " +
		"WHERE " + strings.Join(members, " OR ") + " GROUP BY service_name"
	_, err = q.run(query.Request{
		Segments:       records,
		LocalFiles:     tail,
		SingleSQL:      sqlText,
		SingleArgs:     args,
		SingleFragment: true,
	}, func(rows *sql.Rows) error {
		for rows.Next() {
			var service string
			var count int64
			if err := rows.Scan(&service, &count); err != nil {
				return fmt.Errorf("failed to scan service-error-spans row: %w", err)
			}
			result[service] = count
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to execute service-error-spans query: %w", err)
	}
	return result, nil
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}
