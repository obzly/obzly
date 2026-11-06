package logs_querying

import (
	"obzly/internal/cache"
	projects_services "obzly/internal/features/projects/services"
	"obzly/internal/util/logger"
)

var concurrentQueryLimiter = &ConcurrentQueryLimiter{
	cache.GetCache(),
	logger.GetLogger(),
}

var queryValidator = &QueryValidator{
	logger.GetLogger(),
}

var logQueryService = &LogQueryService{
	projects_services.GetProjectService(),
	concurrentQueryLimiter,
	queryValidator,
	logger.GetLogger(),
}

var logQueryController = &LogQueryController{
	logQueryService,
}

func GetLogQueryService() *LogQueryService {
	return logQueryService
}

func GetLogQueryController() *LogQueryController {
	return logQueryController
}
