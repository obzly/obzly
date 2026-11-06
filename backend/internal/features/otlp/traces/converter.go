// Package otlp_traces is the OTLP traces receiver (HTTP POST /v1/traces and
// gRPC). OTLP is the only trace ingestion path, so it converts straight into
// spans_core.Span and stores batches directly.
package otlp_traces

import (
	"encoding/hex"
	"time"

	spans_core "obzly/internal/features/spans/core"
	"obzly/internal/util/attrnames"

	"github.com/google/uuid"

	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
)

const unknownServiceName = "unknown_service"

// ConvertResourceSpans flattens OTLP ResourceSpans into one span each, with
// resource and scope context on every row.
func ConvertResourceSpans(projectID uuid.UUID, resourceSpansList []*tracev1.ResourceSpans) []*spans_core.Span {
	var result []*spans_core.Span

	for _, resourceSpans := range resourceSpansList {
		resourceAttributes := map[string]any{}
		serviceName := unknownServiceName
		if resource := resourceSpans.GetResource(); resource != nil {
			for _, attribute := range resource.GetAttributes() {
				value := anyValueToGo(attribute.GetValue())
				resourceAttributes[attribute.GetKey()] = value
				if attribute.GetKey() == "service.name" {
					if s, ok := value.(string); ok && s != "" {
						serviceName = s
					}
				}
			}
		}

		for _, scopeSpans := range resourceSpans.GetScopeSpans() {
			scopeAttributes := map[string]any{}
			if scope := scopeSpans.GetScope(); scope != nil {
				if scope.GetName() != "" {
					scopeAttributes["scope.name"] = scope.GetName()
				}
				if scope.GetVersion() != "" {
					scopeAttributes["scope.version"] = scope.GetVersion()
				}
			}

			for _, span := range scopeSpans.GetSpans() {
				result = append(result, convertSpan(projectID, span, serviceName, resourceAttributes, scopeAttributes))
			}
		}
	}

	return result
}

func convertSpan(
	projectID uuid.UUID,
	span *tracev1.Span,
	serviceName string,
	resourceAttributes, scopeAttributes map[string]any,
) *spans_core.Span {
	attributes := make(map[string]any, len(span.GetAttributes())+len(scopeAttributes))
	for key, value := range scopeAttributes {
		attributes[key] = value
	}
	for _, attribute := range span.GetAttributes() {
		attributes[attribute.GetKey()] = anyValueToGo(attribute.GetValue())
	}
	attributes = attrnames.NormalizeMap(attributes)

	return &spans_core.Span{
		ID:                 uuid.New(),
		ProjectID:          projectID,
		TraceID:            hexEncode(span.GetTraceId()),
		SpanID:             hexEncode(span.GetSpanId()),
		ParentSpanID:       hexEncode(span.GetParentSpanId()),
		Name:               span.GetName(),
		ServiceName:        serviceName,
		Kind:               spanKindFor(span.GetKind()),
		StartTime:          timeFromUnixNano(span.GetStartTimeUnixNano()),
		EndTime:            timeFromUnixNano(span.GetEndTimeUnixNano()),
		StatusCode:         statusCodeFor(span.GetStatus()),
		StatusMessage:      span.GetStatus().GetMessage(),
		Attributes:         attributes,
		ResourceAttributes: attrnames.NormalizeMap(resourceAttributes),
		Events:             eventsFor(span.GetEvents()),
	}
}

func eventsFor(otlpEvents []*tracev1.Span_Event) []spans_core.SpanEvent {
	if len(otlpEvents) == 0 {
		return nil
	}

	events := make([]spans_core.SpanEvent, len(otlpEvents))
	for i, event := range otlpEvents {
		attributes := map[string]any{}
		for _, attribute := range event.GetAttributes() {
			attributes[attribute.GetKey()] = anyValueToGo(attribute.GetValue())
		}
		events[i] = spans_core.SpanEvent{
			Name:       event.GetName(),
			Timestamp:  timeFromUnixNano(event.GetTimeUnixNano()),
			Attributes: attrnames.NormalizeMap(attributes),
		}
	}
	return events
}

func spanKindFor(kind tracev1.Span_SpanKind) spans_core.SpanKind {
	switch kind {
	case tracev1.Span_SPAN_KIND_INTERNAL:
		return spans_core.SpanKindInternal
	case tracev1.Span_SPAN_KIND_SERVER:
		return spans_core.SpanKindServer
	case tracev1.Span_SPAN_KIND_CLIENT:
		return spans_core.SpanKindClient
	case tracev1.Span_SPAN_KIND_PRODUCER:
		return spans_core.SpanKindProducer
	case tracev1.Span_SPAN_KIND_CONSUMER:
		return spans_core.SpanKindConsumer
	default:
		return spans_core.SpanKindUnspecified
	}
}

func statusCodeFor(status *tracev1.Status) spans_core.StatusCode {
	if status == nil {
		return spans_core.StatusCodeUnset
	}
	switch status.GetCode() {
	case tracev1.Status_STATUS_CODE_OK:
		return spans_core.StatusCodeOK
	case tracev1.Status_STATUS_CODE_ERROR:
		return spans_core.StatusCodeError
	default:
		return spans_core.StatusCodeUnset
	}
}

func timeFromUnixNano(nanos uint64) time.Time {
	return time.Unix(0, int64(nanos)).UTC()
}

// hexEncode gives the W3C text form of a trace/span ID; absent IDs stay "".
func hexEncode(id []byte) string {
	if len(id) == 0 {
		return ""
	}
	return hex.EncodeToString(id)
}

// anyValueToGo matches otlp_logs.anyValueToGo, which isn't exported.
func anyValueToGo(value *commonv1.AnyValue) any {
	if value == nil {
		return nil
	}

	switch value.GetValue().(type) {
	case *commonv1.AnyValue_StringValue:
		return value.GetStringValue()
	case *commonv1.AnyValue_BoolValue:
		return value.GetBoolValue()
	case *commonv1.AnyValue_IntValue:
		return value.GetIntValue()
	case *commonv1.AnyValue_DoubleValue:
		return value.GetDoubleValue()
	case *commonv1.AnyValue_BytesValue:
		return hex.EncodeToString(value.GetBytesValue())
	case *commonv1.AnyValue_ArrayValue:
		array := value.GetArrayValue().GetValues()
		result := make([]any, len(array))
		for i, item := range array {
			result[i] = anyValueToGo(item)
		}
		return result
	case *commonv1.AnyValue_KvlistValue:
		kvList := value.GetKvlistValue().GetValues()
		result := make(map[string]any, len(kvList))
		for _, kv := range kvList {
			result[kv.GetKey()] = anyValueToGo(kv.GetValue())
		}
		return result
	default:
		return nil
	}
}
