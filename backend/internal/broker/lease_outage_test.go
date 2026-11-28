package broker

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"obzly/internal/config"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/valkey-io/valkey-go"
)

// tcpProxy forwards a local port to target until cut, which closes the
// listener and every open connection: Valkey becoming unreachable.
type tcpProxy struct {
	target string
	addr   string
	mu     sync.Mutex
	ln     net.Listener
	conns  []net.Conn
}

// A Valkey outage longer than the lease TTL used to drop every shard, so all
// ingest stopped until Valkey came back. The worker now keeps consuming;
// compaction (OwnsProject) waits until a renewal is confirmed again.
func Test_ShardRunner_ValkeyUnreachable_KeepsConsumingAndCompactsAgainOnceRenewed(t *testing.T) {
	client := startTestClient(t, 64<<20)
	proxy := startTCPProxy(t, valkeyAddress())
	valkeyClient := proxiedValkeyClient(t, proxy.addr)
	handler := newRecordingHandler()
	runner, err := NewShardRunner(client, valkeyClient, SignalLogs, time.Minute, handler,
		slog.New(slog.NewTextHandler(io.Discard, nil)), WithLeaseScope(uuid.NewString()), WithTick(testTick))
	require.NoError(t, err)
	RegisterRunner(runner)
	t.Cleanup(func() {
		runnersMu.Lock()
		delete(runners, SignalLogs)
		runnersMu.Unlock()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runner.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	require.Eventually(t, func() bool { return runner.OwnedShards() == IngestShards },
		10*time.Second, 50*time.Millisecond)

	proxy.cut()
	time.Sleep(2 * runner.leaseTTL())

	require.Equal(t, IngestShards, runner.OwnedShards(), "shards kept through the outage")
	project := uuid.New()
	require.NoError(t, client.PublishIngest(context.Background(), SignalLogs, project, [][]byte{[]byte("x")}))
	require.Eventually(t, func() bool { return handler.count(project) == 1 },
		10*time.Second, 50*time.Millisecond, "batches still consumed")
	owns, _ := OwnsProject(SignalLogs, project)
	require.False(t, owns, "no compaction on a lease that may have lapsed")

	proxy.restore(t)
	require.Eventually(t, func() bool {
		owns, _ := OwnsProject(SignalLogs, project)
		return owns
	}, 15*time.Second, 50*time.Millisecond, "compacts again once Valkey confirms the lease")
	require.Equal(t, IngestShards, runner.OwnedShards())
}

func valkeyAddress() string {
	env := config.GetEnv()
	return env.ValkeyHost + ":" + env.ValkeyPort
}

func proxiedValkeyClient(t *testing.T, addr string) valkey.Client {
	env := config.GetEnv()
	options := valkey.ClientOption{
		InitAddress: []string{addr},
		Password:    env.ValkeyPassword,
		Username:    env.ValkeyUsername,
	}
	if env.ValkeyIsSsl {
		options.TLSConfig = &tls.Config{ServerName: env.ValkeyHost}
	}
	client, err := valkey.NewClient(options)
	require.NoError(t, err)
	t.Cleanup(client.Close)
	return client
}

func startTCPProxy(t *testing.T, target string) *tcpProxy {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	proxy := &tcpProxy{target: target, addr: ln.Addr().String()}
	proxy.serve(ln)
	t.Cleanup(proxy.cut)
	return proxy
}

func (p *tcpProxy) serve(ln net.Listener) {
	p.mu.Lock()
	p.ln = ln
	p.mu.Unlock()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			upstream, err := net.Dial("tcp", p.target)
			if err != nil {
				_ = conn.Close()
				continue
			}
			p.mu.Lock()
			p.conns = append(p.conns, conn, upstream)
			p.mu.Unlock()
			go func() { _, _ = io.Copy(upstream, conn); _ = upstream.Close() }()
			go func() { _, _ = io.Copy(conn, upstream); _ = conn.Close() }()
		}
	}()
}

func (p *tcpProxy) cut() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ln != nil {
		_ = p.ln.Close()
		p.ln = nil
	}
	for _, conn := range p.conns {
		_ = conn.Close()
	}
	p.conns = nil
}

func (p *tcpProxy) restore(t *testing.T) {
	ln, err := net.Listen("tcp", p.addr)
	require.NoError(t, err)
	p.serve(ln)
}
