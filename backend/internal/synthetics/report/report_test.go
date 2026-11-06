package report

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	collectorlogsv1 "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectormetricsv1 "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/protobuf/proto"

	"obzly/internal/synthetics/spec"
)

func Test_Send_PostsGzippedOTLPWithTheDestinationKey(t *testing.T) {
	var metrics collectormetricsv1.ExportMetricsServiceRequest
	var logs collectorlogsv1.ExportLogsServiceRequest
	keys := map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys[r.URL.Path] = r.Header.Get("X-API-Key")
		reader, err := gzip.NewReader(r.Body)
		require.NoError(t, err)
		body, err := io.ReadAll(reader)
		require.NoError(t, err)
		switch r.URL.Path {
		case "/v1/metrics":
			require.NoError(t, proto.Unmarshal(body, &metrics))
		case "/v1/logs":
			require.NoError(t, proto.Unmarshal(body, &logs))
		}
	}))
	defer server.Close()
	result := spec.RunResult{
		CheckID:    "c1",
		Check:      "Public API health",
		Type:       spec.TypeHTTP,
		Target:     "https://api.example.com/health",
		Location:   "aws-eu-west-1",
		Probe:      "probe-euw1-01",
		StartedAt:  time.Now().UTC(),
		Status:     spec.StatusWarn,
		DurationMs: 612,
		PhasesMs:   map[string]float64{"dns": 8, "ttfb": 500},
		HTTPStatus: 200,
		TLS: &spec.TLSInfo{
			Issuer:   "Amazon RSA 2048 M02",
			Protocol: "TLS 1.3",
			Verified: true,
			NotAfter: time.Now().Add(47 * 24 * time.Hour),
		},
	}

	sender := &Sender{Client: server.Client(), Version: "0.55.0"}
	err := sender.Send(context.Background(), spec.Destination{OTLPURL: server.URL + "/", APIKey: "lb_test"},
		spec.ProbeInfo{Name: "probe-euw1-01"}, []spec.RunResult{result})

	require.NoError(t, err)
	require.Equal(t, map[string]string{"/v1/metrics": "lb_test", "/v1/logs": "lb_test"}, keys)
	names := map[string]int{}
	for _, metric := range metrics.ResourceMetrics[0].ScopeMetrics[0].Metrics {
		names[metric.Name] = len(metric.GetGauge().DataPoints)
	}
	require.Equal(t, 1, names["synthetics_up"])
	require.Equal(t, 1, names["synthetics_warn"])
	require.Equal(t, 3, names["synthetics_duration_seconds"], "total, dns and ttfb")
	require.Equal(t, 1, names["synthetics_tls_info"])
	require.Equal(t, 1, names["synthetics_cert_expiry_seconds"])
	up := metrics.ResourceMetrics[0].ScopeMetrics[0].Metrics[0].GetGauge().DataPoints[0]
	require.Equal(t, 1.0, up.GetAsDouble(), "a warning still passed")
	require.Equal(t, "aws-eu-west-1", attribute(up.Attributes, "location"))
	require.False(t, hasAttribute(up.Attributes, "tls_verified"))
	require.Equal(t, "obzly-probe", attribute(metrics.ResourceMetrics[0].Resource.Attributes, "service.name"))
	require.Equal(t, "probe-euw1-01", attribute(metrics.ResourceMetrics[0].Resource.Attributes, "host.name"))
	require.False(t, hasAttribute(metrics.ResourceMetrics[0].Resource.Attributes, "service.version"),
		"a probe upgrade doesn't start new series")

	record := logs.ResourceLogs[0].ScopeLogs[0].LogRecords[0]
	require.Equal(t, "warn", record.SeverityText)
	require.Equal(t, "Public API health from aws-eu-west-1: warn in 612 ms (HTTP 200)", record.Body.GetStringValue())
	require.Equal(t, "true", attribute(record.Attributes, "tls_verified"))
	var decoded spec.RunResult
	require.NoError(t, json.Unmarshal([]byte(attribute(record.Attributes, RunAttribute)), &decoded))
	require.Equal(t, "c1", decoded.CheckID)
	require.Equal(t, "obzly-probe", attribute(logs.ResourceLogs[0].Resource.Attributes, "service.name"))
	require.Equal(t, "0.55.0", attribute(logs.ResourceLogs[0].Resource.Attributes, "service.version"))
}

