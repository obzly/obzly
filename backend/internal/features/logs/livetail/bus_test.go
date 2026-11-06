package logs_livetail

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"obzly/internal/broker"
	logs_core "obzly/internal/features/logs/core"

	"github.com/google/uuid"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// A tail on one replica gets logs ingested on another: the other replica
// learns of the tail from its announcement and only then publishes.
func Test_NatsBus_TailOnOneReplica_ReceivesLogsPublishedOnAnother(t *testing.T) {
	srv := startServer(t)
	tailReplica := &natsBus{conn: connect(t, srv), interest: newTracker()}
	ingestReplica := &natsBus{conn: connect(t, srv), interest: newTracker()}
	projectID := uuid.New()
	require.NoError(t, ingestReplica.interest.ensureStarted(ingestReplica.conn))

	received := make(chan string, 1)
	ready := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = tailReplica.subscribe(ctx, projectID, func() { close(ready) }, func(items []*logs_core.LogItem) {
			received <- items[0].Message
		})
	}()
	<-ready

	require.Eventually(t, func() bool { return ingestReplica.interest.has(projectID) },
		2*time.Second, 10*time.Millisecond, "the announcement reaches the other replica")
	require.NoError(t, ingestReplica.publish(ctx, projectID, []*logs_core.LogItem{{Message: "from replica b"}}))

	select {
	case message := <-received:
		require.Equal(t, "from replica b", message)
	case <-time.After(5 * time.Second):
		t.Fatal("tail did not receive the log")
	}
}

func Test_NatsBus_WithoutAnOpenTail_PublishesNothing(t *testing.T) {
	srv := startServer(t)
	bus := &natsBus{conn: connect(t, srv), interest: newTracker()}
	projectID := uuid.New()
	watcher := connect(t, srv)
	sub, err := watcher.SubscribeSync(logsSubject + ">")
	require.NoError(t, err)
	require.NoError(t, watcher.Flush())

	require.NoError(t, bus.publish(context.Background(), projectID, []*logs_core.LogItem{{Message: "nobody tails"}}))
	require.NoError(t, bus.conn.Flush())

	_, err = sub.NextMsg(300 * time.Millisecond)
	require.ErrorIs(t, err, nats.ErrTimeout)
}

func newTracker() *interestTracker {
	return &interestTracker{until: map[uuid.UUID]time.Time{}}
}

func startServer(t *testing.T) *server.Server {
	t.Helper()
	srv, err := broker.StartServer(broker.ServerConfig{Name: "tail", StoreDir: t.TempDir(), InProcess: true},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	t.Cleanup(func() {
		srv.Shutdown()
		srv.WaitForShutdown()
	})
	return srv
}

func connect(t *testing.T, srv *server.Server) *nats.Conn {
	t.Helper()
	client, err := broker.ConnectInProcess(srv, "tail-test")
	require.NoError(t, err)
	t.Cleanup(client.Close)
	return client.Conn
}
