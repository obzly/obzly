package logs_core

import "github.com/google/uuid"

// LogCoreService is stateless: it looks up the active repository at call
// time (see activeRepository).
type LogCoreService struct{}

func (s *LogCoreService) OnBeforeProjectDeletion(projectID uuid.UUID) error {
	for _, repository := range GetAllRepositories() {
		if err := repository.DeleteLogsByProject(projectID); err != nil {
			return err
		}
	}
	return nil
}
