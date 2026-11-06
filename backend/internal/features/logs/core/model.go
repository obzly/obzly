package logs_core

import (
	"time"

	"github.com/google/uuid"
)

type LogItem struct {
	ID        uuid.UUID      `json:"id"`
	ProjectID uuid.UUID      `json:"projectId"`
	Timestamp time.Time      `json:"timestamp"`
	Level     LogLevel       `json:"level"`
	Message   string         `json:"message"`
	Fields    map[string]any `json:"fields,omitempty"`
	ClientIP  string         `json:"clientIp,omitempty"`
	// TraceID/SpanID: hex-encoded W3C trace/span IDs; empty without trace context.
	TraceID string `json:"traceId,omitempty"`
	SpanID  string `json:"spanId,omitempty"`
}

// ToDTO converts a just-ingested LogItem into the LogItemDTO a stored search
// returns (Live Tail streams logs before they reach Parquet). Fields are
// stringified as the write path does, so live and stored rows look the same.
func (l *LogItem) ToDTO() LogItemDTO {
	var fields map[string]any
	if len(l.Fields) > 0 || l.ClientIP != "" {
		fields = make(map[string]any, len(l.Fields)+1)
		// client_ip goes into Fields too, as in a stored row: the results table,
		// field sidebar and CSV export read it from there.
		if l.ClientIP != "" {
			fields["client_ip"] = l.ClientIP
		}
		for key, value := range l.Fields {
			fields[key] = StringifyFieldValue(value)
		}
	}

	return LogItemDTO{
		ID:        l.ID.String(),
		Timestamp: l.Timestamp,
		Level:     string(l.Level),
		Message:   l.Message,
		Fields:    fields,
		ClientIP:  l.ClientIP,
		CreatedAt: l.Timestamp,
		TraceID:   l.TraceID,
		SpanID:    l.SpanID,
		// Same as a stored search's row (logs_querier.scanLogItem).
		PatternHash: PatternHash(l.Message),
	}
}
