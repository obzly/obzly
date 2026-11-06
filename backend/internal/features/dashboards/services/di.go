package dashboards_services

import (
	"time"

	"obzly/internal/cache"
	dashboards_repositories "obzly/internal/features/dashboards/repositories"
	projects_services "obzly/internal/features/projects/services"
	"obzly/internal/util/querylimit"
)

var dashboardRepository = &dashboards_repositories.DashboardRepository{}
var folderRepository = &dashboards_repositories.FolderRepository{}

// Panels of one dashboard load at once, so more slots than a log search
// gets, and a wait for one instead of a refusal.
var dashboardService = &DashboardService{
	dashboardRepository,
	folderRepository,
	projects_services.GetProjectService(),
	querylimit.New(cache.GetCache(), "panel", 12, 30*time.Second),
}

func GetDashboardService() *DashboardService {
	return dashboardService
}

// SetupDependencies registers DashboardService as a project-deletion listener.
func SetupDependencies() {
	projects_services.GetProjectService().AddProjectDeletionListener(dashboardService)
}
