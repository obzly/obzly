// Package correlation_repository implements
// correlation_core.IncidentSessionRepository on Postgres. It lives outside
// correlation_core because it imports logs_core/spans_core to re-hydrate
// signals.
package correlation_repository

import (
	"time"

	correlation_core "obzly/internal/features/correlation/core"

	"github.com/google/uuid"
)

// sessionRow and signalRow are the GORM models, so correlation_core's
// domain structs stay free of gorm tags.
type sessionRow struct {
	ID             uuid.UUID `gorm:"column:id"`
	ProjectID      uuid.UUID `gorm:"column:project_id"`
	ServiceName    string    `gorm:"column:service_name"`
	WindowStart    time.Time `gorm:"column:window_start"`
	WindowEnd      time.Time `gorm:"column:window_end"`
	TriggerType    string    `gorm:"column:trigger_type"`
	TriggerSummary string    `gorm:"column:trigger_summary"`
	CreatedAt      time.Time `gorm:"column:created_at"`
}

func (sessionRow) TableName() string {
	return "incident_sessions"
}

func (r sessionRow) toModel() *correlation_core.IncidentSession {
	return &correlation_core.IncidentSession{
		ID:             r.ID,
		ProjectID:      r.ProjectID,
		ServiceName:    r.ServiceName,
		WindowStart:    r.WindowStart,
		WindowEnd:      r.WindowEnd,
		TriggerType:    correlation_core.TriggerType(r.TriggerType),
		TriggerSummary: r.TriggerSummary,
		CreatedAt:      r.CreatedAt,
	}
}

func sessionRowFromModel(s *correlation_core.IncidentSession) sessionRow {
	return sessionRow{
		ID:             s.ID,
		ProjectID:      s.ProjectID,
		ServiceName:    s.ServiceName,
		WindowStart:    s.WindowStart,
		WindowEnd:      s.WindowEnd,
		TriggerType:    string(s.TriggerType),
		TriggerSummary: s.TriggerSummary,
		CreatedAt:      s.CreatedAt,
	}
}

type signalRow struct {
	ID         uuid.UUID                `gorm:"column:id"`
	SessionID  uuid.UUID                `gorm:"column:session_id"`
	SignalType string                   `gorm:"column:signal_type"`
	SignalRef  correlation_core.JSONMap `gorm:"column:signal_ref;type:jsonb"`
	OccurredAt time.Time                `gorm:"column:occurred_at"`
}

func (signalRow) TableName() string {
	return "incident_session_signals"
}

func signalRowFromModel(s *correlation_core.IncidentSessionSignal) signalRow {
	return signalRow{
		ID:         s.ID,
		SessionID:  s.SessionID,
		SignalType: string(s.SignalType),
		SignalRef:  s.SignalRef,
		OccurredAt: s.OccurredAt,
	}
}

// watermarkRow is the worker's per-project scan position.
type watermarkRow struct {
	ProjectID     uuid.UUID `gorm:"column:project_id"`
	LastScannedAt time.Time `gorm:"column:last_scanned_at"`
}

func (watermarkRow) TableName() string {
	return "correlation_watermarks"
}
