package spans_core

import "github.com/google/uuid"

// SpanCoreService is stateless: it looks up the active repository at call
// time rather than storing it (see logs_core.LogCoreService).
type SpanCoreService struct{}

func (s *SpanCoreService) OnBeforeProjectDeletion(projectID uuid.UUID) error {
	return GetSpanCoreRepository().DeleteSpansByProject(projectID)
}
