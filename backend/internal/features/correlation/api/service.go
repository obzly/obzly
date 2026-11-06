// Package correlation_api is the read-only API over incident sessions; only
// correlation_worker creates them.
package correlation_api

import (
	"errors"
	"fmt"
	"time"

	correlation_core "obzly/internal/features/correlation/core"
	projects_services "obzly/internal/features/projects/services"
	users_models "obzly/internal/features/users/models"

	"github.com/google/uuid"
)

// maxSessionListLookback keeps a request without a range from reading every
// session ever created; generous, since sessions are low-volume rows.
const maxSessionListLookback = 30 * 24 * time.Hour

type CorrelationService struct {
	projectService *projects_services.ProjectService
}

func (s *CorrelationService) ListSessions(
	projectID uuid.UUID,
	from, to *time.Time,
	limit int,
	user *users_models.User,
) (*correlation_core.ListSessionsResponseDTO, error) {
	if err := s.requireAccess(projectID, user); err != nil {
		return nil, err
	}

	resolvedFrom, resolvedTo := clampTimeRange(from, to)

	response, err := correlation_core.GetIncidentSessionRepository().
		ListSessions(projectID, &correlation_core.ListSessionsRequestDTO{
			TimeRange: &correlation_core.TimeRangeDTO{From: &resolvedFrom, To: &resolvedTo},
			Limit:     limit,
		})
	if err != nil {
		return nil, fmt.Errorf("failed to list incident sessions: %w", err)
	}
	return response, nil
}

func (s *CorrelationService) GetSessionDetail(
	projectID, sessionID uuid.UUID,
	user *users_models.User,
) (*correlation_core.SessionDetailDTO, error) {
	if err := s.requireAccess(projectID, user); err != nil {
		return nil, err
	}

	detail, err := correlation_core.GetIncidentSessionRepository().
		GetSessionDetail(projectID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to load incident session detail: %w", err)
	}
	return detail, nil
}

func (s *CorrelationService) requireAccess(projectID uuid.UUID, user *users_models.User) error {
	canAccess, _, err := s.projectService.CanUserAccessProject(projectID, user)
	if err != nil {
		return fmt.Errorf("failed to verify project access: %w", err)
	}
	if !canAccess {
		return errors.New("insufficient permissions to query project correlation sessions")
	}
	return nil
}

// clampTimeRange is servicemap.clampTimeRange with maxSessionListLookback.
func clampTimeRange(from, to *time.Time) (time.Time, time.Time) {
	resolvedTo := time.Now().UTC()
	if to != nil {
		resolvedTo = *to
	}

	earliestAllowedFrom := resolvedTo.Add(-maxSessionListLookback)
	resolvedFrom := earliestAllowedFrom
	if from != nil && from.After(earliestAllowedFrom) {
		resolvedFrom = *from
	}

	return resolvedFrom, resolvedTo
}
