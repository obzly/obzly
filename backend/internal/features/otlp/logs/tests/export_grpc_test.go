package otlp_logs_tests

import (
	"context"
	"net"
	"testing"

	"obzly/internal/features/otlp"
	otlp_logs "obzly/internal/features/otlp/logs"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	collectorlogsv1 "go.opentelemetry.io/proto/otlp/collector/logs/v1"
)

// dialOtlpGrpc serves the shared OTLP gRPC server with otlp_logs on an
// in-memory bufconn and returns a client; both close when the test ends.
func dialOtlpGrpc(t *testing.T) collectorlogsv1.LogsServiceClient {
	t.Helper()

	listener := bufconn.Listen(1024 * 1024)
	grpcServer := otlp.NewGRPCServer(nil)
	otlp_logs.RegisterGRPCServer(grpcServer)

	go func() {
		_ = grpcServer.Serve(listener)
	}()
	t.Cleanup(grpcServer.Stop)

	conn, err := grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	return collectorlogsv1.NewLogsServiceClient(conn)
}

func Test_OtlpGrpcExport_WithValidApiKeyAndLogRecord_Accepted(t *testing.T) {
	fixture := SetUpFixture("OTLP gRPC Valid")
	client := dialOtlpGrpc(t)

	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-api-key", fixture.ApiKey.Token)
	response, err := client.Export(ctx, BuildExportRequest("hello from otlp grpc"))

	require.NoError(t, err)
	assert.Nil(t, response.PartialSuccess)
}

// The OTel Collector's otlp (gRPC) exporter gzip-compresses by default. The codec is
// named, not imported, so only otlp.NewGRPCServer's registration makes it available.
func Test_OtlpGrpcExport_WithGzipCompression_Accepted(t *testing.T) {
	fixture := SetUpFixture("OTLP gRPC Gzip")
	client := dialOtlpGrpc(t)

	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-api-key", fixture.ApiKey.Token)
	response, err := client.Export(ctx, BuildExportRequest("gzip-compressed otlp grpc"), grpc.UseCompressor("gzip"))

	require.NoError(t, err)
	assert.Nil(t, response.PartialSuccess)
}

func Test_OtlpGrpcExport_WithoutApiKey_Unauthenticated(t *testing.T) {
	client := dialOtlpGrpc(t)

	_, err := client.Export(context.Background(), BuildExportRequest("should be rejected"))

	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Unauthenticated, st.Code())
}

func Test_OtlpGrpcExport_WithInvalidApiKey_Unauthenticated(t *testing.T) {
	client := dialOtlpGrpc(t)

	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-api-key", "lb_not-a-real-key")
	_, err := client.Export(ctx, BuildExportRequest("should be rejected"))

	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Unauthenticated, st.Code())
}
