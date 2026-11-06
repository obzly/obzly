package otlp_traces_tests

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"obzly/internal/broker"
	spans_core "obzly/internal/features/spans/core"
	spans_jetstream "obzly/internal/features/spans/jetstream"
	spans_parquetstore "obzly/internal/features/spans/parquetstore"
	"obzly/internal/ingest"
	"obzly/internal/ingest/ingesttest"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func Test_OtlpHttpExport_WithJetStream_SpansAreStoredOnlyAfterAWorkerWritesThem(t *testing.T) {
	test_utils.UseIsolatedSegmentWriter(t)
	client := ingesttest.StartBroker(t, 64<<20)
	fixture := SetUpFixture("OTLP Traces JetStream")
	projectID := uuid.MustParse(fixture.ProjectID)

	recorder := postOtlpTraces(t, fixture, fixture.ApiKey.Token, BuildExportRequest("through jetstream"))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	require.EqualValues(t, 1, ingesttest.StreamMessages(t, client, broker.SignalSpans))
	require.Zero(t, storedSpans(t, projectID), "nothing is written before a worker consumes it")

	writer, err := spans_core.GetSpanCoreRepository().(*spans_parquetstore.SpanParquetStore).NewDurableWriter()
	require.NoError(t, err)
	ingesttest.StartConsumer(t, client, broker.SignalSpans,
		spans_jetstream.NewSink(writer, 64<<20, ingesttest.MaxAge, ingesttest.Logger()))

	require.Eventually(t, func() bool {
		return storedSpans(t, projectID) == 1 && ingesttest.StreamMessages(t, client, broker.SignalSpans) == 0
	}, 20*time.Second, 100*time.Millisecond, "the worker writes the span, then acks the batch")
}

func Test_OtlpHttpExport_WhenIngestBufferFull_Returns429WithRetryAfter(t *testing.T) {
	ingesttest.StartBroker(t, 2048)
	fixture := SetUpFixture("OTLP Traces JetStream full")

	code, retryAfter := http.StatusOK, ""
	for i := 0; i < 20 && code == http.StatusOK; i++ {
		recorder := postOtlpTraces(t, fixture, fixture.ApiKey.Token, BuildExportRequest("fill"))
		code, retryAfter = recorder.Code, recorder.Header().Get("Retry-After")
	}

	require.Equal(t, http.StatusTooManyRequests, code)
	require.Equal(t, strconv.Itoa(ingest.RetryAfterSeconds), retryAfter)
}

func storedSpans(t *testing.T, projectID uuid.UUID) int {
	t.Helper()
	from, to := time.Now().UTC().Add(-time.Hour), time.Now().UTC().Add(time.Hour)
	trace, err := spans_core.GetSpanCoreRepository().GetTrace(
		projectID, "0102030405060708090a0b0c0d0e0f10", &spans_core.TimeRangeDTO{From: &from, To: &to},
	)
	if err != nil || trace == nil {
		return 0
	}
	return len(trace.Spans)
}
