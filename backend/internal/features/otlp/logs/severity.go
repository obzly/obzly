package otlp_logs

import (
	logs_core "obzly/internal/features/logs/core"

	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
)

// severityNumberToLogLevel maps OTel SeverityNumber ranges (TRACE 1-4 ...
// FATAL 21-24) to LogLevel. TRACE becomes DEBUG, as for a "TRACE" SeverityText;
// 0 or out of range means INFO, so the log is still kept.
func severityNumberToLogLevel(severity logsv1.SeverityNumber) logs_core.LogLevel {
	switch {
	case severity >= 1 && severity <= 8: // TRACE + DEBUG ranges
		return logs_core.LogLevelDebug
	case severity >= 9 && severity <= 12: // INFO range
		return logs_core.LogLevelInfo
	case severity >= 13 && severity <= 16: // WARN range
		return logs_core.LogLevelWarn
	case severity >= 17 && severity <= 20: // ERROR range
		return logs_core.LogLevelError
	case severity >= 21 && severity <= 24: // FATAL range
		return logs_core.LogLevelFatal
	default:
		return logs_core.LogLevelInfo
	}
}
