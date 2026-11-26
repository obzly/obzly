package spans_querier

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	spans_core "obzly/internal/features/spans/core"
	"obzly/internal/query"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
)

// Stats queries (summary tiles, dashboard panels, service errors, Service
// Map) are written once over a relation and run over each input: rollup
// rows where a rollup answers, raw spans projected to the same columns
// elsewhere. Every fragment returns partials (sums, and duration histogram
// counts for quantiles), so any mix of inputs merges in the final query.
//
// spanStats' relation is spans_1m's columns: timestamp, service_name, name,
// kind, status_code, root, duration_bucket, cnt, duration_sum, plus
// duration_ms for filters (a raw span's exact duration; a rollup row's
// bucket value) and, when asked, the k8s columns. A raw span is a row with
// cnt 1 and its exact start as timestamp.
//
// edgeStats' relation is spans_1m_edges' columns: timestamp, src, dst, peer,
// db_system, messaging_system, duration_bucket, cnt, err_cnt, duration_sum.
//
// traceStats' relation is spans_1m_traces' columns: timestamp (a root
// span's start), service_name (NULL: every root; else roots whose trace has
// a span of that service), errors (the trace has an ERROR span),
// duration_bucket (the root's), cnt, plus duration_ms for filters.

// statsSchema is which relation a stats query reads.
type statsSchema int

// edgeJoinMargin: a raw call's parent (or a client span's child) is looked
// for this far around the ranges; a span starts after its parent.
const edgeJoinMargin = 5 * time.Minute

const (
	spanStats statsSchema = iota
	edgeStats
	traceStats
)

// statsQuery is one query over a stats relation. Its fragment is
// SELECT selectSQL FROM <relation> [WHERE whereSQL] groupSQL; finalSQL merges
// the fragments' rows (query.ResultsPlaceholder, once, before any argument).
type statsQuery struct {
	schema statsSchema
	ranges []interval
	hints  segments.PruningHints
	// grain is the query's time bucket width (0: none): a rollup answers only
	// if its rows each fall in one bucket.
	grain time.Duration
	// rawOnly reads raw spans only (filters a rollup can't answer).
	rawOnly    bool
	k8s        bool
	selectSQL  string
	selectArgs []any
	whereSQL   string
	whereArgs  []any
	groupSQL   string
	finalSQL   string
	finalArgs  []any
	scanBudget int64
}

// runStats plans and runs sq; false when there was nothing to read.
func (q *SpanQuerier) runStats(
	ctx context.Context,
	projectID uuid.UUID,
	sq statsQuery,
	scan func(*sql.Rows) error,
) (bool, error) {
	var tiers rollupTiers
	if !sq.rawOnly {
		tiers = sq.schema.tiers()
	}
	inputs, err := q.statsInputs(projectID, sq.ranges, sq.hints, tiersFor(tiers, sq.grain))
	if err != nil {
		return false, err
	}

	where := ""
	if sq.whereSQL != "" {
		where = " WHERE " + sq.whereSQL
	}
	built := make([]query.Source, 0, len(inputs))
	empty := true
	for _, input := range inputs {
		relation, relationArgs := sq.schema.relation(input, projectID, rangesOf(input.kind, sq.ranges), sq.k8s)
		args := append(append(append([]any{}, sq.selectArgs...), relationArgs...), sq.whereArgs...)
		built = append(built, query.Source{
			Stream:       input.stream,
			Segments:     input.records,
			LocalFiles:   input.tail,
			FragmentSQL:  "SELECT " + sq.selectSQL + " FROM " + relation + where + sq.groupSQL,
			FragmentArgs: args,
			// A raw call's parent and child (a root and its trace) must meet
			// in one fragment.
			SingleFragment: sq.schema != spanStats && !input.rollup,
		})
		empty = empty && len(input.records) == 0 && len(input.tail) == 0
	}
	if empty {
		return false, nil
	}

	main := built[0]
	err = q.coordinator.Query(ctx, query.Request{
		Stream:         main.Stream,
		Segments:       main.Segments,
		LocalFiles:     main.LocalFiles,
		FragmentSQL:    main.FragmentSQL,
		FragmentArgs:   main.FragmentArgs,
		SingleFragment: main.SingleFragment,
		Sources:        built[1:],
		FinalSQL:       sq.finalSQL,
		FinalArgs:      sq.finalArgs,
		ScanBudget:     sq.scanBudget,
	}, scan)
	if errors.Is(err, query.ErrNoData) {
		return false, nil
	}
	return err == nil, err
}

