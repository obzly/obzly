package otlp_traces

import (
	"context"
	"fmt"

	"obzly/internal/features/otlp"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	collectortracev1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
)

// apiKeyMetadataKey carries the API key in gRPC metadata, as for every OTLP
// signal.
const apiKeyMetadataKey = "x-api-key"

// GRPCServer implements collectortracev1.TraceServiceServer on the shared OTLP
// gRPC server.
type GRPCServer struct {
	collectortracev1.UnimplementedTraceServiceServer
	service *OtlpTracesService
}

func (s *GRPCServer) Export(
	ctx context.Context,
	request *collectortracev1.ExportTraceServiceRequest,
) (*collectortracev1.ExportTraceServiceResponse, error) {
	apiKey := apiKeyFromMetadata(ctx)

	accepted, rejected, err := s.service.Export(apiKey, request.GetResourceSpans())
	if err != nil {
		return nil, grpcStatusFor(err)
	}

	response := &collectortracev1.ExportTraceServiceResponse{}
	if rejected > 0 {
		response.PartialSuccess = &collectortracev1.ExportTracePartialSuccess{
			RejectedSpans: int64(rejected),
			ErrorMessage:  fmt.Sprintf("%d of %d spans rejected", rejected, accepted+rejected),
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
		return status.Error(codes.Internal, "failed to process OTLP traces")
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
