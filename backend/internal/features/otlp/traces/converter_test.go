package otlp_traces

import (
	"testing"
	"time"

	spans_core "obzly/internal/features/spans/core"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
)

func stringValue(value string) *commonv1.AnyValue {
	return &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: value}}
}

func Test_ConvertResourceSpans_WithFullSpan_ProducesExpectedSpan(t *testing.T) {
	traceID := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10}
	spanID := []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x11, 0x22}
	parentSpanID := []byte{0x99, 0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22}
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	end := start.Add(100 * time.Millisecond)
	projectID := uuid.New()

	resourceSpans := []*tracev1.ResourceSpans{
		{
			Resource: &resourcev1.Resource{
				Attributes: []*commonv1.KeyValue{
					{Key: "service.name", Value: stringValue("checkout")},
					{Key: "deployment.env", Value: stringValue("prod")},
				},
			},
			ScopeSpans: []*tracev1.ScopeSpans{
				{
					Scope: &commonv1.InstrumentationScope{Name: "my-tracer", Version: "1.0.0"},
					Spans: []*tracev1.Span{
						{
							TraceId:           traceID,
							SpanId:            spanID,
							ParentSpanId:      parentSpanID,
							Name:              "POST /checkout",
							Kind:              tracev1.Span_SPAN_KIND_SERVER,
							StartTimeUnixNano: uint64(start.UnixNano()),
							EndTimeUnixNano:   uint64(end.UnixNano()),
							Status:            &tracev1.Status{Code: tracev1.Status_STATUS_CODE_ERROR, Message: "boom"},
							Attributes: []*commonv1.KeyValue{
								{
									Key:   "http.status_code",
									Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_IntValue{IntValue: 500}},
								},
							},
							Events: []*tracev1.Span_Event{
								{
									Name:         "exception",
									TimeUnixNano: uint64(start.Add(50 * time.Millisecond).UnixNano()),
									Attributes: []*commonv1.KeyValue{
										{Key: "exception.type", Value: stringValue("TimeoutError")},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	spanList := ConvertResourceSpans(projectID, resourceSpans)
	require.Len(t, spanList, 1)

	span := spanList[0]
	assert.Equal(t, projectID, span.ProjectID)
	assert.Equal(t, "0102030405060708090a0b0c0d0e0f10", span.TraceID)
	assert.Equal(t, "aabbccddeeff1122", span.SpanID)
	assert.Equal(t, "9988776655443322", span.ParentSpanID)
	assert.Equal(t, "POST /checkout", span.Name)
	assert.Equal(t, "checkout", span.ServiceName)
	assert.Equal(t, spans_core.SpanKindServer, span.Kind)
	assert.True(t, start.Equal(span.StartTime))
	assert.True(t, end.Equal(span.EndTime))
	assert.Equal(t, spans_core.StatusCodeError, span.StatusCode)
	assert.Equal(t, "boom", span.StatusMessage)
	assert.Equal(t, int64(500), span.Attributes["http_status_code"])
	assert.Equal(t, "my-tracer", span.Attributes["scope_name"])
	assert.Equal(t, "1.0.0", span.Attributes["scope_version"])
	assert.Equal(t, "prod", span.ResourceAttributes["deployment_env"])
	assert.Equal(t, "checkout", span.ResourceAttributes["service_name"])
	require.Len(t, span.Events, 1)
	assert.Equal(t, "exception", span.Events[0].Name)
	assert.Equal(t, "TimeoutError", span.Events[0].Attributes["exception_type"])
}

func Test_ConvertResourceSpans_WithoutServiceNameAttribute_FallsBackToUnknownService(t *testing.T) {
	resourceSpans := []*tracev1.ResourceSpans{
		{
			ScopeSpans: []*tracev1.ScopeSpans{
				{Spans: []*tracev1.Span{{Name: "op"}}},
			},
		},
	}

	spanList := ConvertResourceSpans(uuid.New(), resourceSpans)
	require.Len(t, spanList, 1)
	assert.Equal(t, "unknown_service", spanList[0].ServiceName)
}

func Test_ConvertResourceSpans_WithNoParentSpanID_LeavesItEmpty(t *testing.T) {
	resourceSpans := []*tracev1.ResourceSpans{
		{ScopeSpans: []*tracev1.ScopeSpans{{Spans: []*tracev1.Span{{Name: "root"}}}}},
	}

	spanList := ConvertResourceSpans(uuid.New(), resourceSpans)
	require.Len(t, spanList, 1)
	assert.Empty(t, spanList[0].ParentSpanID)
}

func Test_ConvertResourceSpans_WithNilStatus_DefaultsToUnset(t *testing.T) {
	resourceSpans := []*tracev1.ResourceSpans{
		{ScopeSpans: []*tracev1.ScopeSpans{{Spans: []*tracev1.Span{{Name: "op"}}}}},
	}

	spanList := ConvertResourceSpans(uuid.New(), resourceSpans)
	require.Len(t, spanList, 1)
	assert.Equal(t, spans_core.StatusCodeUnset, spanList[0].StatusCode)
}

func Test_SpanKindFor_CoversEveryOTelKind(t *testing.T) {
	testCases := map[tracev1.Span_SpanKind]spans_core.SpanKind{
		tracev1.Span_SPAN_KIND_UNSPECIFIED: spans_core.SpanKindUnspecified,
		tracev1.Span_SPAN_KIND_INTERNAL:    spans_core.SpanKindInternal,
		tracev1.Span_SPAN_KIND_SERVER:      spans_core.SpanKindServer,
		tracev1.Span_SPAN_KIND_CLIENT:      spans_core.SpanKindClient,
		tracev1.Span_SPAN_KIND_PRODUCER:    spans_core.SpanKindProducer,
		tracev1.Span_SPAN_KIND_CONSUMER:    spans_core.SpanKindConsumer,
	}
	for otelKind, expected := range testCases {
		assert.Equal(t, expected, spanKindFor(otelKind))
	}
}

func Test_ConvertResourceSpans_WithEmptyInput_ReturnsEmpty(t *testing.T) {
	assert.Empty(t, ConvertResourceSpans(uuid.New(), nil))
	assert.Empty(t, ConvertResourceSpans(uuid.New(), []*tracev1.ResourceSpans{}))
	assert.Empty(t, ConvertResourceSpans(uuid.New(), []*tracev1.ResourceSpans{{}}))
}