// tiers are the rollups that may answer schema's queries.
func (s statsSchema) tiers() rollupTiers {
	switch s {
	case edgeStats:
		return rollupTiers{minute: spans_core.StreamSpansEdgeRollup}
	case traceStats:
		return rollupTiers{minute: spans_core.StreamSpansTraceRollup, hour: spans_core.StreamSpansTraceHourRollup}
	}
	return rollupTiers{minute: spans_core.StreamSpansRollup, hour: spans_core.StreamSpansHourRollup}
}

// relation is input's rows over ranges as schema's relation, with its
// arguments; it reads query.SourcePlaceholder once.
func (s statsSchema) relation(
	input statsInput, projectID uuid.UUID, ranges []interval, k8s bool,
) (string, []any) {
	if input.rollup {
		predicate, args := rangePredicate("timestamp", ranges)
		columns := "*, " + spans_core.DurationBucketValueSQL("duration_bucket") + " AS duration_ms"
		live := ""
		if s == edgeStats {
			// The no-calls marker row (spans_compactor's edgesQuery).
			columns, live = "*", " AND cnt > 0"
		}
		return "(SELECT " + columns + " FROM " + query.SourcePlaceholder +
			" WHERE project_id = ?" + live + " AND " + predicate + ")", append([]any{projectID.String()}, args...)
	}
	switch s {
	case edgeStats:
		return rawEdgesRelation(projectID, ranges)
	case traceStats:
		return rawTracesRelation(projectID, ranges)
	}
	return rawStatsRelation(projectID, ranges, k8s)
}

// rawStatsRelation is raw spans as spanStats rows.
func rawStatsRelation(projectID uuid.UUID, ranges []interval, k8s bool) (string, []any) {
	predicate, args := rangePredicate("start_time", ranges)
	columns := "start_time AS timestamp, service_name, name, kind, status_code, parent_span_id = '' AS root, " +
		spans_core.DurationBucketSQL(spans_core.SpanDurationMsSQL) + " AS duration_bucket, " +
		spans_core.SpanDurationMsSQL + " AS duration_ms, 1::BIGINT AS cnt, " +
		spans_core.SpanDurationMsSQL + " AS duration_sum"
	if k8s {
		for _, column := range []struct{ name, path string }{
			{"k8s_namespace", spans_core.K8sNamespacePath},
			{"k8s_deployment", spans_core.K8sDeploymentPath},
			{"k8s_node", spans_core.K8sNodePath},
		} {
			extract := "json_extract_string(resource_attributes, '" + column.path + "')"
			columns += ", " + extract + " AS " + column.name +
				", CASE WHEN " + extract + " IS NOT NULL THEN start_time END AS " + column.name + "_at"
		}
	}
	return "(SELECT " + columns + " FROM " + query.SourcePlaceholder + " WHERE project_id = ? AND " + predicate + ")",
		append([]any{projectID.String()}, args...)
}

// rawEdgesRelation is raw spans as edgeStats rows: calls whose child (or
// client span) starts within ranges. Only spans within edgeJoinMargin of
// the ranges are joined: an edge input is a few partial minutes of whole
// segments, and joining all of a cold segment's spans for them took 40 s.
// The joined spans hold no JSON: a client span's peer is named up front.
func rawEdgesRelation(projectID uuid.UUID, ranges []interval) (string, []any) {
	predicate, rangeArgs := rangePredicate("c.start_time", ranges)
	near := make([]interval, len(ranges))
	for i, r := range ranges {
		near[i] = interval{start: r.start.Add(-edgeJoinMargin), end: r.end.Add(edgeJoinMargin), includeEnd: true}
	}
	nearPredicate, nearArgs := rangePredicate("start_time", near)
	client := "kind IN ('CLIENT', 'PRODUCER')"
	sqlText := "(WITH s AS (SELECT trace_id, span_id, parent_span_id, service_name, status_code, start_time, " +
		spans_core.SpanDurationMsSQL + " AS duration_ms, " +
		"CASE WHEN " + client + " THEN " + spans_core.PeerNameSQL + " END AS peer, " +
		"CASE WHEN " + client + " THEN NULLIF(json_extract_string(attributes, '$.db_system'), '') END AS db_system, " +
		"CASE WHEN " + client + " THEN NULLIF(json_extract_string(attributes, '$.messaging_system'), '') END " +
		"AS messaging_system FROM " + query.SourcePlaceholder + " WHERE project_id = ? AND " + nearPredicate + ") " +
		"SELECT c.start_time AS timestamp, p.service_name AS src, c.service_name AS dst, false AS peer, " +
		"'' AS db_system, '' AS messaging_system, CAST(NULL AS SMALLINT) AS duration_bucket, 1::BIGINT AS cnt, " +
		"(c.status_code = 'ERROR')::BIGINT AS err_cnt, c.duration_ms AS duration_sum " +
		"FROM s c JOIN s p ON c.trace_id = p.trace_id AND c.parent_span_id = p.span_id " +
		"WHERE c.parent_span_id != '' AND " + predicate + " " +
		"UNION ALL BY NAME " +
		"SELECT c.start_time AS timestamp, c.service_name AS src, c.peer AS dst, true AS peer, " +
		"coalesce(c.db_system, '') AS db_system, coalesce(c.messaging_system, '') AS messaging_system, " +
		spans_core.DurationBucketSQL("c.duration_ms") + " AS duration_bucket, 1::BIGINT AS cnt, " +
		"(c.status_code = 'ERROR')::BIGINT AS err_cnt, c.duration_ms AS duration_sum " +
		"FROM s c WHERE c.peer IS NOT NULL AND c.peer <> c.service_name AND " + predicate + " " +
		"AND NOT EXISTS (SELECT 1 FROM s child WHERE child.trace_id = c.trace_id AND child.parent_span_id = c.span_id))"
	args := append(append([]any{projectID.String()}, nearArgs...), rangeArgs...)
	return sqlText, append(args, rangeArgs...)
}

