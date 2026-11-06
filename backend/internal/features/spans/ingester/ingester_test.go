package spans_ingester_test

import (
	"database/sql"
	"testing"
	"time"

	spans_core "obzly/internal/features/spans/core"
	spans_ingester "obzly/internal/features/spans/ingester"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	_ "github.com/marcboeker/go-duckdb/v2"
	"github.com/stretchr/testify/require"
)

func newTestIngester(t *testing.T) *spans_ingester.SpanIngester {
	t.Helper()

	ingester, err := spans_ingester.NewSpanIngester(t.TempDir(), segments.WithMaxRows(1_000_000))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ingester.Close() })

	return ingester
}

func Test_StoreSpansBatch_ThenFlush_WritesReadableParquetSegment(t *testing.T) {
	ingester := newTestIngester(t)
	projectID := uuid.New()
	now := time.Now().UTC().Truncate(time.Millisecond)

	batch := map[uuid.UUID][]*spans_core.Span{
		projectID: {
			{
				ID:           uuid.New(),
				ProjectID:    projectID,
				TraceID:      "0102030405060708090a0b0c0d0e0f10",
				SpanID:       "aabbccddeeff1122",
				ParentSpanID: "",
				Name:         "GET /checkout",
				ServiceName:  "gateway",
				Kind:         spans_core.SpanKindServer,
				StartTime:    now,
				EndTime:      now.Add(100 * time.Millisecond),
				StatusCode:   spans_core.StatusCodeOK,
				Attributes:   map[string]any{"http.method": "GET"},
			},
		},
	}

	require.NoError(t, ingester.StoreSpansBatch(batch))
	flushed, err := ingester.FlushAll()
	require.NoError(t, err)
	require.Len(t, flushed, 1)
	require.Equal(t, "spans", flushed[0].Stream)
	require.Equal(t, 1, flushed[0].RowCount)

	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	var traceID, spanID, name, statusCode string
	var startTime time.Time
	err = db.QueryRow(
		"SELECT trace_id, span_id, name, status_code, start_time FROM read_parquet(?)",
		flushed[0].Path,
	).Scan(&traceID, &spanID, &name, &statusCode, &startTime)
	require.NoError(t, err)

	require.Equal(t, "0102030405060708090a0b0c0d0e0f10", traceID)
	require.Equal(t, "aabbccddeeff1122", spanID)
	require.Equal(t, "GET /checkout", name)
	require.Equal(t, "OK", statusCode)
	require.True(t, now.Equal(startTime), "start_time must round-trip exactly through TIMESTAMP_NS")
}

func Test_StoreSpansBatch_WithoutFlushThreshold_DoesNotFlush(t *testing.T) {
	ingester := newTestIngester(t)
	projectID := uuid.New()

	span := &spans_core.Span{
		ID: uuid.New(), ProjectID: projectID, TraceID: "t", SpanID: "s",
		Name: "op", ServiceName: "svc", StartTime: time.Now().UTC(),
		EndTime: time.Now().UTC(), StatusCode: spans_core.StatusCodeOK,
	}

	require.NoError(t, ingester.StoreSpansBatch(map[uuid.UUID][]*spans_core.Span{projectID: {span}}))

	flushed, err := ingester.FlushStale()
	require.NoError(t, err)
	require.Empty(t, flushed, "a single span shouldn't cross the 1M-row test threshold")
}
