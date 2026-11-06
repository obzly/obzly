package correlation_api

import (
	projects_services "obzly/internal/features/projects/services"
)

var correlationService = &CorrelationService{
	projects_services.GetProjectService(),
}

var correlationController = &CorrelationController{
	correlationService,
}

func GetCorrelationService() *CorrelationService {
	return correlationService
}

func GetCorrelationController() *CorrelationController {
	return correlationController
}
