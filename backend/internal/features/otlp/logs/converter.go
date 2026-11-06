// Package otlp_logs is the OTLP logs receiver (HTTP POST /v1/logs and gRPC).
// It converts OTLP into logs_receiving requests, so OTLP gets the same
// validation, rate limits and quotas as the REST API.
package otlp_logs

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	logs_core "obzly/internal/features/logs/core"
	logs_receiving "obzly/internal/features/logs/receiving"
	"obzly/internal/util/attrnames"

	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
)

// ConvertResourceLogs flattens ResourceLogs into log items. Resource and scope
// attributes are merged into each record's fields with "resource."/"scope."
// prefixes; record attributes stay unprefixed.
func ConvertResourceLogs(resourceLogsList []*logsv1.ResourceLogs) []logs_receiving.LogItemRequestDTO {
	var items []logs_receiving.LogItemRequestDTO

	for _, resourceLogs := range resourceLogsList {
		resourceFields := map[string]any{}
		if resource := resourceLogs.GetResource(); resource != nil {
			addAttributes(resourceFields, "resource.", resource.GetAttributes())
		}

		for _, scopeLogs := range resourceLogs.GetScopeLogs() {
			scopeFields := map[string]any{}
			for key, value := range resourceFields {
				scopeFields[key] = value
			}

			if scope := scopeLogs.GetScope(); scope != nil {
				if scope.GetName() != "" {
					scopeFields["scope.name"] = scope.GetName()
				}
				if scope.GetVersion() != "" {
					scopeFields["scope.version"] = scope.GetVersion()
				}
				addAttributes(scopeFields, "scope.", scope.GetAttributes())
			}

			for _, logRecord := range scopeLogs.GetLogRecords() {
				items = append(items, convertLogRecord(logRecord, scopeFields))
			}
		}
	}

	return items
}

func convertLogRecord(logRecord *logsv1.LogRecord, inheritedFields map[string]any) logs_receiving.LogItemRequestDTO {
	fields := make(map[string]any, len(inheritedFields)+len(logRecord.GetAttributes()))
	for key, value := range inheritedFields {
		fields[key] = value
	}
	addAttributes(fields, "", logRecord.GetAttributes())
	fields = attrnames.NormalizeMap(fields)

	return logs_receiving.LogItemRequestDTO{
		Level:     logLevelFor(logRecord),
		Message:   messageFor(logRecord),
		Timestamp: timestampFor(logRecord),
		Fields:    fields,
		TraceID:   hexEncodeID(logRecord.GetTraceId()),
		SpanID:    hexEncodeID(logRecord.GetSpanId()),
	}
}

// logLevelFor prefers SeverityText (normalized later, like a REST "level") and
// falls back to SeverityNumber.
func logLevelFor(logRecord *logsv1.LogRecord) logs_core.LogLevel {
	if text := strings.TrimSpace(logRecord.GetSeverityText()); text != "" {
		return logs_core.LogLevel(strings.ToLower(text))
	}
	return severityNumberToLogLevel(logRecord.GetSeverityNumber())
}

// messageFor uses a string Body as-is and stringifies any other kind, so a
// structured body still gives a readable message.
func messageFor(logRecord *logsv1.LogRecord) string {
	body := logRecord.GetBody()
	if body == nil {
		return ""
	}
	if _, ok := body.GetValue().(*commonv1.AnyValue_StringValue); ok {
		return body.GetStringValue()
	}
	return stringifyAnyValue(anyValueToGo(body))
}

// timestampFor prefers TimeUnixNano, then ObservedTimeUnixNano (per the OTel
// spec), as RFC3339Nano: ParseTimestamp would misread raw nanoseconds as
// milliseconds.
func timestampFor(logRecord *logsv1.LogRecord) string {
	nanos := logRecord.GetTimeUnixNano()
	if nanos == 0 {
		nanos = logRecord.GetObservedTimeUnixNano()
	}
	if nanos == 0 {
		return ""
	}
	return time.Unix(0, int64(nanos)).UTC().Format(time.RFC3339Nano)
}

// hexEncodeID gives the W3C text form of a trace/span ID; absent IDs stay "".
func hexEncodeID(id []byte) string {
	if len(id) == 0 {
		return ""
	}
	return hex.EncodeToString(id)
}

func addAttributes(dest map[string]any, prefix string, attributes []*commonv1.KeyValue) {
	for _, attribute := range attributes {
		if attribute.GetKey() == "" {
			continue
		}
		dest[prefix+attribute.GetKey()] = anyValueToGo(attribute.GetValue())
	}
}

// anyValueToGo converts an AnyValue to the Go value a JSON `fields` would
// decode to; bytes are hex-encoded since they aren't JSON-safe.
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
		return nil // unspecified/empty AnyValue
	}
}

func stringifyAnyValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", typed)
	}
}
