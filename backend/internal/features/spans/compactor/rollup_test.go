package spans_compactor_test

import (
	"database/sql"
	"testing"
	"time"

	spans_compactor "obzly/internal/features/spans/compactor"
	spans_core "obzly/internal/features/spans/core"
	spans_ingester "obzly/internal/features/spans/ingester"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	_ "github.com/marcboeker/go-duckdb/v2"
	"github.com/stretchr/testify/require"
)

func Test_RollupSegments_CompactedSegment_GetsStatsHourAndEdgeRollups(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()
	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Minute)
	raw := seedSpanSegment(t, dataDir, projectID, callTrace("t1", base))
	compactor := newRollupCompactor(t, dataDir)
	require.NoError(t, (&segments.SegmentRepository{}).SetLevel([]uuid.UUID{raw.ID}, segments.LevelCompacted))

	require.NoError(t, compactor.RollupSegments(projectID))

	for _, stream := range spans_core.SpanRollupStreams {
		rollups := listStream(t, projectID, stream)
		require.Len(t, rollups, 1, stream)
		require.Equal(t, raw.ID.String(), rollups[0].Stats.Source, stream)
	}
	stats := listStream(t, projectID, spans_core.StreamSpansRollup)[0]
	require.Equal(t, int64(3), queryInt(t, "SELECT sum(cnt) FROM read_parquet(?)", stats.Path))
	require.Equal(t, int64(1), queryInt(t,
		"SELECT sum(cnt) FROM read_parquet(?) WHERE root AND service_name = 'gateway'", stats.Path))
	require.Equal(t, int64(1), queryInt(t,
		"SELECT count(*) FROM read_parquet(?) WHERE k8s_namespace = 'shop'", stats.Path))

	traces := listStream(t, projectID, spans_core.StreamSpansTraceRollup)[0]
	require.Equal(t, int64(1), queryInt(t,
		"SELECT sum(cnt) FROM read_parquet(?) WHERE service_name IS NULL AND errors", traces.Path),
		"one root, its trace failed below it")
	require.Equal(t, int64(2), queryInt(t,
		"SELECT sum(cnt) FROM read_parquet(?) WHERE service_name IN ('gateway', 'payments')", traces.Path))
	traceHours := listStream(t, projectID, spans_core.StreamSpansTraceHourRollup)[0]
	require.Equal(t, int64(1), queryInt(t,
		"SELECT sum(cnt) FROM read_parquet(?) WHERE service_name IS NULL AND errors", traceHours.Path))
	require.Equal(t, int64(1), queryInt(t,
		"SELECT count(DISTINCT timestamp) FROM read_parquet(?) WHERE cnt > 0", traceHours.Path), "one hour")

	edges := listStream(t, projectID, spans_core.StreamSpansEdgeRollup)[0]
	require.Equal(t, int64(1), queryInt(t,
		"SELECT sum(cnt) FROM read_parquet(?) WHERE NOT peer AND src = 'gateway' AND dst = 'payments'", edges.Path))
	require.Equal(t, int64(1), queryInt(t,
		"SELECT sum(err_cnt) FROM read_parquet(?) WHERE peer AND src = 'payments' AND dst = 'postgresql' "+
			"AND db_system = 'postgresql'", edges.Path))
}

func Test_RollupSegments_FreshSegment_IsReadRawNotRolledUp(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()
	seedSpanSegment(t, dataDir, projectID, callTrace("t1", time.Now().UTC()))
	compactor := newRollupCompactor(t, dataDir)

	require.NoError(t, compactor.RollupSegments(projectID))

	for _, stream := range spans_core.SpanRollupStreams {
		require.Empty(t, listStream(t, projectID, stream), stream)
	}
}

// A segment with no calls still gets an edges rollup (one row with no
// calls), so the next pass doesn't build it again: rebuilding took a slot
// of every pass's budget, and enough such segments stalled a backfill.
func Test_RollupSegments_SegmentWithoutCalls_IsNotRebuiltNextPass(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()
	start := time.Now().UTC().Add(-time.Hour)
	raw := seedSpanSegment(t, dataDir, projectID, []*spans_core.Span{{
		TraceID: "lone", SpanID: "s1", Name: "GET /health", ServiceName: "gateway", Kind: spans_core.SpanKindServer,
		StartTime: start, EndTime: start.Add(time.Millisecond), StatusCode: spans_core.StatusCodeOK,
	}})
	compactor := newRollupCompactor(t, dataDir)
	require.NoError(t, (&segments.SegmentRepository{}).SetLevel([]uuid.UUID{raw.ID}, segments.LevelCompacted))

	require.NoError(t, compactor.RollupSegments(projectID))
	first := listStream(t, projectID, spans_core.StreamSpansEdgeRollup)
	require.Len(t, first, 1)
	require.Equal(t, int64(0), queryInt(t, "SELECT sum(cnt) FROM read_parquet(?)", first[0].Path))

	require.NoError(t, compactor.RollupSegments(projectID))
	second := listStream(t, projectID, spans_core.StreamSpansEdgeRollup)
	require.Len(t, second, 1)
	require.Equal(t, first[0].ID, second[0].ID)
}

