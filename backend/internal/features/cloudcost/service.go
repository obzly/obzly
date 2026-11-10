package cloudcost

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"obzly/internal/cache"
	projects_services "obzly/internal/features/projects/services"
	users_models "obzly/internal/features/users/models"
	"obzly/internal/util/leaderlock"
)

const maintenanceEvery = time.Hour

type Service struct {
	log *slog.Logger
	now func() time.Time
}

// projectsFor is the projects user can read, narrowed to projectID when one is given.
func (s *Service) projectsFor(user *users_models.User, projectID string) ([]uuid.UUID, error) {
	list, err := projects_services.GetProjectService().GetUserProjects(user)
	if err != nil {
		return nil, err
	}
	var ids []uuid.UUID
	for _, project := range list.Projects {
		if projectID != "" && project.ID.String() != strings.TrimSpace(projectID) {
			continue
		}
		ids = append(ids, project.ID)
	}
	if projectID != "" && len(ids) == 0 {
		return nil, ErrForbidden
	}
	if ids == nil {
		ids = []uuid.UUID{}
	}
	return ids, nil
}

// StartMaintenance drops the chunks of batches that never completed and measures realized
// savings, hourly (leader-locked).
func (s *Service) StartMaintenance(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(maintenanceEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, err := leaderlock.RunExclusive(ctx, cache.GetCache(), "cloud-cost-maintenance", 2*maintenanceEvery,
					func(ctx context.Context) error {
						now := s.now()
						if err := dropStaleChunks(ctx, now); err != nil {
							return err
						}
						if err := dropStaleAdvice(ctx, now); err != nil {
							return err
						}
						return s.realizeSavings(ctx, now)
					})
				if err != nil {
					s.log.Warn("Cloud cost maintenance failed", "error", err)
				}
			}
		}
	}()
}
