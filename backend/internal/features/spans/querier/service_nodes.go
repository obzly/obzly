package spans_querier

import (
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

// entrySpanPredicate is a request in the RED sense: a span that receives
// work from outside the service, or starts a trace.
const entrySpanPredicate = "(kind IN ('SERVER', 'CONSUMER') OR parent_span_id = '')"

const spanDurationMsExpr = "(epoch_ns(end_time) - epoch_ns(start_time)) / 1000000.0"

// peerNameExpr names the peer a client span calls, by the attributes OTel
// instrumentations set (normalized at ingest), most specific first.
const peerNameExpr = "COALESCE(" +
	"NULLIF(json_extract_string(attributes, '$.peer_service'), ''), " +
	"NULLIF(json_extract_string(attributes, '$.server_address'), ''), " +
	"NULLIF(json_extract_string(attributes, '$.net_peer_name'), ''), " +
	"NULLIF(json_extract_string(attributes, '$.db_system'), ''), " +
	"NULLIF(json_extract_string(attributes, '$.messaging_system'), ''))"

// ServiceNodeStats returns each service's RED numbers and the untraced peers
// its client spans call. Both queries run as one fragment each: a
// quantile has no mergeable partial, and the peer query's anti-join must
// see every span of a trace.
func (q *SpanQuerier) ServiceNodeStats(
	projectID uuid.UUID,
	timeRange *spans_core.TimeRangeDTO,
) (*spans_core.ServiceNodeStats, error) {
	from, to := resolveTimeRange(timeRange)
	tail, records, err := q.inputs(projectID, from, to, segments.PruningHints{})
	if err != nil {
		return nil, err
	}
	result := &spans_core.ServiceNodeStats{
		Services:  []spans_core.ServiceNodeStat{},
		PeerEdges: []spans_core.PeerCallStat{},
		Peers:     []spans_core.PeerNodeStat{},
	}

	servicesSQL := "SELECT service_name, " +
		"count(*) FILTER (WHERE " + entrySpanPredicate + ") AS requests, " +
		"count(*) FILTER (WHERE " + entrySpanPredicate + " AND status_code = 'ERROR') AS errors, " +
		"approx_quantile(" + spanDurationMsExpr + ", 0.95) FILTER (WHERE " + entrySpanPredicate + ") AS p95 " +
		"FROM " + query.SourcePlaceholder +
		" WHERE project_id = ? AND start_time >= ? AND start_time <= ? GROUP BY service_name"
	_, err = q.run(query.Request{
		Segments:       records,
		LocalFiles:     tail,
		SingleSQL:      servicesSQL,
		SingleArgs:     []any{projectID.String(), from, to},
		SingleFragment: true,
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

	// A client span with a child span was answered by a traced service (the
	// call is already a ServiceCallEdges edge); only childless ones name a
	// peer with no spans of its own.
	peersSQL := "WITH segs AS (SELECT * FROM " + query.SourcePlaceholder + "), " +
		"client AS (SELECT trace_id, span_id, service_name, status_code, " +
		spanDurationMsExpr + " AS duration_ms, " +
		"NULLIF(json_extract_string(attributes, '$.db_system'), '') AS db_system, " +
		"NULLIF(json_extract_string(attributes, '$.messaging_system'), '') AS messaging_system, " +
		peerNameExpr + " AS peer " +
		"FROM segs WHERE project_id = ? AND start_time >= ? AND start_time <= ? " +
		"AND kind IN ('CLIENT', 'PRODUCER')) " +
		"SELECT c.service_name, c.peer, GROUPING(c.service_name) AS peer_total, " +
		"COALESCE(max(c.db_system), ''), COALESCE(max(c.messaging_system), ''), " +
		"count(*) AS calls, count(*) FILTER (WHERE c.status_code = 'ERROR') AS errors, " +
		"avg(c.duration_ms), approx_quantile(c.duration_ms, 0.95) " +
		"FROM client c WHERE c.peer IS NOT NULL AND c.peer <> c.service_name " +
		"AND NOT EXISTS (SELECT 1 FROM segs child WHERE child.project_id = ? AND child.start_time >= ? " +
		"AND child.trace_id = c.trace_id AND child.parent_span_id = c.span_id) " +
		"GROUP BY GROUPING SETS ((c.service_name, c.peer), (c.peer)) " +
		"ORDER BY calls DESC LIMIT ?"
	_, err = q.run(query.Request{
		Segments:       records,
		LocalFiles:     tail,
		SingleSQL:      peersSQL,
		SingleArgs:     []any{projectID.String(), from, to, projectID.String(), from, maxPeerStatRows},
		SingleFragment: true,
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
