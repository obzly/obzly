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
	flushedRows map[uuid.UUID]int
	flushed     int
	uploaded    int
	failUploads bool
	// failFlushEvery: every nth flush fails and drops its rows, as a real
	// writer does.
	failFlushEvery int
	flushCalls     int
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
	require.Never(t, func() bool { return pendingAcks(t, consumer) == 0 }, 300*time.Millisecond, 50*time.Millisecond,
		"not acked while the segment is only on local disk")

	logs.setFailUploads(false)
	require.Eventually(t, func() bool {
		sink.Tick(context.Background(), broker.ShardFor(projectID))
		return logs.uploadCount() == 1
	}, 5*time.Second, 50*time.Millisecond, "a later tick uploads it")
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

// With lanes, two of a worker's shards can carry one project, sharing its
// writer buffer: releasing one flushes and acks both shards' batches, and
// the other keeps serving the project.
func Test_Sink_ProjectOnTwoShards_ReleasingOneAcksBothAndTheOtherKeepsServing(t *testing.T) {
	client, consumer := startStream(t)
	logs, ai := newFakeWriter(logs_core.StreamLogs), newFakeWriter(logs_core.StreamAILogs)
	sink := NewSink(logs, ai, 1<<30, time.Hour, discardLogger())
	projectID := uuid.New()
	laneA, laneB := broker.LaneShard(projectID, 0), broker.LaneShard(projectID, 1)
	publish(t, client, projectID, 2)
	publish(t, client, projectID, 2)

	handleNextOn(t, client, consumer, sink, laneA, projectID)
	handleNextOn(t, client, consumer, sink, laneB, projectID)
	sink.Release(context.Background(), laneA)

	require.Equal(t, 1, logs.flushCount())
	require.Eventually(t, func() bool { return streamMessages(t, client) == 0 },
		5*time.Second, 50*time.Millisecond, "both shards' batches acked: their rows are in the segment")

	publish(t, client, projectID, 2)
	handleNextOn(t, client, consumer, sink, laneB, projectID)
	sink.Release(context.Background(), laneB)

	require.Equal(t, 2, logs.flushCount(), "the other shard still flushes the project")
	require.Eventually(t, func() bool { return streamMessages(t, client) == 0 },
		5*time.Second, 50*time.Millisecond)
}

// Two shards handling one project's batches at once: every batch is acked
// once its rows are written, none lost.
func Test_Sink_ProjectOnTwoShardsConcurrently_AcksEveryBatchWithItsRows(t *testing.T) {
	client, consumer := startStream(t)
	logs, ai := newFakeWriter(logs_core.StreamLogs), newFakeWriter(logs_core.StreamAILogs)
	sink := NewSink(logs, ai, 4<<10, time.Hour, discardLogger())
	projectID := uuid.New()
	const batches = 100
	for range batches {
		publish(t, client, projectID, 5)
	}

	var wg sync.WaitGroup
	for lane := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range batches / 2 {
				handleNextOn(t, client, consumer, sink, broker.LaneShard(projectID, lane), projectID)
			}
		}()
	}
	wg.Wait()
	sink.Release(context.Background(), broker.LaneShard(projectID, 0))
	sink.Release(context.Background(), broker.LaneShard(projectID, 1))

	require.Eventually(t, func() bool { return streamMessages(t, client) == 0 },
		5*time.Second, 50*time.Millisecond, "every batch acked")
	require.Equal(t, batches*5, logs.rowsFlushed(projectID), "every row in a flushed segment")
}

// Flushes that fail drop their rows and leave their batches unacked for
// redelivery; with two lanes of a project interleaving, a batch is acked only
// when a flush that succeeded wrote its rows.
func Test_Sink_ProjectOnTwoShardsWithFailingFlushes_AcksOnlyBatchesWhoseRowsWereWritten(t *testing.T) {
	client, consumer := startStream(t)
	logs, ai := newFakeWriter(logs_core.StreamLogs), newFakeWriter(logs_core.StreamAILogs)
	logs.failFlushEvery = 3
	sink := NewSink(logs, ai, 2<<10, time.Hour, discardLogger())
	projectID := uuid.New()
	const batches = 200
	for range batches {
		publish(t, client, projectID, 5)
	}

	var wg sync.WaitGroup
	for lane := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range batches / 2 {
				handleNextOn(t, client, consumer, sink, broker.LaneShard(projectID, lane), projectID)
			}
		}()
	}
	wg.Wait()
	sink.Release(context.Background(), broker.LaneShard(projectID, 0))
	sink.Release(context.Background(), broker.LaneShard(projectID, 1))

	time.Sleep(200 * time.Millisecond)
	unacked := int(streamMessages(t, client))
	require.Positive(t, unacked, "some flushes failed")
	require.Equal(t, (batches-unacked)*5, logs.rowsFlushed(projectID), "every acked batch's rows written")
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
	return &fakeWriter{stream: stream, buffered: map[uuid.UUID]int{}, flushedRows: map[uuid.UUID]int{}}
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
	w.flushCalls++
	if w.failFlushEvery > 0 && w.flushCalls%w.failFlushEvery == 0 {
		return nil, errors.New("disk full")
	}
	w.flushedRows[projectID] += rows
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

func handleNextOn(
	t *testing.T,
	client *broker.Client,
	consumer jetstream.Consumer,
	sink *ingest.Sink,
	shard int,
	projectID uuid.UUID,
) {
	t.Helper()
	msg, err := consumer.Next(jetstream.FetchMaxWait(5 * time.Second))
	require.NoError(t, err)
	require.NoError(t, sink.Handle(shard, projectID, msg.Data(), broker.NewAck(client.Conn, msg.Reply())))
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

func (w *fakeWriter) rowsFlushed(projectID uuid.UUID) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.flushedRows[projectID]
}
