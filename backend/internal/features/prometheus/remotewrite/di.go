package prometheus_remotewrite

import (
	api_keys "obzly/internal/features/api_keys"
)

var remoteWriteService = &RemoteWriteService{
	api_keys.GetApiKeyService(),
}

var httpController = &HTTPController{remoteWriteService}

func GetHTTPController() *HTTPController {
	return httpController
}
