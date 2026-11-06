package query

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"obzly/internal/telemetry"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// Fragment results go from executor to coordinator over this gRPC service,
// not NATS: results can be large and NATS messages are capped. It is its
// own listener (QUERIER_GRPC_ADDR), never the OTLP receiver, and every call
// needs QUERIER_TOKEN: an executor reads any project's data.
const (
	resultsFetchMethod = "/obzly.query.v1.Results/Fetch"
	resultChunkBytes   = 1 << 20
	// resultTTL: an unfetched result (its coordinator died) is removed
	// after this.
	resultTTL = 5 * time.Minute
)

var resultsServiceDesc = grpc.ServiceDesc{
	ServiceName: "obzly.query.v1.Results",
	HandlerType: (*any)(nil),
	Streams: []grpc.StreamDesc{{
		StreamName:    "Fetch",
		ServerStreams: true,
		Handler: func(srv any, stream grpc.ServerStream) error {
			server, ok := srv.(*resultsServer)
			if !ok {
				return status.Error(codes.Internal, "unexpected results server")
			}
			return server.fetch(stream)
		},
	}},
}

// resultsServer serves result files from dir by result ID, removing each
// once sent.
type resultsServer struct {
	dir   string
	token string
}

// resultsClient fetches results, one connection per executor address.
type resultsClient struct {
	token string
	// tls nil dials plaintext.
	tls *tls.Config

	mu    sync.Mutex
	conns map[string]*grpc.ClientConn
}

// startResultsServer listens on addr and serves results from dir until ctx
// ends, over TLS when tlsConfig is set. It returns the bound address.
func startResultsServer(ctx context.Context, addr, dir, token string, tlsConfig *tls.Config) (*net.TCPAddr, error) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to listen for query results on %s: %w", addr, err)
	}
	bound, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return nil, fmt.Errorf("query results listener on %s is not TCP", addr)
	}
	options := []grpc.ServerOption{grpc.StreamInterceptor(tokenInterceptor(token))}
	if tlsConfig != nil {
		options = append(options, grpc.Creds(credentials.NewTLS(tlsConfig)))
	}
	server := grpc.NewServer(options...)
	server.RegisterService(&resultsServiceDesc, &resultsServer{dir: dir, token: token})
	go func() { _ = server.Serve(listener) }()
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				server.GracefulStop()
				return
			case <-ticker.C:
				removeExpiredResults(dir)
			}
		}
	}()
	return bound, nil
}

func newResultsClient(token string, tlsConfig *tls.Config) *resultsClient {
	return &resultsClient{token: token, tls: tlsConfig, conns: map[string]*grpc.ClientConn{}}
}

// fetch writes result id from the executor at addr to path.
func (c *resultsClient) fetch(ctx context.Context, addr, id, path string) error {
	conn, err := c.conn(addr)
	if err != nil {
		return err
	}
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+c.token)
	stream, err := conn.NewStream(ctx, &resultsServiceDesc.Streams[0], resultsFetchMethod)
	if err != nil {
		return fmt.Errorf("failed to fetch result from %s: %w", addr, err)
	}
	if err := stream.SendMsg(wrapperspb.String(id)); err != nil {
		return fmt.Errorf("failed to fetch result from %s: %w", addr, err)
	}
	if err := stream.CloseSend(); err != nil {
		return err
	}

	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create fragment result: %w", err)
	}
	defer func() { _ = file.Close() }()
	for {
		chunk := new(wrapperspb.BytesValue)
		err := stream.RecvMsg(chunk)
		if errors.Is(err, io.EOF) {
			return file.Sync()
		}
		if err != nil {
			return fmt.Errorf("failed to fetch result from %s: %w", addr, err)
		}
		if _, err := file.Write(chunk.Value); err != nil {
			return fmt.Errorf("failed to write fragment result: %w", err)
		}
	}
}

func (c *resultsClient) conn(addr string) (*grpc.ClientConn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if conn, ok := c.conns[addr]; ok {
		return conn, nil
	}
	transport := insecure.NewCredentials()
	if c.tls != nil {
		// Without a configured server name the dialled host (the
		// executor's advertised pod IP) is what the cert must cover.
		transport = credentials.NewTLS(c.tls)
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(transport))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to executor %s: %w", addr, err)
	}
	c.conns[addr] = conn
	return conn, nil
}

func (s *resultsServer) fetch(stream grpc.ServerStream) error {
	request := new(wrapperspb.StringValue)
	if err := stream.RecvMsg(request); err != nil {
		return err
	}
	if _, err := uuid.Parse(request.Value); err != nil {
		return status.Error(codes.InvalidArgument, "invalid result id")
	}
	path := resultPath(s.dir, request.Value)
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return status.Error(codes.NotFound, "result not found")
	}
	if err != nil {
		return status.Error(codes.Internal, "failed to open result")
	}
	defer func() {
		_ = file.Close()
		_ = os.Remove(path)
	}()

	buffer := make([]byte, resultChunkBytes)
	for {
		n, err := file.Read(buffer)
		if n > 0 {
			if sendErr := stream.SendMsg(wrapperspb.Bytes(buffer[:n])); sendErr != nil {
				return sendErr
			}
			telemetry.QueryExecutorResultBytes.Add(float64(n))
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return status.Error(codes.Internal, "failed to read result")
		}
	}
}

func tokenInterceptor(token string) grpc.StreamServerInterceptor {
	want := []byte("Bearer " + token)
	return func(srv any, stream grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		md, _ := metadata.FromIncomingContext(stream.Context())
		values := md.Get("authorization")
		if token == "" || len(values) != 1 || subtle.ConstantTimeCompare([]byte(values[0]), want) != 1 {
			return status.Error(codes.Unauthenticated, "invalid query executor token")
		}
		return handler(srv, stream)
	}
}

func resultPath(dir, id string) string {
	return filepath.Join(dir, id+".parquet")
}

func removeExpiredResults(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-resultTTL)
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".parquet") {
			continue
		}
		if info, err := entry.Info(); err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}
