package traces_querying

import (
	"time"

	"obzly/internal/cache"
	projects_services "obzly/internal/features/projects/services"
	"obzly/internal/util/querylimit"
)

var traceQueryService = &TraceQueryService{
	projects_services.GetProjectService(),
	querylimit.New(cache.GetCache(), "spans", 6, 30*time.Second),
}

var traceQueryController = &TraceQueryController{
	traceQueryService,
}

func GetTraceQueryService() *TraceQueryService {
	return traceQueryService
}

func GetTraceQueryController() *TraceQueryController {
	return traceQueryController
}
