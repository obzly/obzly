// Package report turns run results into OTLP metrics and logs and sends
// them to a Site project, the way any OTel exporter would.
package report

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	collectorlogsv1 "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectormetricsv1 "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"

	"obzly/internal/synthetics/spec"
)

// ServiceName is the service.name of everything a probe sends.
const ServiceName = "obzly-probe"

// RunAttribute holds the whole RunResult as JSON on each run's log line;
// the UI reads run history and run detail from it.
const RunAttribute = "synthetics_run"

// Sender posts to a destination's OTLP/HTTP endpoint.
type Sender struct {
	Client  *http.Client
	Version string
}

// StatusError is a destination's answer other than 200.
type StatusError struct {
	URL    string
	Status int
	Body   string
}

// NewClient is the client a Sender should use. It doesn't follow redirects, which would carry the
// ingest key (X-API-Key) elsewhere; a 3xx is reported as the Site's answer.
func NewClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s answered %d: %s", e.URL, e.Status, e.Body)
}

// Retryable is false for a 4xx other than 408 and 429: the same request would be refused again.
func (e *StatusError) Retryable() bool {
	return e.Status < 400 || e.Status >= 500 || e.Status == http.StatusRequestTimeout ||
		e.Status == http.StatusTooManyRequests
}

// Send exports results' metrics, then their log lines.
func (s *Sender) Send(
	ctx context.Context,
	destination spec.Destination,
	probe spec.ProbeInfo,
	results []spec.RunResult,
) error {
	if err := s.SendMetrics(ctx, destination, probe, results); err != nil {
		return fmt.Errorf("metrics: %w", err)
	}
	if err := s.SendLogs(ctx, destination, probe, results); err != nil {
		return fmt.Errorf("logs: %w", err)
	}
	return nil
}

// SendMetrics exports results' metrics.
func (s *Sender) SendMetrics(
	ctx context.Context,
	destination spec.Destination,
	probe spec.ProbeInfo,
	results []spec.RunResult,
) error {
	if len(results) == 0 {
		return nil
	}
	base := strings.TrimRight(destination.OTLPURL, "/")
	return s.post(ctx, base+"/v1/metrics", destination.APIKey, Metrics(s.metricsResource(probe), results))
}

// SendLogs exports results' log lines.
func (s *Sender) SendLogs(
	ctx context.Context,
	destination spec.Destination,
	probe spec.ProbeInfo,
	results []spec.RunResult,
) error {
	if len(results) == 0 {
		return nil
	}
	base := strings.TrimRight(destination.OTLPURL, "/")
	return s.post(ctx, base+"/v1/logs", destination.APIKey, Logs(s.logsResource(probe), results))
}

