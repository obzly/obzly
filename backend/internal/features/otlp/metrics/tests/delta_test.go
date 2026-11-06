package otlp_metrics_tests

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"testing"
	"time"

	metrics_core "obzly/internal/features/metrics/core"
	otlp_metrics "obzly/internal/features/otlp/metrics"
	"obzly/internal/ingest"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	collectormetricsv1 "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"
)

const (
	delta      = metricsv1.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA
	cumulative = metricsv1.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE
)

// failingDeltaTotals behaves like an unreachable Valkey and records whether
// it was asked at all.
type failingDeltaTotals struct {
	calls int
}

func (f *failingDeltaTotals) Add(context.Context, []otlp_metrics.DeltaIncrement) ([]float64, error) {
	f.calls++
	return nil, errors.New("valkey: connection refused")
}

func Test_OtlpHttpExport_DeltaSumInTwoRequests_StoresRunningTotals(t *testing.T) {
	fixture := SetUpFixture("OTLP Metrics HTTP Delta Sum")
	now := time.Now().UTC()

	for i, value := range []float64{3, 5} {
		request := BuildSumExportRequest("delta_tokens", delta, now.Add(time.Duration(i-10)*time.Second), value)
		recorder := postOtlpMetrics(t, fixture, fixture.ApiKey.Token, request)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	}

	assert.Equal(t, []float64{3, 8}, storedValues(t, fixture, "delta_tokens", ""))
}

func Test_OtlpHttpExport_CumulativeOnly_NeverCallsDeltaTotals(t *testing.T) {
	fixture := SetUpFixture("OTLP Metrics HTTP Cumulative")
	totals := &failingDeltaTotals{}
	t.Cleanup(otlp_metrics.SetDeltaTotalsForTest(totals))
	now := time.Now().UTC()

	recorder := postOtlpMetrics(t, fixture, fixture.ApiKey.Token,
		BuildSumExportRequest("cumulative_requests", cumulative, now, 42))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	recorder = postOtlpMetrics(t, fixture, fixture.ApiKey.Token,
		BuildHistogramExportRequest("cumulative_latency", cumulative, now, 2, 20, []uint64{1, 1, 0}))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	assert.Zero(t, totals.calls)
	assert.Equal(t, []float64{42}, storedValues(t, fixture, "cumulative_requests", ""))
}

func Test_OtlpHttpExport_WhenValkeyFailsWithDeltaPoints_Returns503AndStoresNothing(t *testing.T) {
	fixture := SetUpFixture("OTLP Metrics HTTP Delta Valkey Down")
	t.Cleanup(otlp_metrics.SetDeltaTotalsForTest(&failingDeltaTotals{}))
	now := time.Now().UTC()
	request := BuildSumExportRequest("refused_tokens", delta, now, 3)
	// The cumulative half of a refused request isn't stored either: the
	// sender retries the whole request.
	request.ResourceMetrics[0].ScopeMetrics[0].Metrics = append(request.ResourceMetrics[0].ScopeMetrics[0].Metrics,
		BuildSumExportRequest("refused_requests", cumulative, now, 1).ResourceMetrics[0].ScopeMetrics[0].Metrics...)

	recorder := postOtlpMetrics(t, fixture, fixture.ApiKey.Token, request)

	require.Equal(t, http.StatusServiceUnavailable, recorder.Code, recorder.Body.String())
	assert.Equal(t, strconv.Itoa(ingest.RetryAfterSeconds), recorder.Header().Get("Retry-After"))
	assert.Empty(t, storedValues(t, fixture, "refused_tokens", ""))
	assert.Empty(t, storedValues(t, fixture, "refused_requests", ""))
}

func Test_OtlpHttpExport_NaNDeltaPoint_RejectedAsPartialSuccess(t *testing.T) {
	fixture := SetUpFixture("OTLP Metrics HTTP Delta NaN")

	recorder := postOtlpMetrics(t, fixture, fixture.ApiKey.Token,
		BuildSumExportRequest("nan_tokens", delta, time.Now().UTC(), math.NaN()))

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var response collectormetricsv1.ExportMetricsServiceResponse
	require.NoError(t, proto.Unmarshal(recorder.Body.Bytes(), &response))
	require.NotNil(t, response.PartialSuccess)
	assert.EqualValues(t, 1, response.PartialSuccess.RejectedDataPoints)
	assert.Empty(t, storedValues(t, fixture, "nan_tokens", ""))
}

func Test_OtlpGrpcExport_DeltaHistogramInTwoRequests_AccumulatesBuckets(t *testing.T) {
	fixture := SetUpFixture("OTLP Metrics gRPC Delta Histogram")
	client := dialOtlpGrpc(t)
	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-api-key", fixture.ApiKey.Token)
	now := time.Now().UTC()

	_, err := client.Export(ctx,
		BuildHistogramExportRequest("delta_latency", delta, now.Add(-2*time.Second), 3, 30, []uint64{1, 1, 1}))
	require.NoError(t, err)
	_, err = client.Export(ctx,
		BuildHistogramExportRequest("delta_latency", delta, now.Add(-time.Second), 4, 70, []uint64{0, 2, 2}))
	require.NoError(t, err)

	assert.Equal(t, []float64{30, 100}, storedValues(t, fixture, "delta_latency_sum", ""))
	assert.Equal(t, []float64{3, 7}, storedValues(t, fixture, "delta_latency_count", ""))
	assert.Equal(t, []float64{1, 1}, storedValues(t, fixture, "delta_latency_bucket", "10"))
	assert.Equal(t, []float64{2, 4}, storedValues(t, fixture, "delta_latency_bucket", "50"))
	assert.Equal(t, []float64{3, 7}, storedValues(t, fixture, "delta_latency_bucket", "+Inf"))
}

func Test_OtlpGrpcExport_WhenValkeyFailsWithDeltaPoints_Unavailable(t *testing.T) {
	fixture := SetUpFixture("OTLP Metrics gRPC Delta Valkey Down")
	t.Cleanup(otlp_metrics.SetDeltaTotalsForTest(&failingDeltaTotals{}))
	client := dialOtlpGrpc(t)
	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-api-key", fixture.ApiKey.Token)

	_, err := client.Export(ctx, BuildSumExportRequest("grpc_refused_tokens", delta, time.Now().UTC(), 3))

	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Unavailable, st.Code())
	assert.Empty(t, storedValues(t, fixture, "grpc_refused_tokens", ""))
}

// storedValues returns the stored values of one series, oldest first; le
// selects a histogram bucket.
func storedValues(t *testing.T, fixture *TestFixture, name, le string) []float64 {
	t.Helper()
	require.NoError(t, metrics_core.GetMetricCoreRepository().ForceFlush())
	matchers := []metrics_core.MatcherDTO{{Name: "__name__", Type: metrics_core.MatchEqual, Value: name}}
	if le != "" {
		matchers = append(matchers, metrics_core.MatcherDTO{Name: "le", Type: metrics_core.MatchEqual, Value: le})
	}
	from, to := time.Now().UTC().Add(-time.Hour), time.Now().UTC().Add(time.Hour)
	response, err := metrics_core.GetMetricCoreRepository().
		SelectSeries(context.Background(), uuid.MustParse(fixture.ProjectID), &metrics_core.SelectSeriesRequestDTO{
			Matchers:  matchers,
			TimeRange: &metrics_core.TimeRangeDTO{From: &from, To: &to},
		})
	require.NoError(t, err)
	var result []float64
	for _, series := range response.Series {
		for _, sample := range series.Samples {
			result = append(result, sample.Value)
		}
	}
	return result
}
