package servicemap

import (
	projects_services "obzly/internal/features/projects/services"
)

var serviceMapService = &ServiceMapService{
	projects_services.GetProjectService(),
}

var serviceMapController = &ServiceMapController{
	serviceMapService,
}

func GetServiceMapService() *ServiceMapService {
	return serviceMapService
}

func GetServiceMapController() *ServiceMapController {
	return serviceMapController
}