func Test_Metrics_RunsWithAndWithoutTLS_RunGaugesShareOneLabelSet_TLSGaugesCarryVerified(t *testing.T) {
	started := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	passed := spec.RunResult{
		CheckID: "c1", Check: "Public API health", Type: spec.TypeHTTP, Target: "https://api.example.com/health",
		Location: "aws-eu-west-1", Probe: "probe-euw1-01", StartedAt: started, Status: spec.StatusUp,
		DurationMs: 212, HTTPStatus: 200,
		TLS: &spec.TLSInfo{Issuer: "Contoso CA", Verified: true, NotAfter: started.Add(47 * 24 * time.Hour)},
	}
	timedOut := passed
	timedOut.StartedAt, timedOut.Status, timedOut.HTTPStatus, timedOut.TLS = started.Add(time.Minute),
		spec.StatusDown, 0, nil

	points := gaugePoints(Metrics(nil, []spec.RunResult{passed, timedOut}))

	require.Len(t, points["synthetics_up"], 2)
	require.Equal(t, labelSet(points["synthetics_up"][0].Attributes), labelSet(points["synthetics_up"][1].Attributes),
		"one series per monitor, whether or not TLS ran")
	require.Equal(t, "true", attribute(points["synthetics_cert_expiry_seconds"][0].Attributes, "tls_verified"))
	require.Equal(t, "true", attribute(points["synthetics_tls_info"][0].Attributes, "tls_verified"))
	require.Equal(t, (47 * 24 * time.Hour).Seconds(), points["synthetics_cert_expiry_seconds"][0].GetAsDouble(),
		"measured from the run's start, not from when it was sent")
}

func Test_Metrics_DownRuns_SendNoTimings_LogsKeepThem(t *testing.T) {
	started := time.Now().UTC()
	httpDown := spec.RunResult{
		CheckID: "h1", Type: spec.TypeHTTP, StartedAt: started, Status: spec.StatusDown, DurationMs: 10000,
		PhasesMs: map[string]float64{"dns": 3, "connect": 9997}, Error: "timed out",
	}
	icmpLost := spec.RunResult{
		CheckID: "i1", Type: spec.TypeICMP, StartedAt: started, Status: spec.StatusDown,
		ICMP: &spec.ICMPInfo{Sent: 3, LossRatio: 1}, Error: "no replies to 3 pings",
	}
	icmpLossy := spec.RunResult{
		CheckID: "i2", Type: spec.TypeICMP, StartedAt: started, Status: spec.StatusDown, DurationMs: 21,
		ICMP: &spec.ICMPInfo{Sent: 3, Received: 1, LossRatio: 2.0 / 3, RTTAvgMs: 21}, Error: "67% packet loss",
	}

	points := gaugePoints(Metrics(nil, []spec.RunResult{httpDown, icmpLost, icmpLossy}))
	logs := Logs(nil, []spec.RunResult{httpDown})

	require.Len(t, points["synthetics_up"], 3)
	require.NotContains(t, points, "synthetics_duration_seconds")
	require.Len(t, points["synthetics_icmp_loss_ratio"], 2)
	require.Len(t, points["synthetics_icmp_rtt_seconds"], 1, "only the run that got a reply")
	require.Equal(t, "i2", attribute(points["synthetics_icmp_rtt_seconds"][0].Attributes, "check_id"))
	require.Len(t, points["synthetics_icmp_jitter_seconds"], 1)
	record := logs.ResourceLogs[0].ScopeLogs[0].LogRecords[0]
	require.Equal(t, 10000.0, attributeValue(record.Attributes, "duration_ms").GetDoubleValue())
	var decoded spec.RunResult
	require.NoError(t, json.Unmarshal([]byte(attribute(record.Attributes, RunAttribute)), &decoded))
	require.Equal(t, 9997.0, decoded.PhasesMs["connect"])
}

func Test_StatusError_4xxOtherThan408And429_IsNotRetryable(t *testing.T) {
	for status, retryable := range map[int]bool{
		400: false, 401: false, 403: false, 404: false, 413: false,
		408: true, 429: true, 500: true, 502: true, 503: true,
	} {
		require.Equal(t, retryable, (&StatusError{Status: status}).Retryable(), status)
	}
}