func Test_RollupSegments_SourceGone_DeletesItsRollups(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()
	raw := seedSpanSegment(t, dataDir, projectID, callTrace("t1", time.Now().UTC().Add(-3*time.Hour)))
	compactor := newRollupCompactor(t, dataDir)
	repo := &segments.SegmentRepository{}
	require.NoError(t, repo.SetLevel([]uuid.UUID{raw.ID}, segments.LevelCompacted))
	require.NoError(t, compactor.RollupSegments(projectID))
	require.NotEmpty(t, listStream(t, projectID, spans_core.StreamSpansRollup))

	require.NoError(t, repo.DeleteByIDs([]uuid.UUID{raw.ID}))
	require.NoError(t, compactor.RollupSegments(projectID))

	for _, stream := range spans_core.SpanRollupStreams {
		require.Empty(t, listStream(t, projectID, stream), stream)
	}
}

func Test_DeleteAllForProject_RemovesSpanRollups(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()
	raw := seedSpanSegment(t, dataDir, projectID, callTrace("t1", time.Now().UTC().Add(-3*time.Hour)))
	compactor := newRollupCompactor(t, dataDir)
	require.NoError(t, (&segments.SegmentRepository{}).SetLevel([]uuid.UUID{raw.ID}, segments.LevelCompacted))
	require.NoError(t, compactor.RollupSegments(projectID))

	_, err := compactor.DeleteAllForProject(projectID)
	require.NoError(t, err)

	for _, stream := range spans_core.SpanRollupStreams {
		require.Empty(t, listStream(t, projectID, stream), stream)
	}
}

// callTrace is gateway -> payments, payments calling Postgres (a client
// span with no child: an untraced peer) and failing.
func callTrace(traceID string, start time.Time) []*spans_core.Span {
	return []*spans_core.Span{
		{TraceID: traceID, SpanID: "root", Name: "GET /pay", ServiceName: "gateway", Kind: spans_core.SpanKindServer,
			StartTime: start, EndTime: start.Add(80 * time.Millisecond), StatusCode: spans_core.StatusCodeOK,
			ResourceAttributes: map[string]any{"k8s.namespace.name": "shop"}},
		{TraceID: traceID, SpanID: "pay", ParentSpanID: "root", Name: "POST /charge", ServiceName: "payments",
			Kind: spans_core.SpanKindServer, StartTime: start.Add(5 * time.Millisecond),
			EndTime: start.Add(60 * time.Millisecond), StatusCode: spans_core.StatusCodeOK},
		{TraceID: traceID, SpanID: "db", ParentSpanID: "pay", Name: "INSERT charges", ServiceName: "payments",
			Kind: spans_core.SpanKindClient, StartTime: start.Add(10 * time.Millisecond),
			EndTime: start.Add(50 * time.Millisecond), StatusCode: spans_core.StatusCodeError,
			Attributes: map[string]any{"db.system": "postgresql"}},
	}
}

// seedSpanSegment writes spanList as one segment and returns its record.
func seedSpanSegment(t *testing.T, dataDir string, projectID uuid.UUID, spanList []*spans_core.Span) *segments.SegmentRecord {
	t.Helper()
	ingester, err := spans_ingester.NewSpanIngester(dataDir, segments.WithMaxRows(1_000_000))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ingester.Close() })
	for _, span := range spanList {
		span.ID, span.ProjectID = uuid.New(), projectID
	}
	require.NoError(t, ingester.StoreSpansBatch(map[uuid.UUID][]*spans_core.Span{projectID: spanList}))
	_, err = ingester.FlushAll()
	require.NoError(t, err)
	records := listStream(t, projectID, spans_core.StreamSpans)
	require.NotEmpty(t, records)
	return records[len(records)-1]
}

func newRollupCompactor(t *testing.T, dataDir string) *spans_compactor.SpanCompactor {
	t.Helper()
	compactor, err := spans_compactor.NewSpanCompactor(dataDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = compactor.Close() })
	return compactor
}

func listStream(t *testing.T, projectID uuid.UUID, stream string) []*segments.SegmentRecord {
	t.Helper()
	records, err := (&segments.SegmentRepository{}).ListByProject(projectID, stream)
	require.NoError(t, err)
	return records
}

func queryInt(t *testing.T, sqlText string, args ...any) int64 {
	t.Helper()
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	var value sql.NullInt64
	require.NoError(t, db.QueryRow(sqlText, args...).Scan(&value))
	return value.Int64
}
