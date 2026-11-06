package otlp_metrics

import (
	"context"
	"fmt"

	"obzly/internal/features/otlp"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	collectormetricsv1 "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
)

// apiKeyMetadataKey carries the API key in gRPC metadata.
const apiKeyMetadataKey = "x-api-key"

// GRPCServer implements collectormetricsv1.MetricsServiceServer on the shared
// OTLP gRPC server.
type GRPCServer struct {
	collectormetricsv1.UnimplementedMetricsServiceServer
	service *OtlpMetricsService
}

func (s *GRPCServer) Export(
	ctx context.Context,
	request *collectormetricsv1.ExportMetricsServiceRequest,
) (*collectormetricsv1.ExportMetricsServiceResponse, error) {
	apiKey := apiKeyFromMetadata(ctx)

	accepted, rejected, err := s.service.Export(apiKey, request.GetResourceMetrics())
	if err != nil {
		return nil, grpcStatusFor(err)
	}

	response := &collectormetricsv1.ExportMetricsServiceResponse{}
	if rejected > 0 {
		response.PartialSuccess = &collectormetricsv1.ExportMetricsPartialSuccess{
			RejectedDataPoints: int64(rejected),
			ErrorMessage:       fmt.Sprintf("%d of %d samples rejected", rejected, accepted+rejected),
		}
	}

	return response, nil
}

func apiKeyFromMetadata(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}

	values := md.Get(apiKeyMetadataKey)
	if len(values) == 0 {
		return ""
	}

	return values[0]
}

func grpcStatusFor(err error) error {
	exportErr, ok := err.(*ExportError)
	if !ok {
		return status.Error(codes.Internal, "failed to process OTLP metrics")
	}

	switch exportErr.Code {
	case ExportErrorUnauthenticated:
		return status.Error(codes.Unauthenticated, exportErr.Message)
	case ExportErrorResourceExhausted, ExportErrorUnavailable:
		return otlp.RetryableGRPCError(exportErr.Code == ExportErrorResourceExhausted, exportErr.Message)
	default:
		return status.Error(codes.InvalidArgument, exportErr.Message)
	}
}
