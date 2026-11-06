package otlp_metrics_tests

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	metrics_core "obzly/internal/features/metrics/core"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	collectormetricsv1 "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
)

func postOtlpMetrics(
	t *testing.T,
	fixture *TestFixture,
	apiKey string,
	request *collectormetricsv1.ExportMetricsServiceRequest,
) *httptest.ResponseRecorder {
	body, err := proto.Marshal(request)
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodPost, "/v1/metrics", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-protobuf")
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}

	recorder := httptest.NewRecorder()
	fixture.Router.ServeHTTP(recorder, req)
	return recorder
}

func Test_OtlpHttpExport_WithValidApiKeyAndGauge_Accepted(t *testing.T) {
	fixture := SetUpFixture("OTLP Metrics HTTP Valid")
	request := BuildGaugeExportRequest("cpu_temperature")

	recorder := postOtlpMetrics(t, fixture, fixture.ApiKey.Token, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	var response collectormetricsv1.ExportMetricsServiceResponse
	require.NoError(t, proto.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Nil(t, response.PartialSuccess)

	// Check it landed in storage, not just that HTTP accepted it.
	require.NoError(t, metrics_core.GetMetricCoreRepository().ForceFlush())
	projectID, err := uuid.Parse(fixture.ProjectID)
	require.NoError(t, err)

	from := time.Now().UTC().Add(-time.Hour)
	to := time.Now().UTC().Add(time.Hour)
	response2, err := metrics_core.GetMetricCoreRepository().
		SelectSeries(context.Background(), projectID, &metrics_core.SelectSeriesRequestDTO{
			Matchers: []metrics_core.MatcherDTO{
				{Name: "__name__", Type: metrics_core.MatchEqual, Value: "cpu_temperature"},
			},
			TimeRange: &metrics_core.TimeRangeDTO{From: &from, To: &to},
		})
	require.NoError(t, err)
	require.Len(t, response2.Series, 1)
	assert.Equal(t, "us-east", response2.Series[0].Labels["region"])
	assert.Equal(t, "test-service", response2.Series[0].Labels["service_name"])
	require.Len(t, response2.Series[0].Samples, 1)
	assert.InDelta(t, 42.5, response2.Series[0].Samples[0].Value, 0.001)
}

func Test_OtlpHttpExport_WithoutApiKey_Unauthorized(t *testing.T) {
	fixture := SetUpFixture("OTLP Metrics HTTP No Key")
	request := BuildGaugeExportRequest("should_be_rejected")

	recorder := postOtlpMetrics(t, fixture, "", request)
	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
}

func Test_OtlpHttpExport_WithInvalidApiKey_Unauthorized(t *testing.T) {
	fixture := SetUpFixture("OTLP Metrics HTTP Bad Key")
	request := BuildGaugeExportRequest("should_be_rejected")

	recorder := postOtlpMetrics(t, fixture, "lb_not-a-real-key", request)
	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
}

func Test_OtlpHttpExport_WithEmptyResourceMetrics_AcceptedWithNoRecords(t *testing.T) {
	fixture := SetUpFixture("OTLP Metrics HTTP Empty")

	recorder := postOtlpMetrics(t, fixture, fixture.ApiKey.Token, &collectormetricsv1.ExportMetricsServiceRequest{})
	assert.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
}

// The Collector's otlphttp exporter gzips by default.
func Test_OtlpHttpExport_GzipEncoded_Accepted(t *testing.T) {
	fixture := SetUpFixture("OTLP Metrics HTTP Gzip")
	request := BuildGaugeExportRequest("gzip_metric")

	payload, err := proto.Marshal(request)
	require.NoError(t, err)

	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	_, err = gzipWriter.Write(payload)
	require.NoError(t, err)
	require.NoError(t, gzipWriter.Close())

	req, err := http.NewRequest(http.MethodPost, "/v1/metrics", bytes.NewReader(compressed.Bytes()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("X-API-Key", fixture.ApiKey.Token)

	recorder := httptest.NewRecorder()
	fixture.Router.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	var response collectormetricsv1.ExportMetricsServiceResponse
	require.NoError(t, proto.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Nil(t, response.PartialSuccess)
}
