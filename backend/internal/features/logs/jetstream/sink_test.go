package logs_jetstream

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"obzly/internal/broker"
	logs_core "obzly/internal/features/logs/core"
	"obzly/internal/ingest"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
)

// fakeWriter records appended rows and fails uploads while failUploads is set.
type fakeWriter struct {
	mu          sync.Mutex
	stream      string
	buffered    map[uuid.UUID]int
	flushed     int
	uploaded    int
	failUploads bool
}

func Test_Sink_WhenUploadFails_HoldsTheBatchUntilARetrySucceeds(t *testing.T) {
	client, consumer := startStream(t)
	logs, ai := newFakeWriter(logs_core.StreamLogs), newFakeWriter(logs_core.StreamAILogs)
	logs.failUploads = true
	sink := NewSink(logs, ai, 1<<30, time.Nanosecond, discardLogger())
	projectID := uuid.New()
	publish(t, client, projectID, 3)

	handleNext(t, client, consumer, sink, projectID)
	sink.Tick(context.Background(), broker.ShardFor(projectID))

	require.Equal(t, 1, logs.flushCount())
	require.EqualValues(t, 1, pendingAcks(t, consumer), "not acked while the segment is only on local disk")

	logs.setFailUploads(false)
	sink.Tick(context.Background(), broker.ShardFor(projectID))

	require.Equal(t, 1, logs.uploadCount())
	require.Eventually(t, func() bool { return streamMessages(t, client) == 0 },
		5*time.Second, 50*time.Millisecond, "acked once uploaded")
}

func Test_Sink_WhenTargetBytesReached_FlushesWithoutWaitingForMaxAge(t *testing.T) {
	client, consumer := startStream(t)
	logs, ai := newFakeWriter(logs_core.StreamLogs), newFakeWriter(logs_core.StreamAILogs)
	sink := NewSink(logs, ai, 1, time.Hour, discardLogger())
	projectID := uuid.New()
	publish(t, client, projectID, 2)

	handleNext(t, client, consumer, sink, projectID)

	require.Equal(t, 1, logs.flushCount())
	require.Zero(t, ai.flushCount(), "no AI events, so no AI segment")
	require.Eventually(t, func() bool { return streamMessages(t, client) == 0 },
		5*time.Second, 50*time.Millisecond)
}

func Test_Sink_BeforeMaxAge_KeepsRowsBufferedAndTheBatchUnacked(t *testing.T) {
	client, consumer := startStream(t)
	logs, ai := newFakeWriter(logs_core.StreamLogs), newFakeWriter(logs_core.StreamAILogs)
	sink := NewSink(logs, ai, 1<<30, time.Hour, discardLogger())
	projectID := uuid.New()
	publish(t, client, projectID, 2)

	handleNext(t, client, consumer, sink, projectID)
	sink.Tick(context.Background(), broker.ShardFor(projectID))

	require.Zero(t, logs.flushCount())
	require.EqualValues(t, 1, pendingAcks(t, consumer))

	sink.Release(context.Background(), broker.ShardFor(projectID))

	require.Equal(t, 1, logs.flushCount(), "releasing a shard flushes it")
	require.Eventually(t, func() bool { return streamMessages(t, client) == 0 },
		5*time.Second, 50*time.Millisecond)
}

func Test_Sink_UndecodableBatch_IsTerminatedNotRedelivered(t *testing.T) {
	client, consumer := startStream(t)
	sink := NewSink(newFakeWriter(logs_core.StreamLogs), newFakeWriter(logs_core.StreamAILogs),
		1<<30, time.Hour, discardLogger())
	projectID := uuid.New()
	require.NoError(t, client.PublishIngest(context.Background(), broker.SignalLogs, projectID,
		[][]byte{[]byte("not json")}))

	handleNext(t, client, consumer, sink, projectID)

	require.Eventually(t, func() bool { return streamMessages(t, client) == 0 },
		5*time.Second, 50*time.Millisecond)
}

func newFakeWriter(stream string) *fakeWriter {
	return &fakeWriter{stream: stream, buffered: map[uuid.UUID]int{}}
}

func (w *fakeWriter) Append(entries map[uuid.UUID][]*logs_core.LogItem) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	for projectID, items := range entries {
		w.buffered[projectID] += len(items)
	}
	return nil
}

func (w *fakeWriter) Flush(projectID uuid.UUID) (*segments.SegmentRecord, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	rows := w.buffered[projectID]
	if rows == 0 {
		return nil, nil
	}
	delete(w.buffered, projectID)
	w.flushed++
	return &segments.SegmentRecord{ID: uuid.New(), ProjectID: projectID, Stream: w.stream, RowCount: int64(rows)}, nil
}

func (w *fakeWriter) Upload(_ context.Context, _ *segments.SegmentRecord) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failUploads {
		return errors.New("object storage unavailable")
	}
	w.uploaded++
	return nil
}

func (w *fakeWriter) setFailUploads(fail bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.failUploads = fail
}

func (w *fakeWriter) flushCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.flushed
}

func (w *fakeWriter) uploadCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.uploaded
}

func startStream(t *testing.T) (*broker.Client, jetstream.Consumer) {
	t.Helper()
	srv, err := broker.StartServer(broker.ServerConfig{Name: "sink", StoreDir: t.TempDir(), InProcess: true},
		discardLogger())
	require.NoError(t, err)
	t.Cleanup(func() {
		srv.Shutdown()
		srv.WaitForShutdown()
	})
	client, err := broker.ConnectInProcess(srv, "test")
	require.NoError(t, err)
	t.Cleanup(client.Close)
	ctx := context.Background()
	require.NoError(t, client.EnsureIngestStreams(ctx, broker.StreamSettings{Replicas: 1, MaxBytes: 64 << 20}))
	consumer, err := client.JetStream.CreateOrUpdateConsumer(ctx, "INGEST_LOGS", jetstream.ConsumerConfig{
		Durable: "sink-test", AckPolicy: jetstream.AckExplicitPolicy, AckWait: time.Minute,
	})
	require.NoError(t, err)
	return client, consumer
}

func publish(t *testing.T, client *broker.Client, projectID uuid.UUID, rows int) {
	t.Helper()
	items := make([]*logs_core.LogItem, rows)
	for i := range items {
		items[i] = &logs_core.LogItem{ID: uuid.New(), ProjectID: projectID, Timestamp: time.Now().UTC(),
			Level: "info", Message: "row"}
	}
	payloads, err := ingest.EncodePayloads(items)
	require.NoError(t, err)
	require.NoError(t, client.PublishIngest(context.Background(), broker.SignalLogs, projectID, payloads))
}

func handleNext(
	t *testing.T,
	client *broker.Client,
	consumer jetstream.Consumer,
	sink *ingest.Sink,
	projectID uuid.UUID,
) {
	t.Helper()
	msg, err := consumer.Next(jetstream.FetchMaxWait(5 * time.Second))
	require.NoError(t, err)
	require.NoError(t, sink.Handle(broker.ShardFor(projectID), projectID, msg.Data(),
		broker.NewAck(client.Conn, msg.Reply())))
	require.NoError(t, client.Conn.Flush())
}

func pendingAcks(t *testing.T, consumer jetstream.Consumer) int {
	t.Helper()
	info, err := consumer.Info(context.Background())
	require.NoError(t, err)
	return info.NumAckPending
}

func streamMessages(t *testing.T, client *broker.Client) uint64 {
	t.Helper()
	stream, err := client.JetStream.Stream(context.Background(), "INGEST_LOGS")
	require.NoError(t, err)
	info, err := stream.Info(context.Background())
	require.NoError(t, err)
	return info.State.Msgs
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
