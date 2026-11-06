package otlp_logs_tests

import (
	"context"
	"net"
	"testing"
	"time"

	"obzly/internal/features/otlp"
	otlp_logs "obzly/internal/features/otlp/logs"
	"obzly/internal/util/tlsutil"
	tlsutil_testing "obzly/internal/util/tlsutil/testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	collectorlogsv1 "go.opentelemetry.io/proto/otlp/collector/logs/v1"
)

func Test_OtlpGrpcExport_TLSConfiguredAndTLSClient_Accepted(t *testing.T) {
	fixture := SetUpFixture("OTLP gRPC TLS")
	addr, caFile := serveOtlpGrpcTLS(t)
	clientTLS, err := tlsutil.ClientConfig(caFile, "")
	require.NoError(t, err)
	client := dialOtlpGrpcAt(t, addr, credentials.NewTLS(clientTLS))

	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-api-key", fixture.ApiKey.Token)
	response, err := client.Export(ctx, BuildExportRequest("hello from otlp grpc over tls"))

	require.NoError(t, err)
	assert.Nil(t, response.PartialSuccess)
}

// With TLS on, a plaintext client never reaches the handler, so its API key
// is never read off the wire in clear text.
func Test_OtlpGrpcExport_TLSConfiguredAndPlaintextClient_Rejected(t *testing.T) {
	fixture := SetUpFixture("OTLP gRPC TLS Plaintext")
	addr, _ := serveOtlpGrpcTLS(t)
	client := dialOtlpGrpcAt(t, addr, insecure.NewCredentials())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "x-api-key", fixture.ApiKey.Token)
	_, err := client.Export(ctx, BuildExportRequest("should not arrive"))

	require.Error(t, err)
	assert.Equal(t, codes.Unavailable, status.Code(err))
}

// serveOtlpGrpcTLS serves the shared OTLP gRPC server over TLS on a loopback
// port, returning its address and the CA file that trusts it.
func serveOtlpGrpcTLS(t *testing.T) (addr, caFile string) {
	t.Helper()
	certFile, keyFile := tlsutil_testing.WriteSelfSignedCert(t, t.TempDir(), "otlp", "127.0.0.1")
	serverTLS, err := tlsutil.ServerConfig(certFile, keyFile)
	require.NoError(t, err)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcServer := otlp.NewGRPCServer(serverTLS)
	otlp_logs.RegisterGRPCServer(grpcServer)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(grpcServer.Stop)
	return listener.Addr().String(), certFile
}

func dialOtlpGrpcAt(
	t *testing.T,
	addr string,
	transport credentials.TransportCredentials,
) collectorlogsv1.LogsServiceClient {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(transport))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return collectorlogsv1.NewLogsServiceClient(conn)
}
