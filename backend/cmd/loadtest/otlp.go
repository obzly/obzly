package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"time"

	collectorlogsv1 "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectormetricsv1 "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collectortracev1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	grpcgzip "google.golang.org/grpc/encoding/gzip"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// otlpSender sends OTLP export requests for one signal, over HTTP
// (protobuf, the OTel Collector's otlphttp default) or gRPC. batchSize is
// log records, spans or data points per request.
type otlpSender struct {
	signal    string
	protocol  string
	httpURL   string
	apiKey    string
	batchSize int
	gzip      bool
	client    *http.Client
	conn      *grpc.ClientConn
}

func newOTLPSender(
	signal, protocol, baseURL, grpcAddr, apiKey string,
	batchSize int,
	useGzip, grpcTLS bool,
	timeout time.Duration,
) (*otlpSender, error) {
	s := &otlpSender{signal: signal, protocol: protocol, apiKey: apiKey, batchSize: batchSize, gzip: useGzip}
	if protocol == "grpc" {
		creds := insecure.NewCredentials()
		if grpcTLS {
			creds = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
		}
		conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(creds))
		if err != nil {
			return nil, fmt.Errorf("grpc dial %s: %w", grpcAddr, err)
		}
		s.conn = conn
		return s, nil
	}
	s.httpURL = fmt.Sprintf("%s/v1/%s", baseURL, signal)
	s.client = &http.Client{Timeout: timeout}
	return s, nil
}

func (s *otlpSender) target() string {
	if s.protocol == "grpc" {
		return fmt.Sprintf("OTLP/gRPC %s (%s)", s.conn.Target(), s.signal)
	}
	return "OTLP/HTTP " + s.httpURL
}

func (s *otlpSender) unit() string {
	switch s.signal {
	case "traces":
		return "spans"
	case "metrics":
		return "data points"
	default:
		return "logs"
	}
}

func (s *otlpSender) close() {
	if s.conn != nil {
		_ = s.conn.Close()
	}
}

func (s *otlpSender) send(ctx context.Context, rng *rand.Rand) result {
	var request proto.Message
	switch s.signal {
	case "traces":
		request = buildTraceRequest(rng, s.batchSize)
	case "metrics":
		request = buildMetricsRequest(rng, s.batchSize)
	default:
		request = buildLogsRequest(rng, s.batchSize)
	}
	if s.protocol == "grpc" {
		return s.sendGRPC(ctx, request)
	}
	return s.sendHTTP(ctx, request)
}

