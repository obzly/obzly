package spans_core

import (
	"time"

	"github.com/google/uuid"
)

// Span is the write path's input shape. TraceID/SpanID/ParentSpanID are
// hex strings (W3C text form), as for logs.
type Span struct {
	ID                 uuid.UUID      `json:"id"`
	ProjectID          uuid.UUID      `json:"projectId"`
	TraceID            string         `json:"traceId"`
	SpanID             string         `json:"spanId"`
	ParentSpanID       string         `json:"parentSpanId,omitempty"`
	Name               string         `json:"name"`
	ServiceName        string         `json:"serviceName"`
	Kind               SpanKind       `json:"kind"`
	StartTime          time.Time      `json:"startTime"`
	EndTime            time.Time      `json:"endTime"`
	StatusCode         StatusCode     `json:"statusCode"`
	StatusMessage      string         `json:"statusMessage,omitempty"`
	Attributes         map[string]any `json:"attributes,omitempty"`
	ResourceAttributes map[string]any `json:"resourceAttributes,omitempty"`
	Events             []SpanEvent    `json:"events,omitempty"`
}

type SpanEvent struct {
	Name       string         `json:"name"`
	Timestamp  time.Time      `json:"timestamp"`
	Attributes map[string]any `json:"attributes,omitempty"`
}
