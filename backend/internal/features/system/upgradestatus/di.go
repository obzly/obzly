package system_upgradestatus

import (
	"time"

	"obzly/internal/config"
	"obzly/internal/storage/segments"
)

var service = &Service{&segments.SegmentRepository{}}

var releaseChecker = NewReleaseChecker(latestReleaseURL,
	func() bool { return config.GetEnv().UpdateCheckEnabled() },
	func() time.Time { return time.Now().UTC() })

var controller = &Controller{service, releaseChecker}

func GetController() *Controller {
	return controller
}
