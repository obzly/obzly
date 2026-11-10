package cloudcost

import (
	"time"

	"obzly/internal/util/logger"
)

var service = &Service{logger.GetLogger(), func() time.Time { return time.Now().UTC() }}

var controller = &Controller{service}

func GetService() *Service { return service }

func GetController() *Controller { return controller }
