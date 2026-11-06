package dashboards_controllers

import (
	dashboards_services "obzly/internal/features/dashboards/services"
)

var dashboardController = &DashboardController{
	dashboards_services.GetDashboardService(),
}

func GetDashboardController() *DashboardController {
	return dashboardController
}
