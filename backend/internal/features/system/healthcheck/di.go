package system_healthcheck

import (
	"obzly/internal/features/disk"
)

var healthcheckService = &HealthcheckService{
	disk.GetDiskService(),
}
var healthcheckController = &HealthcheckController{
	healthcheckService,
}

func GetHealthcheckController() *HealthcheckController {
	return healthcheckController
}