// rawTracesRelation is raw spans as traceStats rows: root spans starting
// within ranges, with the spans of their trace up to edgeJoinMargin after
// the ranges (a span starts after its parent).
func rawTracesRelation(projectID uuid.UUID, ranges []interval) (string, []any) {
	rootPredicate, rangeArgs := rangePredicate("start_time", ranges)
	near := make([]interval, len(ranges))
	for i, r := range ranges {
		near[i] = interval{start: r.start, end: r.end.Add(edgeJoinMargin), includeEnd: true}
	}
	nearPredicate, nearArgs := rangePredicate("start_time", near)
	bucket := spans_core.DurationBucketSQL("r.duration_ms")
	sqlText := "(WITH s AS (SELECT trace_id, parent_span_id, service_name, status_code, start_time, " +
		spans_core.SpanDurationMsSQL + " AS duration_ms FROM " + query.SourcePlaceholder +
		" WHERE project_id = ? AND " + nearPredicate + "), " +
		"errs AS (SELECT DISTINCT trace_id FROM s WHERE status_code = 'ERROR'), " +
		"r AS (SELECT trace_id, start_time, duration_ms, " +
		"coalesce(trace_id IN (SELECT trace_id FROM errs), false) AS errors FROM s " +
		"WHERE parent_span_id = '' AND " + rootPredicate + ") " +
		"SELECT r.start_time AS timestamp, NULL::VARCHAR AS service_name, r.errors, " + bucket + " AS duration_bucket, " +
		"r.duration_ms, 1::BIGINT AS cnt FROM r " +
		"UNION ALL BY NAME " +
		"SELECT r.start_time AS timestamp, p.service_name, r.errors, " + bucket + " AS duration_bucket, " +
		"r.duration_ms, 1::BIGINT AS cnt FROM r JOIN (SELECT DISTINCT trace_id, service_name FROM s) p " +
		"ON p.trace_id = r.trace_id)"
	return sqlText, append(append([]any{projectID.String()}, nearArgs...), rangeArgs...)
}

// histogramSQL aggregates relation (one or more rows per keys and
// duration_bucket, counted in countColumn) per keys: the count as cnt,
// extras (aggregate expressions with their aliases), and each quantile as
// p<percent>, NULL without spans. A quantile is the value of the first
// bucket whose running count reaches it.
func histogramSQL(relation, countColumn string, keys, extras []string, quantiles []float64) string {
	partition := ""
	group := ""
	selected := ""
	if len(keys) > 0 {
		partition = "PARTITION BY " + strings.Join(keys, ", ") + " "
		group = " GROUP BY " + strings.Join(keys, ", ")
		selected = strings.Join(keys, ", ") + ", "
	}
	columns := []string{"sum(" + countColumn + ")::BIGINT AS cnt"}
	columns = append(columns, extras...)
	for _, quantile := range quantiles {
		columns = append(columns, fmt.Sprintf(
			"CASE WHEN sum(%[1]s) > 0 THEN %[2]s END AS p%[3]d",
			countColumn,
			spans_core.DurationBucketValueSQL(fmt.Sprintf(
				"min(duration_bucket) FILTER (WHERE running >= %[1]g * total AND %[2]s > 0)", quantile, countColumn)),
			int(quantile*100+0.5),
		))
	}
	return "SELECT " + selected + strings.Join(columns, ", ") + " FROM (SELECT *, " +
		"sum(" + countColumn + ") OVER (" + partition + "ORDER BY duration_bucket " +
		"ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS running, " +
		"sum(" + countColumn + ") OVER (" + strings.TrimSpace(partition) + ") AS total FROM " + relation + ")" + group
}
