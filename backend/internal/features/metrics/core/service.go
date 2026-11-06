package metrics_core

import "github.com/google/uuid"

// MetricCoreService is stateless: it looks up the active repository at call
// time rather than storing it (see logs_core.LogCoreService).
type MetricCoreService struct{}

func (s *MetricCoreService) OnBeforeProjectDeletion(projectID uuid.UUID) error {
	return GetMetricCoreRepository().DeleteMetricsByProject(projectID)
}
