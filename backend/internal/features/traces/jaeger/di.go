package traces_jaeger

import (
	"time"

	"obzly/internal/cache"
	api_keys "obzly/internal/features/api_keys"
	"obzly/internal/util/querylimit"
)

var jaegerService = &JaegerService{
	api_keys.GetApiKeyService(),
	querylimit.New(cache.GetCache(), "jaeger-lookups", 8, 30*time.Second),
}

var jaegerController = &JaegerController{
	jaegerService,
}

func GetJaegerController() *JaegerController {
	return jaegerController
}