// Metrics is one gauge point per measurement per run, labelled check_id, check, type, location,
// probe and target (plus phase for timings, step and step_index for steps). tls_verified is only
// on the TLS gauges: a run that never reached TLS (a timeout) lacks it, which would split a
// monitor's gauges into several series. Timings come from passing and warning runs only, since a
// failed run's time (often the timeout) would skew latency; its log line keeps them.
func Metrics(resource *resourcev1.Resource, results []spec.RunResult) *collectormetricsv1.ExportMetricsServiceRequest {
	gauges := map[string][]*metricsv1.NumberDataPoint{}
	var order []string
	add := func(name string, value float64, at uint64, labels []*commonv1.KeyValue) {
		if _, ok := gauges[name]; !ok {
			order = append(order, name)
		}
		gauges[name] = append(gauges[name], &metricsv1.NumberDataPoint{
			TimeUnixNano: at, Attributes: labels, Value: &metricsv1.NumberDataPoint_AsDouble{AsDouble: value},
		})
	}
	for _, result := range results {
		at := uint64(result.StartedAt.UnixNano())
		labels := commonLabels(result)
		add("synthetics_up", boolValue(result.Status != spec.StatusDown), at, labels)
		add("synthetics_warn", boolValue(result.Status == spec.StatusWarn), at, labels)
		if result.Status != spec.StatusDown {
			add("synthetics_duration_seconds", result.DurationMs/1000, at, withLabel(labels, "phase", "total"))
			for phase, ms := range result.PhasesMs {
				if phase != "total" {
					add("synthetics_duration_seconds", ms/1000, at, withLabel(labels, "phase", phase))
				}
			}
		}
		if result.HTTPStatus > 0 {
			add("synthetics_http_status", float64(result.HTTPStatus), at, labels)
			add("synthetics_response_bytes", float64(result.ResponseBytes), at, labels)
			add("synthetics_redirects", float64(result.Redirects), at, labels)
		}
		if result.TLS != nil {
			tlsLabels := withLabel(labels, "tls_verified", strconv.FormatBool(result.TLS.Verified))
			add("synthetics_cert_expiry_seconds", result.TLS.NotAfter.Sub(result.StartedAt).Seconds(), at, tlsLabels)
			info := withLabel(
				withLabel(withLabel(tlsLabels, "issuer", result.TLS.Issuer), "protocol", result.TLS.Protocol),
				"cipher",
				result.TLS.Cipher,
			)
			add("synthetics_tls_info", 1, at, info)
		}
		if result.Type == spec.TypeDNS {
			add("synthetics_dns_answers", float64(len(result.DNSAnswers)), at, labels)
		}
		if result.ICMP != nil {
			if result.ICMP.Received > 0 {
				add("synthetics_icmp_rtt_seconds", result.ICMP.RTTAvgMs/1000, at, labels)
				add("synthetics_icmp_jitter_seconds", result.ICMP.JitterMs/1000, at, labels)
			}
			add("synthetics_icmp_loss_ratio", result.ICMP.LossRatio, at, labels)
		}
		// Skipped steps report nothing: they neither passed nor failed.
		for _, step := range result.Steps {
			if step.Status == spec.StatusSkipped {
				continue
			}
			stepLabels := withLabel(withLabel(labels, "step", step.Name), "step_index", strconv.Itoa(step.Index))
			add("synthetics_step_up", boolValue(step.Status != spec.StatusDown), at, stepLabels)
			if step.Status != spec.StatusDown {
				add("synthetics_step_duration_seconds", step.DurationMs/1000, at, stepLabels)
			}
		}
	}
	metrics := make([]*metricsv1.Metric, 0, len(order))
	for _, name := range order {
		metrics = append(
			metrics,
			&metricsv1.Metric{
				Name: name,
				Data: &metricsv1.Metric_Gauge{Gauge: &metricsv1.Gauge{DataPoints: gauges[name]}},
			},
		)
	}
	return &collectormetricsv1.ExportMetricsServiceRequest{ResourceMetrics: []*metricsv1.ResourceMetrics{{
		Resource: resource, ScopeMetrics: []*metricsv1.ScopeMetrics{{Metrics: metrics}},
	}}}
}

// Logs is one line per run: info when up, warn when amber, error when red.
func Logs(resource *resourcev1.Resource, results []spec.RunResult) *collectorlogsv1.ExportLogsServiceRequest {
	records := make([]*logsv1.LogRecord, 0, len(results))
	for _, result := range results {
		level, severity := "info", logsv1.SeverityNumber_SEVERITY_NUMBER_INFO
		switch result.Status {
		case spec.StatusWarn:
			level, severity = "warn", logsv1.SeverityNumber_SEVERITY_NUMBER_WARN
		case spec.StatusDown:
			level, severity = "error", logsv1.SeverityNumber_SEVERITY_NUMBER_ERROR
		}
		encoded, _ := json.Marshal(result)
		attributes := commonLabels(result)
		if result.TLS != nil {
			attributes = append(attributes, stringAttribute("tls_verified", strconv.FormatBool(result.TLS.Verified)))
		}
		attributes = append(attributes,
			stringAttribute("status", result.Status),
			doubleAttribute("duration_ms", result.DurationMs),
			stringAttribute(RunAttribute, string(encoded)),
		)
		if result.HTTPStatus > 0 {
			attributes = append(attributes, intAttribute("http_status", int64(result.HTTPStatus)))
		}
		if result.Error != "" {
			attributes = append(attributes, stringAttribute("error", result.Error))
		}
		at := uint64(result.StartedAt.UnixNano())
		records = append(records, &logsv1.LogRecord{
			TimeUnixNano: at, ObservedTimeUnixNano: at, SeverityText: level, SeverityNumber: severity,
			Body:       &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: Summary(result)}},
			Attributes: attributes,
		})
	}
	return &collectorlogsv1.ExportLogsServiceRequest{ResourceLogs: []*logsv1.ResourceLogs{{
		Resource: resource, ScopeLogs: []*logsv1.ScopeLogs{{LogRecords: records}},
	}}}
}

// Summary is a run's log message, e.g. "Public API health from
// aws-eu-west-1: up in 212 ms (HTTP 200)".
func Summary(result spec.RunResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s from %s: %s in %.0f ms", result.Check, result.Location, result.Status, result.DurationMs)
	if result.HTTPStatus > 0 {
		fmt.Fprintf(&b, " (HTTP %d)", result.HTTPStatus)
	}
	if result.Error != "" {
		b.WriteString(": ")
		b.WriteString(result.Error)
	} else {
		for _, assertion := range result.Assertions {
			if !assertion.Passed {
				fmt.Fprintf(&b, ": %s failed (got %s)", assertion.Description, assertion.Actual)
				break
			}
		}
	}
	for _, step := range result.Steps {
		b.WriteString("\n")
		writeStep(&b, step)
	}
	return b.String()
}

