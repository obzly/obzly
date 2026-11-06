package correlation_worker

import (
	"sync"

	"obzly/internal/cache"
	projects_services "obzly/internal/features/projects/services"
	"obzly/internal/util/logger"
)

var correlationBackgroundService = &CorrelationBackgroundService{
	projects_services.GetProjectService(),
	logger.GetLogger(),
	nil,
	nil,
	sync.WaitGroup{},
	cache.GetCache(),
}

func GetCorrelationBackgroundService() *CorrelationBackgroundService {
	return correlationBackgroundService
}
