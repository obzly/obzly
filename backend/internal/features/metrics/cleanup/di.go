package metrics_cleanup

import (
	"sync"

	"obzly/internal/cache"
	projects_services "obzly/internal/features/projects/services"
	"obzly/internal/util/logger"
)

var metricCleanupBackgroundService = &MetricCleanupBackgroundService{
	projects_services.GetProjectService(),
	logger.GetLogger(),
	nil,
	nil,
	sync.WaitGroup{},
	cache.GetCache(),
}

func GetMetricCleanupBackgroundService() *MetricCleanupBackgroundService {
	return metricCleanupBackgroundService
}
