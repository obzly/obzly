package prometheus_remotewrite_tests

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"obzly/internal/ingest"
	"obzly/internal/ingest/ingesttest"

	"github.com/prometheus/prometheus/prompb"
	"github.com/stretchr/testify/require"
)

// Prometheus retries 429 only with retry_on_http_429, so a full buffer is 503.
func Test_RemoteWrite_WhenIngestBufferFull_Returns503WithRetryAfter(t *testing.T) {
	ingesttest.StartBroker(t, 2048)
	router, apiKey, _ := setUpFixture("Remote Write JetStream full")
	code, retryAfter := http.StatusNoContent, ""
	for i := 0; i < 20 && code == http.StatusNoContent; i++ {
		// A different sample each time: an identical request is a duplicate
		// the stream drops, which never fills it.
		request := &prompb.WriteRequest{Timeseries: []prompb.TimeSeries{{
			Labels:  []prompb.Label{{Name: "__name__", Value: "fill_total"}},
			Samples: []prompb.Sample{{Value: float64(i), Timestamp: time.Now().UTC().UnixMilli()}},
		}}}
		recorder := postRemoteWrite(t, router, apiKey.Token, request)
		code, retryAfter = recorder.Code, recorder.Header().Get("Retry-After")
	}

	require.Equal(t, http.StatusServiceUnavailable, code)
	require.Equal(t, strconv.Itoa(ingest.RetryAfterSeconds), retryAfter)
}
