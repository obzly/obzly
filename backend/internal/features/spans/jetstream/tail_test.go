package spans_jetstream

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"obzly/internal/broker"
	spans_core "obzly/internal/features/spans/core"
	spans_parquetstore "obzly/internal/features/spans/parquetstore"
	"obzly/internal/ingest"
	"obzly/internal/ingest/ingesttest"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const tailTestTraceID = "0102030405060708090a0b0c0d0e0f10"

// Spans a worker hasn't written yet are in their trace, and once it has
// they are counted once.
func Test_Tail_PendingSpansAreQueryable_AndCountedOnceAfterAWorkerWritesThem(t *testing.T) {
	test_utils.UseIsolatedSegmentWriter(t)
	client := ingesttest.StartBroker(t, 64<<20)
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tail, err := NewTail(filepath.Join(dir, "tail"), logger)
	require.NoError(t, err)
	store, err := spans_parquetstore.New(dir, spans_parquetstore.WithTail(tail))
	require.NoError(t, err)
	projectID := uuid.New()
	now := time.Now().UTC()
	var spans []*spans_core.Span
	for i := range 3 {
		spans = append(spans, &spans_core.Span{ID: uuid.New(), ProjectID: projectID, TraceID: tailTestTraceID,
			SpanID: uuid.NewString()[:16], Name: "op", ServiceName: "svc",
			StartTime: now.Add(time.Duration(i) * time.Millisecond), EndTime: now.Add(time.Second)})
	}
	require.NoError(t, ingest.Publish(client, broker.SignalSpans, projectID, spans))

	require.Eventually(t, func() bool { return traceSpans(t, store, projectID) == 3 },
		15*time.Second, 100*time.Millisecond, "pending spans are in the trace")

	writer, err := store.NewDurableWriter()
	require.NoError(t, err)
	ingesttest.StartConsumer(t, client, broker.SignalSpans, NewSink(writer, 64<<20, ingesttest.MaxAge, logger))
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		require.LessOrEqual(t, traceSpans(t, store, projectID), 3, "written spans are never counted twice")
		time.Sleep(50 * time.Millisecond)
	}
	require.Zero(t, ingesttest.StreamMessages(t, client, broker.SignalSpans))
	require.Equal(t, 3, traceSpans(t, store, projectID))
}

func traceSpans(t *testing.T, store *spans_parquetstore.SpanParquetStore, projectID uuid.UUID) int {
	t.Helper()
	from, to := time.Now().UTC().Add(-time.Hour), time.Now().UTC().Add(time.Hour)
	trace, err := store.GetTrace(projectID, tailTestTraceID, &spans_core.TimeRangeDTO{From: &from, To: &to})
	if err != nil || trace == nil {
		return 0
	}
	return len(trace.Spans)
}
