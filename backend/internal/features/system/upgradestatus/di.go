package system_upgradestatus

import "obzly/internal/storage/segments"

var service = &Service{&segments.SegmentRepository{}}

var controller = &Controller{service}

func GetController() *Controller {
	return controller
}
