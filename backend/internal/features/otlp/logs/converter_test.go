package otlp_logs

import (
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
)

func stringValue(value string) *commonv1.AnyValue {
	return &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: value}}
}

func Test_ConvertResourceLogs_WithFullRecord_ProducesExpectedItem(t *testing.T) {
	traceID := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10}
	spanID := []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x11, 0x22}
	timestamp := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)

	resourceLogs := []*logsv1.ResourceLogs{
		{
			Resource: &resourcev1.Resource{
				Attributes: []*commonv1.KeyValue{
					{Key: "service.name", Value: stringValue("checkout")},
				},
			},
			ScopeLogs: []*logsv1.ScopeLogs{
				{
					Scope: &commonv1.InstrumentationScope{Name: "my-lib", Version: "1.2.3"},
					LogRecords: []*logsv1.LogRecord{
						{
							TimeUnixNano:   uint64(timestamp.UnixNano()),
							SeverityNumber: logsv1.SeverityNumber_SEVERITY_NUMBER_WARN,
							Body:           stringValue("something happened"),
							Attributes: []*commonv1.KeyValue{
								{
									Key:   "http.status_code",
									Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_IntValue{IntValue: 500}},
								},
							},
							TraceId: traceID,
							SpanId:  spanID,
						},
					},
				},
			},
		},
	}

	items := ConvertResourceLogs(resourceLogs)
	require.Len(t, items, 1)

	item := items[0]
	assert.Equal(t, logs_core.LogLevelWarn, item.Level)
	assert.Equal(t, "something happened", item.Message)
	assert.Equal(t, "0102030405060708090a0b0c0d0e0f10", item.TraceID)
	assert.Equal(t, "aabbccddeeff1122", item.SpanID)
	// Names follow attrnames: "resource." dropped, dots become underscores.
	assert.Equal(t, "checkout", item.Fields["service_name"])
	assert.Equal(t, "my-lib", item.Fields["scope_name"])
	assert.Equal(t, "1.2.3", item.Fields["scope_version"])
	assert.Equal(t, int64(500), item.Fields["http_status_code"])

	parsedTimestamp, err := time.Parse(time.RFC3339Nano, item.Timestamp.(string))
	require.NoError(t, err)
	assert.True(t, timestamp.Equal(parsedTimestamp))
}

func Test_ConvertResourceLogs_WithSeverityText_OverridesSeverityNumber(t *testing.T) {
	resourceLogs := []*logsv1.ResourceLogs{
		{
			ScopeLogs: []*logsv1.ScopeLogs{
				{
					LogRecords: []*logsv1.LogRecord{
						{
							SeverityNumber: logsv1.SeverityNumber_SEVERITY_NUMBER_INFO,
							SeverityText:   "warning",
							Body:           stringValue("custom severity text"),
						},
					},
				},
			},
		},
	}

	items := ConvertResourceLogs(resourceLogs)
	require.Len(t, items, 1)
	// The raw SeverityText is passed through as-is; logs_receiving's
	// normalizeLogLevel (not exercised here) is what maps "warning" to warn.
	assert.Equal(t, logs_core.LogLevel("warning"), items[0].Level)
}

func Test_ConvertResourceLogs_WithNoTimestamp_FallsBackToObservedTime(t *testing.T) {
	observed := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)

	resourceLogs := []*logsv1.ResourceLogs{
		{
			ScopeLogs: []*logsv1.ScopeLogs{
				{
					LogRecords: []*logsv1.LogRecord{
						{
							ObservedTimeUnixNano: uint64(observed.UnixNano()),
							Body:                 stringValue("fallback timestamp"),
						},
					},
				},
			},
		},
	}

	items := ConvertResourceLogs(resourceLogs)
	require.Len(t, items, 1)

	parsedTimestamp, err := time.Parse(time.RFC3339Nano, items[0].Timestamp.(string))
	require.NoError(t, err)
	assert.True(t, observed.Equal(parsedTimestamp))
}

func Test_ConvertResourceLogs_WithNoTraceOrSpanID_LeavesThemEmpty(t *testing.T) {
	resourceLogs := []*logsv1.ResourceLogs{
		{
			ScopeLogs: []*logsv1.ScopeLogs{
				{
					LogRecords: []*logsv1.LogRecord{
						{Body: stringValue("no trace context")},
					},
				},
			},
		},
	}

	items := ConvertResourceLogs(resourceLogs)
	require.Len(t, items, 1)
	assert.Empty(t, items[0].TraceID)
	assert.Empty(t, items[0].SpanID)
}

func Test_ConvertResourceLogs_WithNonStringBody_StringifiesIt(t *testing.T) {
	resourceLogs := []*logsv1.ResourceLogs{
		{
			ScopeLogs: []*logsv1.ScopeLogs{
				{
					LogRecords: []*logsv1.LogRecord{
						{Body: &commonv1.AnyValue{Value: &commonv1.AnyValue_IntValue{IntValue: 42}}},
					},
				},
			},
		},
	}

	items := ConvertResourceLogs(resourceLogs)
	require.Len(t, items, 1)
	assert.Equal(t, "42", items[0].Message)
}

func Test_ConvertResourceLogs_WithEmptyInput_ReturnsEmpty(t *testing.T) {
	assert.Empty(t, ConvertResourceLogs(nil))
	assert.Empty(t, ConvertResourceLogs([]*logsv1.ResourceLogs{}))
	assert.Empty(t, ConvertResourceLogs([]*logsv1.ResourceLogs{{}}))
}

func Test_SeverityNumberToLogLevel_CoversEveryOTelRange(t *testing.T) {
	testCases := []struct {
		severity logsv1.SeverityNumber
		expected logs_core.LogLevel
	}{
		{0, logs_core.LogLevelInfo}, // unspecified
		{1, logs_core.LogLevelDebug},
		{4, logs_core.LogLevelDebug},
		{5, logs_core.LogLevelDebug},
		{8, logs_core.LogLevelDebug},
		{9, logs_core.LogLevelInfo},
		{12, logs_core.LogLevelInfo},
		{13, logs_core.LogLevelWarn},
		{16, logs_core.LogLevelWarn},
		{17, logs_core.LogLevelError},
		{20, logs_core.LogLevelError},
		{21, logs_core.LogLevelFatal},
		{24, logs_core.LogLevelFatal},
		{99, logs_core.LogLevelInfo}, // out of range
	}

	for _, tc := range testCases {
		assert.Equal(t, tc.expected, severityNumberToLogLevel(tc.severity), "severity=%d", tc.severity)
	}
}

func Test_AnyValueToGo_HandlesNestedKvlistAndArray(t *testing.T) {
	value := &commonv1.AnyValue{
		Value: &commonv1.AnyValue_KvlistValue{
			KvlistValue: &commonv1.KeyValueList{
				Values: []*commonv1.KeyValue{
					{Key: "nested", Value: stringValue("value")},
					{
						Key: "list",
						Value: &commonv1.AnyValue{
							Value: &commonv1.AnyValue_ArrayValue{
								ArrayValue: &commonv1.ArrayValue{
									Values: []*commonv1.AnyValue{stringValue("a"), stringValue("b")},
								},
							},
						},
					},
				},
			},
		},
	}

	result := anyValueToGo(value)
	asMap, ok := result.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "value", asMap["nested"])
	assert.Equal(t, []any{"a", "b"}, asMap["list"])
}