func (s *otlpSender) sendHTTP(ctx context.Context, request proto.Message) result {
	body, err := proto.Marshal(request)
	if err != nil {
		return result{err: fmt.Errorf("marshal request: %w", err)}
	}
	if s.gzip {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		_, _ = zw.Write(body)
		_ = zw.Close()
		body = buf.Bytes()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.httpURL, bytes.NewReader(body))
	if err != nil {
		return result{err: fmt.Errorf("build request: %w", err)}
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	if s.gzip {
		req.Header.Set("Content-Encoding", "gzip")
	}
	if s.apiKey != "" {
		req.Header.Set("X-API-Key", s.apiKey)
	}

	start := time.Now()
	resp, err := s.client.Do(req)
	latency := time.Since(start)
	if err != nil {
		return result{err: fmt.Errorf("request failed: %w", err), latency: latency}
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return result{
			err:            fmt.Errorf("unexpected status %d: %s", resp.StatusCode, truncate(string(respBody), 200)),
			latency:        latency,
			httpStatusCode: resp.StatusCode,
		}
	}

	rejected, err := s.rejectedFromHTTP(respBody)
	if err != nil {
		return result{err: err, latency: latency}
	}
	return result{
		latency:        latency,
		sentBytes:      len(body),
		accepted:       s.batchSize - rejected,
		rejected:       rejected,
		httpStatusCode: resp.StatusCode,
	}
}

func (s *otlpSender) rejectedFromHTTP(body []byte) (int, error) {
	switch s.signal {
	case "traces":
		var r collectortracev1.ExportTraceServiceResponse
		if err := proto.Unmarshal(body, &r); err != nil {
			return 0, fmt.Errorf("unmarshal response: %w", err)
		}
		return int(r.GetPartialSuccess().GetRejectedSpans()), nil
	case "metrics":
		var r collectormetricsv1.ExportMetricsServiceResponse
		if err := proto.Unmarshal(body, &r); err != nil {
			return 0, fmt.Errorf("unmarshal response: %w", err)
		}
		return int(r.GetPartialSuccess().GetRejectedDataPoints()), nil
	default:
		var r collectorlogsv1.ExportLogsServiceResponse
		if err := proto.Unmarshal(body, &r); err != nil {
			return 0, fmt.Errorf("unmarshal response: %w", err)
		}
		return int(r.GetPartialSuccess().GetRejectedLogRecords()), nil
	}
}

func (s *otlpSender) sendGRPC(ctx context.Context, request proto.Message) result {
	if s.apiKey != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "x-api-key", s.apiKey)
	}
	var opts []grpc.CallOption
	if s.gzip {
		opts = append(opts, grpc.UseCompressor(grpcgzip.Name))
	}

	start := time.Now()
	var rejected int64
	var err error
	switch r := request.(type) {
	case *collectortracev1.ExportTraceServiceRequest:
		var resp *collectortracev1.ExportTraceServiceResponse
		resp, err = collectortracev1.NewTraceServiceClient(s.conn).Export(ctx, r, opts...)
		rejected = resp.GetPartialSuccess().GetRejectedSpans()
	case *collectormetricsv1.ExportMetricsServiceRequest:
		var resp *collectormetricsv1.ExportMetricsServiceResponse
		resp, err = collectormetricsv1.NewMetricsServiceClient(s.conn).Export(ctx, r, opts...)
		rejected = resp.GetPartialSuccess().GetRejectedDataPoints()
	case *collectorlogsv1.ExportLogsServiceRequest:
		var resp *collectorlogsv1.ExportLogsServiceResponse
		resp, err = collectorlogsv1.NewLogsServiceClient(s.conn).Export(ctx, r, opts...)
		rejected = resp.GetPartialSuccess().GetRejectedLogRecords()
	}
	latency := time.Since(start)
	if err != nil {
		st, _ := status.FromError(err)
		return result{err: fmt.Errorf("grpc %s: %s", st.Code(), truncate(st.Message(), 200)), latency: latency}
	}
	return result{
		latency:   latency,
		sentBytes: proto.Size(request),
		accepted:  s.batchSize - int(rejected),
		rejected:  int(rejected),
	}
}

func strAttr(key, value string) *commonv1.KeyValue {
	return &commonv1.KeyValue{
		Key:   key,
		Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: value}},
	}
}

func intAttr(key string, value int64) *commonv1.KeyValue {
	return &commonv1.KeyValue{Key: key, Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_IntValue{IntValue: value}}}
}

func resourceFor(rng *rand.Rand) *resourcev1.Resource {
	return &resourcev1.Resource{Attributes: []*commonv1.KeyValue{
		strAttr("service.name", services[rng.Intn(len(services))]),
		strAttr("host.name", fmt.Sprintf("loadtest-host-%d", rng.Intn(8))),
		strAttr("loadtest", "true"),
	}}
}

var otlpSeverities = map[string]logsv1.SeverityNumber{
	"DEBUG": logsv1.SeverityNumber_SEVERITY_NUMBER_DEBUG,
	"INFO":  logsv1.SeverityNumber_SEVERITY_NUMBER_INFO,
	"WARN":  logsv1.SeverityNumber_SEVERITY_NUMBER_WARN,
	"ERROR": logsv1.SeverityNumber_SEVERITY_NUMBER_ERROR,
}

func buildLogsRequest(rng *rand.Rand, n int) *collectorlogsv1.ExportLogsServiceRequest {
	now := uint64(time.Now().UnixNano())
	records := make([]*logsv1.LogRecord, n)
	for i := range records {
		level := levels[rng.Intn(len(levels))]
		records[i] = &logsv1.LogRecord{
			TimeUnixNano:   now,
			SeverityText:   level,
			SeverityNumber: otlpSeverities[level],
			Body: &commonv1.AnyValue{
				Value: &commonv1.AnyValue_StringValue{StringValue: messages[rng.Intn(len(messages))]},
			},
			Attributes: []*commonv1.KeyValue{
				strAttr("request_id", fmt.Sprintf("req-%d-%d", now, rng.Int63())),
				intAttr("duration_ms", int64(rng.Intn(2000))),
				intAttr("status_code", int64([]int{200, 200, 200, 201, 400, 404, 500}[rng.Intn(7)])),
				strAttr("user_id", fmt.Sprintf("user-%d", rng.Intn(10000))),
			},
		}
	}
	return &collectorlogsv1.ExportLogsServiceRequest{ResourceLogs: []*logsv1.ResourceLogs{{
		Resource:  resourceFor(rng),
		ScopeLogs: []*logsv1.ScopeLogs{{LogRecords: records}},
	}}}
}

