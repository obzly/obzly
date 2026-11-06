package otlp_metrics_tests

import (
	"context"
	"net"
	"testing"

	"obzly/internal/features/otlp"
	otlp_metrics "obzly/internal/features/otlp/metrics"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	collectormetricsv1 "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
)

func dialOtlpGrpc(t *testing.T) collectormetricsv1.MetricsServiceClient {
	t.Helper()

	listener := bufconn.Listen(1024 * 1024)
	grpcServer := otlp.NewGRPCServer(nil)
	otlp_metrics.RegisterGRPCServer(grpcServer)

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

	return collectormetricsv1.NewMetricsServiceClient(conn)
}

func Test_OtlpGrpcExport_WithValidApiKeyAndGauge_Accepted(t *testing.T) {
	fixture := SetUpFixture("OTLP Metrics gRPC Valid")
	client := dialOtlpGrpc(t)

	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-api-key", fixture.ApiKey.Token)
	response, err := client.Export(ctx, BuildGaugeExportRequest("grpc_metric"))

	require.NoError(t, err)
	assert.Nil(t, response.PartialSuccess)
}

// The OTel Collector's otlp (gRPC) exporter gzip-compresses by default. The codec is
// named, not imported, so only otlp.NewGRPCServer's registration makes it available.
func Test_OtlpGrpcExport_WithGzipCompression_Accepted(t *testing.T) {
	fixture := SetUpFixture("OTLP Metrics gRPC Gzip")
	client := dialOtlpGrpc(t)

	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-api-key", fixture.ApiKey.Token)
	response, err := client.Export(ctx, BuildGaugeExportRequest("grpc_gzip_metric"), grpc.UseCompressor("gzip"))

	require.NoError(t, err)
	assert.Nil(t, response.PartialSuccess)
}

func Test_OtlpGrpcExport_WithoutApiKey_Unauthenticated(t *testing.T) {
	client := dialOtlpGrpc(t)

	_, err := client.Export(context.Background(), BuildGaugeExportRequest("should_be_rejected"))

	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Unauthenticated, st.Code())
}

func Test_OtlpGrpcExport_WithInvalidApiKey_Unauthenticated(t *testing.T) {
	client := dialOtlpGrpc(t)

	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-api-key", "lb_not-a-real-key")
	_, err := client.Export(ctx, BuildGaugeExportRequest("should_be_rejected"))

	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Unauthenticated, st.Code())
}
