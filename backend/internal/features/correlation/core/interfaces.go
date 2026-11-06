package correlation_core

import (
	"time"

	"github.com/google/uuid"
)

// IncidentSessionRepository stores incident sessions in Postgres: they are
// low-volume and relational, unlike the Parquet signal streams.
type IncidentSessionRepository interface {
	// CreateSession persists a session and its signals atomically.
	CreateSession(session *IncidentSession, signals []*IncidentSessionSignal) error
	ListSessions(
		projectID uuid.UUID,
		request *ListSessionsRequestDTO,
	) (*ListSessionsResponseDTO, error)
	// GetSessionDetail re-hydrates each signal from its live stream; missing
	// rows come back with Available: false.
	GetSessionDetail(projectID, sessionID uuid.UUID) (*SessionDetailDTO, error)
	// GetWatermark returns zero time for a never-scanned project; the
	// cold-start lookback cap is the worker's policy, not storage's.
	GetWatermark(projectID uuid.UUID) (time.Time, error)
	SetWatermark(projectID uuid.UUID, t time.Time) error
	TestConnection() error
}