var operations = []string{
	"GET /api/orders",
	"POST /api/checkout",
	"GET /api/users/:id",
	"db.query",
	"cache.get",
	"publish event",
}

// buildTraceRequest emits traces of up to 5 spans (a root plus children),
// so the batch looks like real request trees rather than lone spans.
func buildTraceRequest(rng *rand.Rand, n int) *collectortracev1.ExportTraceServiceRequest {
	end := time.Now()
	spans := make([]*tracev1.Span, 0, n)
	for len(spans) < n {
		traceID := make([]byte, 16)
		rng.Read(traceID)
		rootID := make([]byte, 8)
		rng.Read(rootID)
		size := min(1+rng.Intn(5), n-len(spans))
		for j := 0; j < size; j++ {
			spanID := rootID
			var parentID []byte
			kind := tracev1.Span_SPAN_KIND_SERVER
			if j > 0 {
				spanID = make([]byte, 8)
				rng.Read(spanID)
				parentID = rootID
				kind = tracev1.Span_SPAN_KIND_CLIENT
			}
			duration := time.Duration(1+rng.Intn(500)) * time.Millisecond
			code := tracev1.Status_STATUS_CODE_OK
			if rng.Intn(20) == 0 {
				code = tracev1.Status_STATUS_CODE_ERROR
			}
			spans = append(spans, &tracev1.Span{
				TraceId:           traceID,
				SpanId:            spanID,
				ParentSpanId:      parentID,
				Name:              operations[rng.Intn(len(operations))],
				Kind:              kind,
				StartTimeUnixNano: uint64(end.Add(-duration).UnixNano()),
				EndTimeUnixNano:   uint64(end.UnixNano()),
				Status:            &tracev1.Status{Code: code},
				Attributes: []*commonv1.KeyValue{
					intAttr("http.response.status_code", int64([]int{200, 200, 201, 404, 500}[rng.Intn(5)])),
					strAttr("user_id", fmt.Sprintf("user-%d", rng.Intn(10000))),
				},
			})
		}
	}
	return &collectortracev1.ExportTraceServiceRequest{ResourceSpans: []*tracev1.ResourceSpans{{
		Resource:   resourceFor(rng),
		ScopeSpans: []*tracev1.ScopeSpans{{Spans: spans}},
	}}}
}

// buildMetricsRequest spreads n data points over a gauge and a cumulative
// counter with a bounded label set, like a scraped host would send.
func buildMetricsRequest(rng *rand.Rand, n int) *collectormetricsv1.ExportMetricsServiceRequest {
	now := uint64(time.Now().UnixNano())
	gauge := make([]*metricsv1.NumberDataPoint, 0, n/2+1)
	counter := make([]*metricsv1.NumberDataPoint, 0, n/2+1)
	for i := 0; i < n; i++ {
		point := &metricsv1.NumberDataPoint{
			TimeUnixNano: now,
			Attributes: []*commonv1.KeyValue{
				strAttr("endpoint", operations[rng.Intn(len(operations))]),
				strAttr("instance", fmt.Sprintf("pod-%d", rng.Intn(50))),
			},
		}
		if i%2 == 0 {
			point.Value = &metricsv1.NumberDataPoint_AsDouble{AsDouble: rng.Float64()}
			gauge = append(gauge, point)
		} else {
			point.Value = &metricsv1.NumberDataPoint_AsInt{AsInt: int64(rng.Intn(1_000_000))}
			counter = append(counter, point)
		}
	}
	return &collectormetricsv1.ExportMetricsServiceRequest{ResourceMetrics: []*metricsv1.ResourceMetrics{{
		Resource: resourceFor(rng),
		ScopeMetrics: []*metricsv1.ScopeMetrics{{Metrics: []*metricsv1.Metric{
			{
				Name: "loadtest.cpu.utilization",
				Data: &metricsv1.Metric_Gauge{Gauge: &metricsv1.Gauge{DataPoints: gauge}},
			},
			{Name: "loadtest.requests", Data: &metricsv1.Metric_Sum{Sum: &metricsv1.Sum{
				DataPoints:             counter,
				IsMonotonic:            true,
				AggregationTemporality: metricsv1.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
			}}},
		}}},
	}}}
}
