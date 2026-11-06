package traces_jaeger

import (
	"fmt"

	spans_core "obzly/internal/features/spans/core"
)

// ConvertTrace maps a GetTraceResponseDTO onto a Jaeger Trace, building
// one Process per service for spans to reference by processID.
func ConvertTrace(response *spans_core.GetTraceResponseDTO) Trace {
	processIDs := map[string]string{} // service name -> "p1", "p2", ...
	processes := map[string]Process{}

	processIDFor := func(serviceName string) string {
		if id, ok := processIDs[serviceName]; ok {
			return id
		}
		id := fmt.Sprintf("p%d", len(processIDs)+1)
		processIDs[serviceName] = id
		processes[id] = Process{ServiceName: serviceName}
		return id
	}

	spanList := make([]Span, len(response.Spans))
	for i, span := range response.Spans {
		spanList[i] = convertSpan(span, processIDFor(span.ServiceName))
	}

	return Trace{
		TraceID:   response.TraceID,
		Spans:     spanList,
		Processes: processes,
	}
}

func convertSpan(span spans_core.SpanDTO, processID string) Span {
	var references []Reference
	if span.ParentSpanID != "" {
		references = append(references, Reference{
			RefType: RefTypeChildOf,
			TraceID: span.TraceID,
			SpanID:  span.ParentSpanID,
		})
	}

	tags := keyValuesFor(span.Attributes)
	if span.StatusCode != "" {
		tags = append(tags, KeyValue{Key: "otel.status_code", Type: "string", Value: span.StatusCode})
	}
	if span.StatusMessage != "" {
		tags = append(tags, KeyValue{Key: "otel.status_description", Type: "string", Value: span.StatusMessage})
	}
	if span.Kind != "" {
		tags = append(tags, KeyValue{Key: "span.kind", Type: "string", Value: span.Kind})
	}

	return Span{
		TraceID:       span.TraceID,
		SpanID:        span.SpanID,
		OperationName: span.Name,
		References:    references,
		StartTime: uint64(
			span.StartTime.UnixMicro(),
		), //nolint:gosec // unix microseconds fit uint64 until year 292277
		Duration:  uint64(span.EndTime.Sub(span.StartTime).Microseconds()),
		Tags:      tags,
		Logs:      logsFor(span.Events),
		ProcessID: processID,
	}
}

// logsFor maps span events onto Jaeger span logs. Jaeger has no event-name
// field, so the name leads as an "event" field, as OTel's Jaeger exporter does.
func logsFor(events []spans_core.SpanEventDTO) []Log {
	if len(events) == 0 {
		return nil
	}

	logs := make([]Log, len(events))
	for i, event := range events {
		fields := append(
			[]KeyValue{{Key: "event", Type: "string", Value: event.Name}},
			keyValuesFor(event.Attributes)...)
		logs[i] = Log{
			Timestamp: uint64(event.Timestamp.UnixMicro()), //nolint:gosec // see convertSpan
			Fields:    fields,
		}
	}
	return logs
}

func keyValuesFor(attributes map[string]any) []KeyValue {
	if len(attributes) == 0 {
		return nil
	}

	keyValues := make([]KeyValue, 0, len(attributes))
	for key, value := range attributes {
		keyValues = append(keyValues, keyValueFor(key, value))
	}
	return keyValues
}

func keyValueFor(key string, value any) KeyValue {
	switch typed := value.(type) {
	case string:
		return KeyValue{Key: key, Type: "string", Value: typed}
	case bool:
		return KeyValue{Key: key, Type: "bool", Value: typed}
	case float64:
		return KeyValue{Key: key, Type: "float64", Value: typed}
	case int64:
		return KeyValue{Key: key, Type: "float64", Value: float64(typed)}
	default:
		// Arrays/maps have no Jaeger tag type: stringify rather than drop.
		return KeyValue{Key: key, Type: "string", Value: fmt.Sprintf("%v", typed)}
	}
}
