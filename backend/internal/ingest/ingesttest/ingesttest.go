// Package ingesttest runs the JetStream ingest path inside a test: an
// in-process broker that receivers publish to, and a worker's shard runner.
package ingesttest

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"obzly/internal/broker"
	"obzly/internal/cache"
	"obzly/internal/ingest"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// MaxAge is the L0 max-age StartConsumer's sinks should use.
const MaxAge = 300 * time.Millisecond

// StartBroker makes this test's receivers publish to an in-process broker
// whose ingest streams hold maxBytes each, as SERVICE_MODE=all with
// INGEST_BUFFER=jetstream does.
func StartBroker(t *testing.T, maxBytes int64) *broker.Client {
	t.Helper()
	client, err := broker.StartInProcess("test-"+uuid.NewString()[:8], t.TempDir(), discardLogger())
	require.NoError(t, err)
	require.NoError(t, client.EnsureIngestStreams(context.Background(),
		broker.StreamSettings{Replicas: 1, MaxBytes: maxBytes}))
	broker.SetDefault(client)
	t.Cleanup(broker.ShutdownDefault)
	return client
}

// StartConsumer runs a worker's shard runner for signal until the test ends.
func StartConsumer(t *testing.T, client *broker.Client, signal string, sink *ingest.Sink) {
	t.Helper()
	runner, err := broker.NewShardRunner(client, cache.GetCache(), signal, time.Minute, sink, discardLogger(),
		broker.WithLeaseScope(uuid.NewString()), broker.WithTick(100*time.Millisecond))
	require.NoError(t, err)

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
}

// StreamMessages is how many batches signal's stream holds.
func StreamMessages(t *testing.T, client *broker.Client, signal string) uint64 {
	t.Helper()
	for _, s := range broker.IngestStreams {
		if s.Signal != signal {
			continue
		}
		stream, err := client.JetStream.Stream(context.Background(), s.Name)
		require.NoError(t, err)
		info, err := stream.Info(context.Background())
		require.NoError(t, err)
		return info.State.Msgs
	}
	t.Fatalf("no ingest stream for %q", signal)
	return 0
}

// Logger discards everything.
func Logger() *slog.Logger {
	return discardLogger()
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
