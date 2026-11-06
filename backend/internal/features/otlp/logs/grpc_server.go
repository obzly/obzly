package otlp_logs

import (
	"context"
	"fmt"
	"net"

	"obzly/internal/features/otlp"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	collectorlogsv1 "go.opentelemetry.io/proto/otlp/collector/logs/v1"
)

// apiKeyMetadataKey carries the API key in gRPC metadata, the X-API-Key header
// lowercased (gRPC lowercases metadata keys).
const apiKeyMetadataKey = "x-api-key"

// GRPCServer implements collectorlogsv1.LogsServiceServer, the OTLP gRPC logs
// receiver on OTLP_GRPC_PORT (default 4317).
type GRPCServer struct {
	collectorlogsv1.UnimplementedLogsServiceServer
	service *OtlpLogsService
}

func (s *GRPCServer) Export(
	ctx context.Context,
	request *collectorlogsv1.ExportLogsServiceRequest,
) (*collectorlogsv1.ExportLogsServiceResponse, error) {
	apiKey := apiKeyFromMetadata(ctx)
	clientIP := clientIPFromPeer(ctx)

	accepted, rejected, err := s.service.Export(apiKey, clientIP, request.GetResourceLogs())
	if err != nil {
		return nil, grpcStatusFor(err)
	}

	response := &collectorlogsv1.ExportLogsServiceResponse{}
	if rejected > 0 {
		response.PartialSuccess = &collectorlogsv1.ExportLogsPartialSuccess{
			RejectedLogRecords: int64(rejected),
			ErrorMessage:       fmt.Sprintf("%d of %d log records rejected", rejected, accepted+rejected),
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

func clientIPFromPeer(ctx context.Context) string {
	p, ok := peer.FromContext(ctx)
	if !ok || p.Addr == nil {
		return ""
	}

	host, _, err := net.SplitHostPort(p.Addr.String())
	if err != nil {
		return p.Addr.String()
	}

	return host
}

func grpcStatusFor(err error) error {
	exportErr, ok := err.(*ExportError)
	if !ok {
		return status.Error(codes.Internal, "failed to process OTLP logs")
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
