package otlp_metrics

import (
	"obzly/internal/cache"
	api_keys "obzly/internal/features/api_keys"

	"google.golang.org/grpc"

	collectormetricsv1 "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
)

var otlpMetricsService = &OtlpMetricsService{
	api_keys.GetApiKeyService(),
	NewValkeyDeltaTotals(cache.GetCache()),
}

var httpController = &HTTPController{otlpMetricsService}

func GetHTTPController() *HTTPController {
	return httpController
}

// RegisterGRPCServer registers the metrics OTLP service on the shared gRPC
// server.
func RegisterGRPCServer(server *grpc.Server) {
	collectormetricsv1.RegisterMetricsServiceServer(server, &GRPCServer{service: otlpMetricsService})
}
