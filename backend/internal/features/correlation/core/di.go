package correlation_core

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// activeRepository is set at startup; read it lazily, never during
// package-level var init (it is still nil then).
var activeRepository IncidentSessionRepository

func SetRepository(repo IncidentSessionRepository) {
	activeRepository = repo
}

func GetIncidentSessionRepository() IncidentSessionRepository {
	if activeRepository == nil {
		panic(
			"correlation_core: IncidentSessionRepository accessed before SetRepository was called",
		)
	}
	return activeRepository
}

func GetUnavailableIncidentSessionRepository() IncidentSessionRepository {
	return unavailableRepository{}
}

var errRepositoryUnavailable = errors.New("correlation storage is unavailable")

type unavailableRepository struct{}

func (unavailableRepository) CreateSession(*IncidentSession, []*IncidentSessionSignal) error {
	return errRepositoryUnavailable
}

func (unavailableRepository) ListSessions(
	uuid.UUID,
	*ListSessionsRequestDTO,
) (*ListSessionsResponseDTO, error) {
	return nil, errRepositoryUnavailable
}

func (unavailableRepository) GetSessionDetail(uuid.UUID, uuid.UUID) (*SessionDetailDTO, error) {
	return nil, errRepositoryUnavailable
}

func (unavailableRepository) GetWatermark(uuid.UUID) (time.Time, error) {
	return time.Time{}, errRepositoryUnavailable
}

func (unavailableRepository) SetWatermark(uuid.UUID, time.Time) error {
	return errRepositoryUnavailable
}

func (unavailableRepository) TestConnection() error {
	return errRepositoryUnavailable
}
