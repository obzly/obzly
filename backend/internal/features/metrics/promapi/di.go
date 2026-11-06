package metrics_promapi

import (
	api_keys "obzly/internal/features/api_keys"
)

var metricsQueryService = &MetricsQueryService{
	api_keys.GetApiKeyService(),
}

var controller = &Controller{metricsQueryService}

func GetController() *Controller {
	return controller
}
