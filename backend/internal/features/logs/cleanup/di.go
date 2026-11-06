package logs_cleanup

import (
	"sync"

	"obzly/internal/cache"
	projects_services "obzly/internal/features/projects/services"
	"obzly/internal/util/logger"
)

var logCleanupBackgroundService = &LogCleanupBackgroundService{
	projects_services.GetProjectService(),
	logger.GetLogger(),
	nil,
	nil,
	sync.WaitGroup{},
	cache.GetCache(),
}

func GetLogCleanupBackgroundService() *LogCleanupBackgroundService {
	return logCleanupBackgroundService
}
