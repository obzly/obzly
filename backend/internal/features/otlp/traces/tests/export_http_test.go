package otlp_traces_tests

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	spans_core "obzly/internal/features/spans/core"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	collectortracev1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
)

func postOtlpTraces(
	t *testing.T,
	fixture *TestFixture,
	apiKey string,
	request *collectortracev1.ExportTraceServiceRequest,
) *httptest.ResponseRecorder {
	body, err := proto.Marshal(request)
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-protobuf")
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}

	recorder := httptest.NewRecorder()
	fixture.Router.ServeHTTP(recorder, req)
	return recorder
}

func Test_OtlpHttpExport_WithValidApiKeyAndSpan_Accepted(t *testing.T) {
	fixture := SetUpFixture("OTLP Traces HTTP Valid")
	request := BuildExportRequest("hello from otlp http")

	recorder := postOtlpTraces(t, fixture, fixture.ApiKey.Token, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	var response collectortracev1.ExportTraceServiceResponse
	require.NoError(t, proto.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Nil(t, response.PartialSuccess)

	// Check it landed in storage, not just that HTTP accepted it.
	require.NoError(t, spans_core.GetSpanCoreRepository().ForceFlush())
	projectID, err := uuid.Parse(fixture.ProjectID)
	require.NoError(t, err)

	from := time.Now().UTC().Add(-time.Hour)
	to := time.Now().UTC().Add(time.Hour)
	trace, err := spans_core.GetSpanCoreRepository().GetTrace(
		projectID, "0102030405060708090a0b0c0d0e0f10", &spans_core.TimeRangeDTO{From: &from, To: &to},
	)
	require.NoError(t, err)
	require.Len(t, trace.Spans, 1)
	assert.Equal(t, "hello from otlp http", trace.Spans[0].Name)
	assert.Equal(t, "test-service", trace.Spans[0].ServiceName)
}

func Test_OtlpHttpExport_WithoutApiKey_Unauthorized(t *testing.T) {
	fixture := SetUpFixture("OTLP Traces HTTP No Key")
	request := BuildExportRequest("should be rejected")

	recorder := postOtlpTraces(t, fixture, "", request)
	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
}

func Test_OtlpHttpExport_WithInvalidApiKey_Unauthorized(t *testing.T) {
	fixture := SetUpFixture("OTLP Traces HTTP Bad Key")
	request := BuildExportRequest("should be rejected")

	recorder := postOtlpTraces(t, fixture, "lb_not-a-real-key", request)
	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
}

func Test_OtlpHttpExport_WithEmptyResourceSpans_AcceptedWithNoRecords(t *testing.T) {
	fixture := SetUpFixture("OTLP Traces HTTP Empty")

	recorder := postOtlpTraces(t, fixture, fixture.ApiKey.Token, &collectortracev1.ExportTraceServiceRequest{})
	assert.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
}

// The Collector's otlphttp exporter gzips by default.
func Test_OtlpHttpExport_GzipEncoded_Accepted(t *testing.T) {
	fixture := SetUpFixture("OTLP Traces HTTP Gzip")
	request := BuildExportRequest("hello from a gzip-compressed otlp collector")

	payload, err := proto.Marshal(request)
	require.NoError(t, err)

	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	_, err = gzipWriter.Write(payload)
	require.NoError(t, err)
	require.NoError(t, gzipWriter.Close())

	req, err := http.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(compressed.Bytes()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("X-API-Key", fixture.ApiKey.Token)

	recorder := httptest.NewRecorder()
	fixture.Router.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	var response collectortracev1.ExportTraceServiceResponse
	require.NoError(t, proto.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Nil(t, response.PartialSuccess)
}
