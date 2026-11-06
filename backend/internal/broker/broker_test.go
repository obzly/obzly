package broker

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
)

const testToken = "test-token"

func Test_InProcessServer_PublishedBatchIsConsumedOnceAndLeavesTheStream(t *testing.T) {
	srv := startTestServer(t, ServerConfig{Name: "all-1", StoreDir: t.TempDir(), InProcess: true})
	client, err := ConnectInProcess(srv, "test")
	require.NoError(t, err)
	t.Cleanup(client.Close)
	ctx := context.Background()
	require.NoError(t, client.EnsureIngestStreams(ctx, StreamSettings{Replicas: 1, MaxBytes: 64 << 20}))

	_, err = client.JetStream.Publish(ctx, "ingest.logs.7.project-a", []byte("batch-1"))
	require.NoError(t, err)

	consumer, err := client.JetStream.CreateOrUpdateConsumer(ctx, "INGEST_LOGS", jetstream.ConsumerConfig{
		Durable: "writer", AckPolicy: jetstream.AckExplicitPolicy,
	})
	require.NoError(t, err)
	batch, err := consumer.Fetch(1, jetstream.FetchMaxWait(2*time.Second))
	require.NoError(t, err)
	var received []string
	for msg := range batch.Messages() {
		received = append(received, string(msg.Data()))
		require.NoError(t, msg.DoubleAck(ctx))
	}
	require.Equal(t, []string{"batch-1"}, received)

	stream, err := client.JetStream.Stream(ctx, "INGEST_LOGS")
	require.NoError(t, err)
	info, err := stream.Info(ctx)
	require.NoError(t, err)
	require.Zero(t, info.State.Msgs, "work-queue retention removes an acked batch")
}

func Test_IngestStream_WhenFull_RejectsNewPublishes(t *testing.T) {
	srv := startTestServer(t, ServerConfig{Name: "all-1", StoreDir: t.TempDir(), InProcess: true})
	client, err := ConnectInProcess(srv, "test")
	require.NoError(t, err)
	t.Cleanup(client.Close)
	ctx := context.Background()
	require.NoError(t, client.EnsureIngestStreams(ctx, StreamSettings{Replicas: 1, MaxBytes: 4096}))

	payload := []byte(strings.Repeat("x", 1024))
	var publishErr error
	for i := 0; i < 20 && publishErr == nil; i++ {
		_, publishErr = client.JetStream.Publish(ctx, "ingest.logs.0.project-a", payload)
	}

	require.Error(t, publishErr, "a full stream refuses the batch instead of dropping stored data")
}

// With R3 on three brokers, losing one neither loses acked batches nor stops
// new ones being acked.
func Test_ThreeBrokerCluster_R3StreamSurvivesOneBrokerLoss(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a three-node JetStream cluster")
	}
	ports := freePorts(t, 6)
	clientPorts, clusterPorts := ports[:3], ports[3:]
	var routes []string
	for _, port := range clusterPorts {
		routes = append(routes, fmt.Sprintf("nats-route://127.0.0.1:%d", port))
	}
	var servers []*server.Server
	for i := range 3 {
		servers = append(servers, startTestServer(t, ServerConfig{
			Name: fmt.Sprintf("broker-%d", i), StoreDir: t.TempDir(), Token: testToken,
			ClientPort: clientPorts[i], ClusterPort: clusterPorts[i], MonitorPort: -1,
			ClusterName: "obzly-test", Routes: routes,
		}))
	}
	var urls []string
	for _, port := range clientPorts {
		urls = append(urls, fmt.Sprintf("nats://127.0.0.1:%d", port))
	}
	client, err := Connect(strings.Join(urls, ","), testToken, "test")
	require.NoError(t, err)
	t.Cleanup(client.Close)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// The cluster needs a meta leader before it accepts streams; requests during
	// the election can go unanswered, so each attempt is bounded and retried.
	require.Eventually(t, func() bool {
		attemptCtx, attemptCancel := context.WithTimeout(ctx, 3*time.Second)
		defer attemptCancel()
		return client.EnsureIngestStreams(attemptCtx, StreamSettings{Replicas: 3, MaxBytes: 64 << 20}) == nil
	}, 30*time.Second, 250*time.Millisecond)
	for i := range 5 {
		_, err := client.JetStream.Publish(ctx, "ingest.spans.1.project-a", fmt.Appendf(nil, "before-%d", i))
		require.NoError(t, err)
	}

	// Wait for a settled stream (leader and both followers current); under load
	// a follower can still be catching up here.
	stream, err := client.JetStream.Stream(ctx, "INGEST_SPANS")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		info, err := stream.Info(ctx)
		if err != nil || info.Cluster == nil || info.Cluster.Leader == "" || len(info.Cluster.Replicas) != 2 {
			return false
		}
		for _, replica := range info.Cluster.Replicas {
			if !replica.Current || replica.Offline {
				return false
			}
		}
		return true
	}, 30*time.Second, 100*time.Millisecond, "stream replicas current")

	servers[0].Shutdown()
	servers[0].WaitForShutdown()

	// Bounded: a publish routed to the stopped broker can otherwise wait out the
	// whole context.
	require.Eventually(t, func() bool {
		attemptCtx, attemptCancel := context.WithTimeout(ctx, 3*time.Second)
		defer attemptCancel()
		_, err := client.JetStream.Publish(attemptCtx, "ingest.spans.1.project-a", []byte("after"))
		return err == nil
	}, 30*time.Second, 250*time.Millisecond, "two of three brokers keep acking")
	info, err := stream.Info(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 6, info.State.Msgs, "every acked batch is still held")
	require.Equal(t, 3, info.Config.Replicas)
}

func Test_ServerOptions_NetworkedBrokerRequiresToken(t *testing.T) {
	_, err := serverOptions(ServerConfig{Name: "broker-0", StoreDir: t.TempDir(), ClientPort: 4222})
	require.ErrorContains(t, err, "token")

	opts, err := serverOptions(ServerConfig{
		Name: "broker-0", StoreDir: t.TempDir(), Token: testToken, ClientPort: 4222, ClusterPort: 6222,
		ClusterName: "obzly", Routes: []string{"nats-route://broker-1:6222"},
	})
	require.NoError(t, err)
	require.Equal(t, testToken, opts.Authorization)
	require.Equal(t, routeUser, opts.Routes[0].User.Username(), "routes carry the peer credentials")
}

// startTestServer's log is shown only if the test fails: the cluster test
// fails rarely, on loaded CI runners, and its elections are the evidence.
func startTestServer(t *testing.T, cfg ServerConfig) *server.Server {
	t.Helper()
	logs := &lockedBuffer{}
	srv, err := StartServer(cfg, slog.New(slog.NewTextHandler(logs, nil)))
	require.NoError(t, err)
	t.Cleanup(func() {
		srv.Shutdown()
		srv.WaitForShutdown()
	})
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("%s log:\n%s", cfg.Name, logs.String())
		}
	})
	return srv
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// freePorts picks ports below the kernel's ephemeral range (Linux:
// 32768-60999): a port from listening on :0 can be taken by another test's
// outgoing connection before the broker binds it.
func freePorts(t *testing.T, n int) []int {
	t.Helper()
	ports := make([]int, 0, n)
	for len(ports) < n {
		port := 20000 + rand.IntN(12000)
		if slices.Contains(ports, port) {
			continue
		}
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		require.NoError(t, listener.Close())
		ports = append(ports, port)
	}
	return ports
}
