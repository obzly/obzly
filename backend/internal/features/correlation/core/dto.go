package correlation_core

import "time"

// TimeRangeDTO is duplicated per signal package rather than imported, so
// feature packages don't depend on each other for a {from,to} struct.
type TimeRangeDTO struct {
	From *time.Time `json:"from,omitempty"`
	To   *time.Time `json:"to,omitempty"`
}

type ListSessionsRequestDTO struct {
	TimeRange *TimeRangeDTO `json:"timeRange,omitempty"`
	Limit     int           `json:"limit,omitempty"`
}

// SessionSummaryDTO is IncidentSession's API shape.
type SessionSummaryDTO struct {
	ID             string    `json:"id"`
	ProjectID      string    `json:"projectId"`
	ServiceName    string    `json:"serviceName"`
	WindowStart    time.Time `json:"windowStart"`
	WindowEnd      time.Time `json:"windowEnd"`
	TriggerType    string    `json:"triggerType"`
	TriggerSummary string    `json:"triggerSummary,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
}

type ListSessionsResponseDTO struct {
	Sessions []SessionSummaryDTO `json:"sessions"`
}

// LogSignalContentDTO is a log signal's content, read live from the logs stream.
type LogSignalContentDTO struct {
	Level     string    `json:"level"`
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
}

type SpanSignalContentDTO struct {
	Name        string    `json:"name"`
	ServiceName string    `json:"serviceName"`
	StatusCode  string    `json:"statusCode"`
	TraceID     string    `json:"traceId"`
	SpanID      string    `json:"spanId"`
	StartTime   time.Time `json:"startTime"`
}

// SignalDetailDTO is one re-hydrated signal. Available is false when its row
// is gone (e.g. retention), so one missing row doesn't fail the response.
type SignalDetailDTO struct {
	SignalType SignalType `json:"signalType"`
	OccurredAt time.Time  `json:"occurredAt"`
	Available  bool       `json:"available"`

	Log  *LogSignalContentDTO  `json:"log,omitempty"`
	Span *SpanSignalContentDTO `json:"span,omitempty"`
}

// SessionDetailDTO is a session with its re-hydrated signals.
type SessionDetailDTO struct {
	Session SessionSummaryDTO `json:"session"`
	Signals []SignalDetailDTO `json:"signals"`
}

func (s *IncidentSession) ToSummaryDTO() SessionSummaryDTO {
	return SessionSummaryDTO{
		ID:             s.ID.String(),
		ProjectID:      s.ProjectID.String(),
		ServiceName:    s.ServiceName,
		WindowStart:    s.WindowStart,
		WindowEnd:      s.WindowEnd,
		TriggerType:    string(s.TriggerType),
		TriggerSummary: s.TriggerSummary,
		CreatedAt:      s.CreatedAt,
	}
}

// ServiceImpactDTO is one service's error signals in a session's window,
// counted live from the logs and spans streams.
type ServiceImpactDTO struct {
	ServiceName string `json:"serviceName"`
	ErrorLogs   int64  `json:"errorLogs"`
	ErrorSpans  int64  `json:"errorSpans"`
	Total       int64  `json:"total"`
}

// SessionImpactDTO ranks a session's services by error signals (most
// first) within [WindowStart, WindowEnd].
type SessionImpactDTO struct {
	SessionID   string             `json:"sessionId"`
	WindowStart time.Time          `json:"windowStart"`
	WindowEnd   time.Time          `json:"windowEnd"`
	Services    []ServiceImpactDTO `json:"services"`
}