// writeStep is one line per step: "2. Create order: POST https://... → 201
// in 240 ms (dns 0 · connect 0 · tls 0 · ttfb 228 · transfer 12 ms);
// assertions 2/2 passed; extracted orderId, token (secret)".
func writeStep(b *strings.Builder, step spec.StepResult) {
	fmt.Fprintf(b, "%d. %s: ", step.Index, step.Name)
	if step.Status == spec.StatusSkipped {
		fmt.Fprintf(b, "skipped (%s %s)", step.Method, step.URL)
		return
	}
	fmt.Fprintf(b, "%s %s", step.Method, step.URL)
	if step.HTTPStatus > 0 {
		fmt.Fprintf(b, " → %d", step.HTTPStatus)
	}
	fmt.Fprintf(b, " in %.0f ms, %s", step.DurationMs, step.Status)
	var timings []string
	for _, phase := range []string{"dns", "connect", "tls", "ttfb", "transfer"} {
		if ms, ok := step.PhasesMs[phase]; ok {
			timings = append(timings, fmt.Sprintf("%s %.0f", phase, ms))
		}
	}
	if len(timings) > 0 {
		fmt.Fprintf(b, " (%s ms)", strings.Join(timings, " · "))
	}
	if step.AfterFailure {
		b.WriteString(", ran after a failure")
	}
	if len(step.Assertions) > 0 {
		passed := 0
		var failed []string
		for _, assertion := range step.Assertions {
			if assertion.Passed {
				passed++
			} else {
				failed = append(failed, fmt.Sprintf("%s failed (got %s)", assertion.Description, assertion.Actual))
			}
		}
		fmt.Fprintf(b, "; assertions %d/%d passed", passed, len(step.Assertions))
		if len(failed) > 0 {
			fmt.Fprintf(b, ": %s", strings.Join(failed, ", "))
		}
	}
	if len(step.Extracted) > 0 {
		names := make([]string, len(step.Extracted))
		for i, value := range step.Extracted {
			names[i] = value.Name
			if value.Secret {
				names[i] += " (secret)"
			}
		}
		fmt.Fprintf(b, "; extracted %s", strings.Join(names, ", "))
	}
	if step.Error != "" {
		fmt.Fprintf(b, "; %s", step.Error)
	}
}

// metricsResource leaves out service.version: ingest turns resource attributes into labels, so
// every probe upgrade would start new series.
func (s *Sender) metricsResource(probe spec.ProbeInfo) *resourcev1.Resource {
	return &resourcev1.Resource{Attributes: []*commonv1.KeyValue{
		stringAttribute("service.name", ServiceName),
		stringAttribute("host.name", probe.Name),
	}}
}

func (s *Sender) logsResource(probe spec.ProbeInfo) *resourcev1.Resource {
	return &resourcev1.Resource{Attributes: []*commonv1.KeyValue{
		stringAttribute("service.name", ServiceName),
		stringAttribute("service.version", s.Version),
		stringAttribute("host.name", probe.Name),
	}}
}

func (s *Sender) post(ctx context.Context, url, apiKey string, message proto.Message) error {
	payload, err := proto.Marshal(message)
	if err != nil {
		return err
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, _ = writer.Write(payload)
	_ = writer.Close()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &compressed)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/x-protobuf")
	request.Header.Set("Content-Encoding", "gzip")
	request.Header.Set("X-API-Key", apiKey)
	response, err := s.Client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
	if response.StatusCode != http.StatusOK {
		return &StatusError{URL: url, Status: response.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	return nil
}

func commonLabels(result spec.RunResult) []*commonv1.KeyValue {
	return []*commonv1.KeyValue{
		stringAttribute("check_id", result.CheckID),
		stringAttribute("check", result.Check),
		stringAttribute("type", result.Type),
		stringAttribute("location", result.Location),
		stringAttribute("probe", result.Probe),
		stringAttribute("target", result.Target),
	}
}

func withLabel(labels []*commonv1.KeyValue, key, value string) []*commonv1.KeyValue {
	out := make([]*commonv1.KeyValue, len(labels), len(labels)+1)
	copy(out, labels)
	return append(out, stringAttribute(key, value))
}

func boolValue(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func stringAttribute(key, value string) *commonv1.KeyValue {
	return &commonv1.KeyValue{
		Key:   key,
		Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: value}},
	}
}

func intAttribute(key string, value int64) *commonv1.KeyValue {
	return &commonv1.KeyValue{Key: key, Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_IntValue{IntValue: value}}}
}

func doubleAttribute(key string, value float64) *commonv1.KeyValue {
	return &commonv1.KeyValue{
		Key:   key,
		Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_DoubleValue{DoubleValue: value}},
	}
}
