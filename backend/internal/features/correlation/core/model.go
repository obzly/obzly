// Package correlation_core is the Postgres model for incident sessions:
// co-occurring anomaly signals (ERROR logs, non-OK spans, log/span pairs
// sharing a trace_id) grouped by service and time window. The correlation
// is dimensional and temporal, not semantic/ML; see correlation_worker.
package correlation_core

import (
	"time"

	"github.com/google/uuid"
)

// TriggerType is why a session was created. MetricThreshold is reserved in
// the schema; the worker does not produce it yet.
type TriggerType string

const (
	TriggerTypeErrorLog        TriggerType = "error_log"
	TriggerTypeSpanErrorStatus TriggerType = "span_error_status"
	TriggerTypeMetricThreshold TriggerType = "metric_threshold"
	TriggerTypeTraceIDLink     TriggerType = "trace_id_link"
)

// SignalType is the stream a signal came from (Metric is reserved, unused).
type SignalType string

const (
	SignalTypeLog    SignalType = "log"
	SignalTypeSpan   SignalType = "span"
	SignalTypeMetric SignalType = "metric"
)

// IncidentSession is a persisted group of co-occurring signals with at least
// one anomaly.
type IncidentSession struct {
	ID             uuid.UUID
	ProjectID      uuid.UUID
	ServiceName    string
	WindowStart    time.Time
	WindowEnd      time.Time
	TriggerType    TriggerType
	TriggerSummary string
	CreatedAt      time.Time
}

// IncidentSessionSignal is one signal of a session. SignalRef points at the
// row in its own stream (re-queried on read, never copied).
type IncidentSessionSignal struct {
	ID         uuid.UUID
	SessionID  uuid.UUID
	SignalType SignalType
	SignalRef  JSONMap
	OccurredAt time.Time
}

// SignalRefLog builds a log SignalRef; the worker and repository share these
// builders so write and read paths can't drift apart.
func SignalRefLog(logID string) JSONMap {
	return JSONMap{"logId": logID}
}

func SignalRefSpan(traceID, spanID string) JSONMap {
	return JSONMap{"traceId": traceID, "spanId": spanID}
}
