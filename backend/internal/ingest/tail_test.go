package ingest

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"obzly/internal/broker"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
)

const testStream = "rows"

// fakeDeltas is a DeltaWriter recording each delta's payloads in its file
// name (no file is written); a payload starting with "slow" blocks until
// release is closed.
type fakeDeltas struct {
	dir     string
	release chan struct{}
}

// A query on a tail that is still usable but older than TailRefreshAfter
// gets the batches published since: it used to be served the old files
// while a refresh ran in the background, missing everything since the
// replica was last queried.
func Test_TailFiles_StaleTail_QueryGetsBatchesPublishedJustBefore(t *testing.T) {
	client := startTailBroker(t)
	tail := newTestTail(t, &fakeDeltas{})
	project := uuid.New()
	publish(t, client, project, "first")
	require.Equal(t, []string{"first"}, tailPayloads(t, tail, project, time.Time{}))

	time.Sleep(TailRefreshAfter + 100*time.Millisecond)
	publish(t, client, project, "second")

	require.Equal(t, []string{"first", "second"}, tailPayloads(t, tail, project, time.Time{}))
}

// A segment uploaded after the tail's last refresh holds rows of it: the
// query waits for a refresh that drops the batches a worker acked and reads
// what is pending now, so nothing is counted twice or missed.
func Test_TailFiles_SegmentNewerThanTheTail_DropsAckedBatchesAndAddsNewOnes(t *testing.T) {
	client := startTailBroker(t)
	tail := newTestTail(t, &fakeDeltas{})
	project := uuid.New()
	publish(t, client, project, "first")
	require.Equal(t, []string{"first"}, tailPayloads(t, tail, project, time.Time{}))

	ackOldest(t, client, project)
	uploaded := time.Now().UTC()
	publish(t, client, project, "second")

	require.Equal(t, []string{"second"}, tailPayloads(t, tail, project, uploaded))
}

// A refresh writing a long pending window serves each chunk once written: a
// query waits past TailWaitForRefresh while chunks are being written, up to
// TailMaxWaitForRefresh, then gets the chunks done rather than nothing; the
// next query gets the rest.
func Test_TailFiles_ChunkStillWriting_QueryGetsTheChunksDoneAfterTheLongerWait(t *testing.T) {
	client := startTailBroker(t)
	deltas := &fakeDeltas{release: make(chan struct{})}
	tail := newTestTail(t, deltas)
	tail.wait, tail.maxWait, tail.minChunk = 50*time.Millisecond, 400*time.Millisecond, 1
	project := uuid.New()
	for _, payload := range []string{"one", "two", "slow-three", "four"} {
		publish(t, client, project, payload)
	}

	started := time.Now()
	got := tailPayloads(t, tail, project, time.Time{})

	require.Equal(t, []string{"one", "two", "four"}, got)
	require.GreaterOrEqual(t, time.Since(started), tail.maxWait, "waited the longer wait while writing")
	close(deltas.release)
	require.Eventually(t, func() bool {
		return slices.Equal(tailPayloads(t, tail, project, time.Time{}), []string{"one", "two", "slow-three", "four"})
	}, 5*time.Second, 20*time.Millisecond)
}

// A refresh still running from before a segment became readable may serve
// rows of it: the query waits for it to end and for a new one, which drops
// the batches the worker acked.
func Test_TailFiles_RefreshRunningFromBeforeASegment_WaitsForTheNextOne(t *testing.T) {
	client := startTailBroker(t)
	deltas := &fakeDeltas{release: make(chan struct{})}
	tail := newTestTail(t, deltas)
	tail.wait, tail.maxWait, tail.minChunk = 50*time.Millisecond, 100*time.Millisecond, 1
	project := uuid.New()
	publish(t, client, project, "slow-first")
	require.Empty(t, tailPayloads(t, tail, project, time.Time{}), "still writing")

	ackOldest(t, client, project)
	uploaded := time.Now().UTC()
	publish(t, client, project, "second")
	tail.wait, tail.maxWait = 5*time.Second, 5*time.Second
	go func() {
		time.Sleep(100 * time.Millisecond)
		close(deltas.release)
	}()

	require.Equal(t, []string{"second"}, tailPayloads(t, tail, project, uploaded))
}

