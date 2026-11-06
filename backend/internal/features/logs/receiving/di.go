package logs_receiving

import (
	api_keys "obzly/internal/features/api_keys"
	logs_livetail "obzly/internal/features/logs/livetail"
	projects_services "obzly/internal/features/projects/services"
	"obzly/internal/util/logger"
	rate_limit "obzly/internal/util/rate_limit"
)

var rateLimiter = rate_limit.NewRateLimiter()

var logReceivingService = &LogReceivingService{
	rateLimiter,
	projects_services.GetProjectService(),
	api_keys.GetApiKeyService(),
	logs_livetail.GetPublisher(),
	logger.GetLogger(),
}

var receivingController = &ReceivingController{
	logReceivingService,
}

func GetLogReceivingService() *LogReceivingService {
	return logReceivingService
}

func GetReceivingController() *ReceivingController {
	return receivingController
}
