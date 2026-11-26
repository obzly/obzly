package servicemap

import (
	"time"

	"obzly/internal/cache"
	projects_services "obzly/internal/features/projects/services"
	"obzly/internal/util/querylimit"
)

var serviceMapService = &ServiceMapService{
	projects_services.GetProjectService(),
	querylimit.New(cache.GetCache(), "servicemap", 4, 30*time.Second),
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
