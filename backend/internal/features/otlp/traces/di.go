package otlp_traces

import (
	api_keys "obzly/internal/features/api_keys"

	"google.golang.org/grpc"

	collectortracev1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
)

var otlpTracesService = &OtlpTracesService{
	api_keys.GetApiKeyService(),
}

var httpController = &HTTPController{otlpTracesService}

func GetHTTPController() *HTTPController {
	return httpController
}

// RegisterGRPCServer registers the traces OTLP service on the shared gRPC
// server.
func RegisterGRPCServer(server *grpc.Server) {
	collectortracev1.RegisterTraceServiceServer(server, &GRPCServer{service: otlpTracesService})
}
