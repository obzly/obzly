package otlp_logs_tests

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	collectorlogsv1 "go.opentelemetry.io/proto/otlp/collector/logs/v1"
)

func postOtlpLogs(
	t *testing.T,
	fixture *TestFixture,
	apiKey string,
	request *collectorlogsv1.ExportLogsServiceRequest,
) *httptest.ResponseRecorder {
	body, err := proto.Marshal(request)
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodPost, "/v1/logs", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-protobuf")
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}

	recorder := httptest.NewRecorder()
	fixture.Router.ServeHTTP(recorder, req)
	return recorder
}

func Test_OtlpHttpExport_WithValidApiKeyAndLogRecord_Accepted(t *testing.T) {
	fixture := SetUpFixture("OTLP HTTP Valid")
	request := BuildExportRequest("hello from otlp http")

	recorder := postOtlpLogs(t, fixture, fixture.ApiKey.Token, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	var response collectorlogsv1.ExportLogsServiceResponse
	require.NoError(t, proto.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Nil(t, response.PartialSuccess)
}

func Test_OtlpHttpExport_WithoutApiKey_Unauthorized(t *testing.T) {
	fixture := SetUpFixture("OTLP HTTP No Key")
	request := BuildExportRequest("should be rejected")

	recorder := postOtlpLogs(t, fixture, "", request)
	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
}

func Test_OtlpHttpExport_WithInvalidApiKey_Unauthorized(t *testing.T) {
	fixture := SetUpFixture("OTLP HTTP Bad Key")
	request := BuildExportRequest("should be rejected")

	recorder := postOtlpLogs(t, fixture, "lb_not-a-real-key", request)
	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
}

func Test_OtlpHttpExport_WithEmptyResourceLogs_AcceptedWithNoRecords(t *testing.T) {
	fixture := SetUpFixture("OTLP HTTP Empty")

	recorder := postOtlpLogs(t, fixture, fixture.ApiKey.Token, &collectorlogsv1.ExportLogsServiceRequest{})
	assert.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
}

// The Collector's otlphttp exporter gzips by default; every export from a
// default Collector was rejected with a 400 until otlp.ReadBody gunzipped it.
func Test_OtlpHttpExport_GzipEncoded_Accepted(t *testing.T) {
	fixture := SetUpFixture("OTLP HTTP Gzip")
	request := BuildExportRequest("hello from a gzip-compressed otlp collector")

	payload, err := proto.Marshal(request)
	require.NoError(t, err)

	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	_, err = gzipWriter.Write(payload)
	require.NoError(t, err)
	require.NoError(t, gzipWriter.Close())

	req, err := http.NewRequest(http.MethodPost, "/v1/logs", bytes.NewReader(compressed.Bytes()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("X-API-Key", fixture.ApiKey.Token)

	recorder := httptest.NewRecorder()
	fixture.Router.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	var response collectorlogsv1.ExportLogsServiceResponse
	require.NoError(t, proto.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Nil(t, response.PartialSuccess)
}

func Test_OtlpHttpExport_WithJSONContentType_Accepted(t *testing.T) {
	fixture := SetUpFixture("OTLP HTTP JSON")
	request := BuildExportRequest("hello from otlp http json")

	body, err := protojson.Marshal(request)
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodPost, "/v1/logs", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", fixture.ApiKey.Token)

	recorder := httptest.NewRecorder()
	fixture.Router.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
}
