package inventory

import (
	"time"

	"obzly/internal/util/logger"
)

var repo = &repository{}

var builder = &Builder{
	repo,
	&storageCollector{logger.GetLogger(), &valueCache{}},
	logger.GetLogger(),
	func() time.Time { return time.Now().UTC() },
}

var service = &Service{repo, func() time.Time { return time.Now().UTC() }}

var controller = &Controller{service}

func GetBuilder() *Builder { return builder }

func GetController() *Controller { return controller }
