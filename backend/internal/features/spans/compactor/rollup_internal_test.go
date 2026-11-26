package spans_compactor

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	spans_core "obzly/internal/features/spans/core"
	spans_ingester "obzly/internal/features/spans/ingester"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// A big segment's edges are joined in parts of whole traces (a 20M-span
// cold segment joined at once ran out of memory and temp disk); the parts
// together hold the same calls and peers as one join.
func Test_EdgesQueries_InParts_MatchOneJoin(t *testing.T) {
	dataDir := t.TempDir()
	record, compactor := seedJoinSegment(t, dataDir)

	whole, err := compactor.compactor.BuildRollupQueries(record, streamSpans, spans_core.StreamSpansEdgeRollup,
		[]string{edgesQuery(1, 0)}, edgesCombine, dataDir, true)
	require.NoError(t, err)
	parts, err := compactor.compactor.BuildRollupQueries(record, streamSpans, spans_core.StreamSpansEdgeRollup,
		[]string{edgesQuery(3, 0), edgesQuery(3, 1), edgesQuery(3, 2)}, edgesCombine, dataDir, true)
	require.NoError(t, err)

	require.Equal(t, edgeRows(t, whole.Path), edgeRows(t, parts.Path))
	require.Equal(t, callRows(t, whole.Path), callRows(t, parts.Path),
		"an edge's rows from several parts are summed into one")
	require.Len(t, edgeRows(t, whole.Path), 4+4+4, "gateway -> svc-N, svc-N -> svc-N, svc-N -> postgresql")
}

// spans_1m_traces is joined in parts like the edges: the parts hold the
// same root counts per minute, service and error flag as one join.
func Test_TracesQueries_InParts_MatchOneJoin(t *testing.T) {
	dataDir := t.TempDir()
	record, compactor := seedJoinSegment(t, dataDir)

	whole, err := compactor.compactor.BuildRollupQueries(record, streamSpans, spans_core.StreamSpansTraceRollup,
		[]string{tracesQuery(1, 0)}, tracesCombine, dataDir, true)
	require.NoError(t, err)
	parts, err := compactor.compactor.BuildRollupQueries(record, streamSpans, spans_core.StreamSpansTraceRollup,
		[]string{tracesQuery(3, 0), tracesQuery(3, 1), tracesQuery(3, 2)}, tracesCombine, dataDir, true)
	require.NoError(t, err)

	require.Equal(t, traceRows(t, whole.Path), traceRows(t, parts.Path))
	require.Equal(t, []string{"<nil> false 26", "<nil> true 14", "gateway false 26", "gateway true 14",
		"svc-0 false 6", "svc-0 true 4", "svc-1 false 7", "svc-1 true 3", "svc-2 false 7", "svc-2 true 3",
		"svc-3 false 6", "svc-3 true 4"}, traceRows(t, whole.Path))
}

// callRows counts a spans_1m_edges file's rows, markers aside.
func callRows(t *testing.T, path string) int64 {
	t.Helper()
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	var rows int64
	require.NoError(t, db.QueryRow("SELECT count(*) FROM read_parquet(?) WHERE cnt > 0", path).Scan(&rows))
	return rows
}

// edgeRows are a spans_1m_edges file's calls (no markers), summed per edge.
func edgeRows(t *testing.T, path string) []string {
	t.Helper()
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	rows, err := db.Query(`SELECT src, dst, peer, db_system, sum(cnt), sum(err_cnt), round(sum(duration_sum), 6)
		FROM read_parquet(?) WHERE cnt > 0 GROUP BY ALL ORDER BY ALL`, path)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var result []string
	for rows.Next() {
		var src, dst, dbSystem string
		var peer bool
		var cnt, errs int64
		var duration float64
		require.NoError(t, rows.Scan(&src, &dst, &peer, &dbSystem, &cnt, &errs, &duration))
		result = append(result, fmt.Sprint(src, dst, peer, dbSystem, cnt, errs, duration))
	}
	require.NoError(t, rows.Err())
	return result
}

// traceRows are a spans_1m_traces file's root counts per service and error
// flag.
func traceRows(t *testing.T, path string) []string {
	t.Helper()
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	rows, err := db.Query(`SELECT service_name, errors, sum(cnt) FROM read_parquet(?) WHERE cnt > 0
		GROUP BY ALL ORDER BY ALL NULLS FIRST`, path)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var result []string
	for rows.Next() {
		var service sql.NullString
		var errors bool
		var cnt int64
		require.NoError(t, rows.Scan(&service, &errors, &cnt))
		name := "<nil>"
		if service.Valid {
			name = service.String
		}
		result = append(result, fmt.Sprint(name, " ", errors, " ", cnt))
	}
	require.NoError(t, rows.Err())
	return result
}

// seedJoinSegment writes one segment of 40 traces, gateway -> svc-<n%4>
// calling Postgres, every third svc span failing, and a compactor for it.
func seedJoinSegment(t *testing.T, dataDir string) (*segments.SegmentRecord, *SpanCompactor) {
	t.Helper()
	projectID := uuid.New()
	ingester, err := spans_ingester.NewSpanIngester(dataDir, segments.WithMaxRows(1_000_000))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ingester.Close() })
	base := time.Now().UTC().Add(-time.Hour)
	var spanList []*spans_core.Span
	for trace := range 40 {
		start := base.Add(time.Duration(trace) * 7 * time.Second)
		traceID := fmt.Sprintf("trace-%02d", trace)
		status := spans_core.StatusCodeOK
		if trace%3 == 0 {
			status = spans_core.StatusCodeError
		}
		spanList = append(spanList,
			&spans_core.Span{TraceID: traceID, SpanID: "root", Name: "GET /", ServiceName: "gateway",
				Kind: spans_core.SpanKindServer, StartTime: start, EndTime: start.Add(90 * time.Millisecond)},
			&spans_core.Span{TraceID: traceID, SpanID: "a", ParentSpanID: "root", Name: "GET /a",
				ServiceName: fmt.Sprintf("svc-%d", trace%4), Kind: spans_core.SpanKindServer,
				StartTime: start.Add(time.Millisecond), EndTime: start.Add(40 * time.Millisecond), StatusCode: status},
			&spans_core.Span{TraceID: traceID, SpanID: "db", ParentSpanID: "a", Name: "SELECT",
				ServiceName: fmt.Sprintf("svc-%d", trace%4), Kind: spans_core.SpanKindClient,
				StartTime: start.Add(2 * time.Millisecond), EndTime: start.Add(time.Duration(3+trace) * time.Millisecond),
				Attributes: map[string]any{"db.system": "postgresql"}})
	}
	for _, span := range spanList {
		span.ID, span.ProjectID = uuid.New(), projectID
		if span.StatusCode == "" {
			span.StatusCode = spans_core.StatusCodeOK
		}
	}
	require.NoError(t, ingester.StoreSpansBatch(map[uuid.UUID][]*spans_core.Span{projectID: spanList}))
	_, err = ingester.FlushAll()
	require.NoError(t, err)
	records, err := (&segments.SegmentRepository{}).ListByProject(projectID, spans_core.StreamSpans)
	require.NoError(t, err)
	require.Len(t, records, 1)
	compactor, err := NewSpanCompactor(dataDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = compactor.Close() })

	return records[0], compactor
}
