// Package otlp holds what every OTLP signal receiver shares: one gRPC server
// and port (each signal registers its service on it) and the HTTP body reader.
package otlp

import (
	"crypto/tls"
	"fmt"
	"net"

	"obzly/internal/telemetry"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	// Registers the gzip codec, the OTel Collector exporter's default. gRPC checks
	// MaxRecvMsgSize against the decompressed size.
	_ "google.golang.org/grpc/encoding/gzip"
)

// NewGRPCServer builds the shared OTLP gRPC server; callers register their
// services on it. MaxRecvMsgSize matches ReadBody's cap (gRPC's default is
// 4 MiB). A nil tlsConfig serves plaintext.
func NewGRPCServer(tlsConfig *tls.Config) *grpc.Server {
	options := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(MaxBodyBytes),
		grpc.ChainUnaryInterceptor(telemetry.UnaryServerInterceptor()),
	}
	if tlsConfig != nil {
		options = append(options, grpc.Creds(credentials.NewTLS(tlsConfig)))
	}
	return grpc.NewServer(options...)
}

// Listen opens the OTLP gRPC listener, separately so a bind failure surfaces
// before background workers start.
func Listen(port string) (net.Listener, error) {
	listener, err := net.Listen("tcp", ":"+port)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on OTLP gRPC port %s: %w", port, err)
	}
	return listener, nil
}
