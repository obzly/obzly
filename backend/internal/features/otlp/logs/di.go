package otlp_logs

import (
	api_keys "obzly/internal/features/api_keys"
	logs_receiving "obzly/internal/features/logs/receiving"

	"google.golang.org/grpc"

	collectorlogsv1 "go.opentelemetry.io/proto/otlp/collector/logs/v1"
)

var otlpLogsService = &OtlpLogsService{
	api_keys.GetApiKeyService(),
	logs_receiving.GetLogReceivingService(),
}

var httpController = &HTTPController{otlpLogsService}

func GetHTTPController() *HTTPController {
	return httpController
}

// RegisterGRPCServer registers the logs OTLP service on the shared gRPC server.
func RegisterGRPCServer(server *grpc.Server) {
	collectorlogsv1.RegisterLogsServiceServer(server, &GRPCServer{service: otlpLogsService})
}
