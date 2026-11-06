package system_selftracing

import (
	"context"

	spans_core "obzly/internal/features/spans/core"
	"obzly/internal/util/attrnames"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// spanExporter writes finished spans into the system project, in the same
// shape the OTLP receiver stores (names normalized by attrnames).
type spanExporter struct {
	projectID uuid.UUID
	store     func([]*spans_core.Span) error
}

func (e *spanExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	if len(spans) == 0 {
		return nil
	}
	converted := make([]*spans_core.Span, 0, len(spans))
	for _, span := range spans {
		converted = append(converted, e.convert(span))
	}
	return e.store(converted)
}

func (e *spanExporter) Shutdown(context.Context) error {
	return nil
}

func (e *spanExporter) convert(span sdktrace.ReadOnlySpan) *spans_core.Span {
	converted := &spans_core.Span{
		ID:                 uuid.New(),
		ProjectID:          e.projectID,
		TraceID:            span.SpanContext().TraceID().String(),
		SpanID:             span.SpanContext().SpanID().String(),
		Name:               span.Name(),
		ServiceName:        ServiceName,
		Kind:               spanKind(span.SpanKind()),
		StartTime:          span.StartTime().UTC(),
		EndTime:            span.EndTime().UTC(),
		StatusCode:         statusCode(span.Status().Code),
		StatusMessage:      span.Status().Description,
		Attributes:         attrnames.NormalizeMap(attributeMap(span.Attributes())),
		ResourceAttributes: attrnames.NormalizeMap(attributeMap(span.Resource().Attributes())),
	}
	if parent := span.Parent(); parent.IsValid() {
		converted.ParentSpanID = parent.SpanID().String()
	}
	for _, event := range span.Events() {
		converted.Events = append(converted.Events, spans_core.SpanEvent{
			Name:       event.Name,
			Timestamp:  event.Time.UTC(),
			Attributes: attrnames.NormalizeMap(attributeMap(event.Attributes)),
		})
	}
	return converted
}

func attributeMap(attributes []attribute.KeyValue) map[string]any {
	values := make(map[string]any, len(attributes))
	for _, kv := range attributes {
		values[string(kv.Key)] = kv.Value.AsInterface()
	}
	return values
}

func spanKind(kind trace.SpanKind) spans_core.SpanKind {
	switch kind {
	case trace.SpanKindServer:
		return spans_core.SpanKindServer
	case trace.SpanKindClient:
		return spans_core.SpanKindClient
	case trace.SpanKindProducer:
		return spans_core.SpanKindProducer
	case trace.SpanKindConsumer:
		return spans_core.SpanKindConsumer
	case trace.SpanKindInternal:
		return spans_core.SpanKindInternal
	default:
		return spans_core.SpanKindUnspecified
	}
}

func statusCode(code codes.Code) spans_core.StatusCode {
	switch code {
	case codes.Ok:
		return spans_core.StatusCodeOK
	case codes.Error:
		return spans_core.StatusCodeError
	default:
		return spans_core.StatusCodeUnset
	}
}
