package metrics_querying

import (
	"time"

	"obzly/internal/cache"
	projects_services "obzly/internal/features/projects/services"
	"obzly/internal/util/querylimit"
)

// The metrics explorer and infrastructure pages send several PromQL queries
// at once, so they queue for a slot rather than fail.
var metricQueryService = &MetricQueryService{
	projects_services.GetProjectService(),
	querylimit.New(cache.GetCache(), "promql", 12, 30*time.Second),
}

var metricQueryController = &MetricQueryController{
	metricQueryService,
}

func GetMetricQueryService() *MetricQueryService {
	return metricQueryService
}

func GetMetricQueryController() *MetricQueryController {
	return metricQueryController
}
