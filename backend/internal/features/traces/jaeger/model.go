// Package traces_jaeger implements the Jaeger HTTP query API over
// spans_core.SpanRepository, so Grafana's Jaeger datasource works as is.
// Built from Jaeger's documented JSON wire format, not its source code.
//
// Jaeger's API has no tenancy, so it is mounted per project
// (/api/v1/jaeger/:projectId/api/...) and requires that project's API key.
package traces_jaeger

// Response is the envelope every Jaeger API endpoint returns.
type Response struct {
	Data   any      `json:"data"`
	Total  int      `json:"total"`
	Limit  int      `json:"limit"`
	Offset int      `json:"offset"`
	Errors []string `json:"errors"`
}

type Trace struct {
	TraceID   string             `json:"traceID"`
	Spans     []Span             `json:"spans"`
	Processes map[string]Process `json:"processes"`
	Warnings  []string           `json:"warnings,omitempty"`
}

type Span struct {
	TraceID       string      `json:"traceID"`
	SpanID        string      `json:"spanID"`
	OperationName string      `json:"operationName"`
	References    []Reference `json:"references"`
	StartTime     uint64      `json:"startTime"` // microseconds since epoch
	Duration      uint64      `json:"duration"`  // microseconds
	Tags          []KeyValue  `json:"tags"`
	Logs          []Log       `json:"logs"`
	ProcessID     string      `json:"processID"`
}

const RefTypeChildOf = "CHILD_OF"

type Reference struct {
	RefType string `json:"refType"`
	TraceID string `json:"traceID"`
	SpanID  string `json:"spanID"`
}

type KeyValue struct {
	Key   string `json:"key"`
	Type  string `json:"type"` // "string" | "bool" | "float64"
	Value any    `json:"value"`
}

type Log struct {
	Timestamp uint64     `json:"timestamp"` // microseconds since epoch
	Fields    []KeyValue `json:"fields"`
}

type Process struct {
	ServiceName string     `json:"serviceName"`
	Tags        []KeyValue `json:"tags,omitempty"`
}
