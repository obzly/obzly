package otlp_metrics_tests

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"obzly/internal/broker"
	metrics_core "obzly/internal/features/metrics/core"
	metrics_jetstream "obzly/internal/features/metrics/jetstream"
	metrics_parquetstore "obzly/internal/features/metrics/parquetstore"
	"obzly/internal/ingest"
	"obzly/internal/ingest/ingesttest"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func Test_OtlpHttpExport_WithJetStream_SamplesAreStoredOnlyAfterAWorkerWritesThem(t *testing.T) {
	test_utils.UseIsolatedSegmentWriter(t)
	client := ingesttest.StartBroker(t, 64<<20)
	fixture := SetUpFixture("OTLP Metrics JetStream")
	projectID := uuid.MustParse(fixture.ProjectID)

	recorder := postOtlpMetrics(t, fixture, fixture.ApiKey.Token, BuildGaugeExportRequest("jetstream_gauge"))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	require.EqualValues(t, 1, ingesttest.StreamMessages(t, client, broker.SignalMetrics))
	require.Zero(t, storedSamples(t, projectID, "jetstream_gauge"), "nothing is written before a worker consumes it")

	writer, err := metrics_core.GetMetricCoreRepository().(*metrics_parquetstore.MetricParquetStore).NewDurableWriter()
	require.NoError(t, err)
	ingesttest.StartConsumer(t, client, broker.SignalMetrics,
		metrics_jetstream.NewSink(writer, writer.Series(), 64<<20, ingesttest.MaxAge, ingesttest.Logger()))

	require.Eventually(t, func() bool {
		return storedSamples(t, projectID, "jetstream_gauge") == 1 &&
			ingesttest.StreamMessages(t, client, broker.SignalMetrics) == 0
	}, 20*time.Second, 100*time.Millisecond, "the worker writes the sample, then acks the batch")
}

func Test_OtlpHttpExport_WhenIngestBufferFull_Returns429WithRetryAfter(t *testing.T) {
	ingesttest.StartBroker(t, 2048)
	fixture := SetUpFixture("OTLP Metrics JetStream full")

	code, retryAfter := http.StatusOK, ""
	for i := 0; i < 20 && code == http.StatusOK; i++ {
		recorder := postOtlpMetrics(t, fixture, fixture.ApiKey.Token, BuildGaugeExportRequest("fill"))
		code, retryAfter = recorder.Code, recorder.Header().Get("Retry-After")
	}

	require.Equal(t, http.StatusTooManyRequests, code)
	require.Equal(t, strconv.Itoa(ingest.RetryAfterSeconds), retryAfter)
}

func storedSamples(t *testing.T, projectID uuid.UUID, name string) int {
	t.Helper()
	from, to := time.Now().UTC().Add(-time.Hour), time.Now().UTC().Add(time.Hour)
	response, err := metrics_core.GetMetricCoreRepository().
		SelectSeries(context.Background(), projectID, &metrics_core.SelectSeriesRequestDTO{
			Matchers:  []metrics_core.MatcherDTO{{Name: "__name__", Type: metrics_core.MatchEqual, Value: name}},
			TimeRange: &metrics_core.TimeRangeDTO{From: &from, To: &to},
		})
	require.NoError(t, err)
	total := 0
	for _, series := range response.Series {
		total += len(series.Samples)
	}
	return total
}