// A refresh splits a long pending window into one chunk per writer, in
// order, none under the minimum.
func Test_Chunks_SplitsByPayloadSizeInOrder(t *testing.T) {
	tail := &Tail{minChunk: 10}
	var batches []broker.PendingMessage
	for seq := range uint64(9) {
		batches = append(batches, broker.PendingMessage{Seq: seq + 1, Data: make([]byte, 10)})
	}

	chunks := tail.chunks(batches)

	require.Len(t, chunks, tailWriters)
	var seqs []uint64
	for _, chunk := range chunks {
		require.NotEmpty(t, chunk)
		for _, msg := range chunk {
			seqs = append(seqs, msg.Seq)
		}
	}
	require.Equal(t, []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9}, seqs)
	require.Len(t, (&Tail{minChunk: 100}).chunks(batches), 1, "under two chunks' worth: one")
}

func (f *fakeDeltas) write(projectID uuid.UUID, payloads [][]byte) (map[string]*segments.Segment, error) {
	names := make([]string, len(payloads))
	for i, payload := range payloads {
		names[i] = string(payload)
		if strings.HasPrefix(names[i], "slow") {
			<-f.release
		}
	}
	now := time.Now().UTC()
	return map[string]*segments.Segment{testStream: {
		ProjectID: projectID, Path: filepath.Join(f.dir, strings.Join(names, ",")), MinTime: now, MaxTime: now,
	}}, nil
}

func newTestTail(t *testing.T, deltas *fakeDeltas) *Tail {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	deltas.dir = t.TempDir()
	tail, err := NewTail(broker.SignalLogs, filepath.Join(t.TempDir(), "tail"),
		func() (DeltaWriter, error) { return deltas.write, nil }, log)
	require.NoError(t, err)
	return tail
}

func startTailBroker(t *testing.T) *broker.Client {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	client, err := broker.StartInProcess("tail-"+uuid.NewString()[:8], t.TempDir(), log)
	require.NoError(t, err)
	require.NoError(t, client.EnsureIngestStreams(context.Background(),
		broker.StreamSettings{Replicas: 1, MaxBytes: 64 << 20}))
	broker.SetDefault(client)
	t.Cleanup(broker.ShutdownDefault)
	return client
}

func publish(t *testing.T, client *broker.Client, project uuid.UUID, payload string) {
	t.Helper()
	_, err := client.JetStream.Publish(context.Background(), broker.IngestSubject(broker.SignalLogs, project),
		[]byte(payload))
	require.NoError(t, err)
}

// ackOldest acks project's oldest batch as its shard's worker would.
func ackOldest(t *testing.T, client *broker.Client, project uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	consumer, err := client.JetStream.CreateOrUpdateConsumer(ctx, "INGEST_LOGS", jetstream.ConsumerConfig{
		Durable: "tail-test-" + uuid.NewString()[:8], AckPolicy: jetstream.AckExplicitPolicy,
		FilterSubject: broker.IngestSubject(broker.SignalLogs, project),
	})
	require.NoError(t, err)
	batch, err := consumer.Fetch(1, jetstream.FetchMaxWait(2*time.Second))
	require.NoError(t, err)
	for msg := range batch.Messages() {
		require.NoError(t, msg.DoubleAck(ctx))
	}
}

// tailPayloads is the payloads the query's tail files hold, in order.
func tailPayloads(t *testing.T, tail *Tail, project uuid.UUID, newestReadable time.Time) []string {
	t.Helper()
	var payloads []string
	for _, file := range tail.TailFiles(context.Background(), project, testStream, newestReadable) {
		payloads = append(payloads, strings.Split(filepath.Base(file.Path), ",")...)
	}
	return payloads
}
