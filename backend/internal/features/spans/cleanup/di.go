package spans_cleanup

import (
	"sync"

	"obzly/internal/cache"
	projects_services "obzly/internal/features/projects/services"
	"obzly/internal/util/logger"
)

var spanCleanupBackgroundService = &SpanCleanupBackgroundService{
	projects_services.GetProjectService(),
	logger.GetLogger(),
	nil,
	nil,
	sync.WaitGroup{},
	cache.GetCache(),
}

func GetSpanCleanupBackgroundService() *SpanCleanupBackgroundService {
	return spanCleanupBackgroundService
}
