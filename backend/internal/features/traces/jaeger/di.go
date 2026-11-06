package traces_jaeger

import (
	api_keys "obzly/internal/features/api_keys"
)

var jaegerService = &JaegerService{
	api_keys.GetApiKeyService(),
}

var jaegerController = &JaegerController{
	jaegerService,
}

func GetJaegerController() *JaegerController {
	return jaegerController
}