func Test_Metrics_MultiStepRun_StepGaugesPerRanStep_RunGaugesForTheWhole(t *testing.T) {
	result := spec.RunResult{
		CheckID: "m1", Check: "Checkout API flow", Type: spec.TypeMulti, Location: "aws-ap-south-1",
		StartedAt: time.Now().UTC(), Status: spec.StatusDown, DurationMs: 510,
		Error: "step 2 (Create order): Status equals 201 failed (got 409)",
		Steps: []spec.StepResult{
			{Index: 1, Name: "Sign in", Status: spec.StatusUp, Method: "POST", URL: "https://api.example.com/auth/login",
				HTTPStatus: 200, DurationMs: 182, PhasesMs: map[string]float64{"dns": 12, "ttfb": 96},
				Extracted: []spec.ExtractedValue{{Name: "token", Secret: true}}},
			{Index: 2, Name: "Create order", Status: spec.StatusDown, Method: "POST",
				URL: "https://api.example.com/orders", HTTPStatus: 409, DurationMs: 240},
			{Index: 3, Name: "Check order", Status: spec.StatusSkipped, Method: "GET",
				URL: "https://api.example.com/orders/{{orderId}}"},
		},
	}

	request := Metrics(nil, []spec.RunResult{result})
	summary := Summary(result)

	points := map[string][]*metricsv1.NumberDataPoint{}
	for _, metric := range request.ResourceMetrics[0].ScopeMetrics[0].Metrics {
		points[metric.Name] = metric.GetGauge().DataPoints
	}
	require.Len(t, points["synthetics_up"], 1)
	require.Equal(t, 0.0, points["synthetics_up"][0].GetAsDouble())
	require.NotContains(t, points, "synthetics_duration_seconds", "a failed run reports no timings")
	require.Len(t, points["synthetics_step_up"], 2, "skipped steps report nothing")
	require.Equal(t, 1.0, points["synthetics_step_up"][0].GetAsDouble())
	require.Equal(t, 0.0, points["synthetics_step_up"][1].GetAsDouble())
	require.Equal(t, "Create order", attribute(points["synthetics_step_up"][1].Attributes, "step"))
	require.Equal(t, "2", attribute(points["synthetics_step_up"][1].Attributes, "step_index"))
	require.Len(t, points["synthetics_step_duration_seconds"], 1, "not for the failed step")
	require.InDelta(t, 0.182, points["synthetics_step_duration_seconds"][0].GetAsDouble(), 1e-9)
	require.NotContains(t, points, "synthetics_http_status")
	require.Equal(t, "Checkout API flow from aws-ap-south-1: down in 510 ms: "+
		"step 2 (Create order): Status equals 201 failed (got 409)\n"+
		"1. Sign in: POST https://api.example.com/auth/login → 200 in 182 ms, up (dns 12 · ttfb 96 ms); "+
		"extracted token (secret)\n"+
		"2. Create order: POST https://api.example.com/orders → 409 in 240 ms, down\n"+
		"3. Check order: skipped (GET https://api.example.com/orders/{{orderId}})", summary)
}

func Test_Send_SiteRefusesTheKey_ReturnsItsAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "invalid API key", http.StatusUnauthorized)
	}))
	defer server.Close()

	err := (&Sender{Client: server.Client()}).Send(context.Background(), spec.Destination{OTLPURL: server.URL},
		spec.ProbeInfo{}, []spec.RunResult{{StartedAt: time.Now(), Status: spec.StatusUp}})

	require.ErrorContains(t, err, "401")
	require.ErrorContains(t, err, "invalid API key")
}

func Test_Send_SiteRedirects_TheKeyIsNotForwarded(t *testing.T) {
	var forwarded atomic.Bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded.Store(r.Header.Get("X-API-Key") != "")
	}))
	defer elsewhere.Close()
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer site.Close()

	err := (&Sender{Client: NewClient(5 * time.Second)}).Send(context.Background(),
		spec.Destination{OTLPURL: site.URL, APIKey: "lb_ingest_key"}, spec.ProbeInfo{},
		[]spec.RunResult{{StartedAt: time.Now(), Status: spec.StatusUp}})

	require.ErrorContains(t, err, "307")
	require.False(t, forwarded.Load())
}

func attribute(attributes []*commonv1.KeyValue, key string) string {
	return attributeValue(attributes, key).GetStringValue()
}

func attributeValue(attributes []*commonv1.KeyValue, key string) *commonv1.AnyValue {
	for _, kv := range attributes {
		if kv.Key == key {
			return kv.Value
		}
	}
	return nil
}

func hasAttribute(attributes []*commonv1.KeyValue, key string) bool {
	return attributeValue(attributes, key) != nil
}

func labelSet(attributes []*commonv1.KeyValue) map[string]string {
	labels := map[string]string{}
	for _, kv := range attributes {
		labels[kv.Key] = kv.Value.GetStringValue()
	}
	return labels
}

func gaugePoints(request *collectormetricsv1.ExportMetricsServiceRequest) map[string][]*metricsv1.NumberDataPoint {
	points := map[string][]*metricsv1.NumberDataPoint{}
	for _, metric := range request.ResourceMetrics[0].ScopeMetrics[0].Metrics {
		points[metric.Name] = metric.GetGauge().DataPoints
	}
	return points
}
