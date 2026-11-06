package logs_livetail

import (
	"obzly/internal/cache"
	projects_services "obzly/internal/features/projects/services"
	"obzly/internal/util/logger"
)

var publisher = NewPublisher(cache.GetCache(), logger.GetLogger())

var service = NewService(cache.GetCache(), projects_services.GetProjectService(), logger.GetLogger())

var concurrentTailLimiter = NewConcurrentTailLimiter(cache.GetCache(), logger.GetLogger())

var controller = NewController(service, concurrentTailLimiter)

func GetPublisher() *Publisher {
	return publisher
}

func GetService() *Service {
	return service
}

func GetConcurrentTailLimiter() *ConcurrentTailLimiter {
	return concurrentTailLimiter
}

func GetController() *Controller {
	return controller
}
