package broker

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
)

func Test_PendingIngest_ReturnsOnlyTheProjectsUnackedBatchesInOrder(t *testing.T) {
	client := startTestClient(t, 64<<20)
	ctx := context.Background()
	project, other := uuid.New(), uuid.New()
	for i := range 300 {
		require.NoError(t, client.PublishIngest(ctx, SignalLogs, project, [][]byte{fmt.Appendf(nil, "p-%d", i)}))
	}
	require.NoError(t, client.PublishIngest(ctx, SignalLogs, other, [][]byte{[]byte("other")}))

	pending, err := client.PendingIngest(ctx, SignalLogs, project, 64<<20)

	require.NoError(t, err)
	require.Len(t, pending, 300, "more than one direct-get batch")
	require.Equal(t, "p-0", string(pending[0].Data))
	require.Equal(t, "p-299", string(pending[299].Data))

	consumer, err := client.JetStream.CreateOrUpdateConsumer(ctx, "INGEST_LOGS", jetstream.ConsumerConfig{
		Durable: "ack-first", AckPolicy: jetstream.AckExplicitPolicy,
		FilterSubject: IngestSubject(SignalLogs, project),
	})
	require.NoError(t, err)
	msg, err := consumer.Next(jetstream.FetchMaxWait(2 * time.Second))
	require.NoError(t, err)
	require.NoError(t, msg.DoubleAck(ctx))

	pending, err = client.PendingIngest(ctx, SignalLogs, project, 64<<20)
	require.NoError(t, err)
	require.Len(t, pending, 299, "an acked batch is in a segment, no longer pending")
	require.Equal(t, "p-1", string(pending[0].Data))
}

func Test_PendingIngest_OverTheByteLimit_ReturnsTheOldestAndErrPendingTooLarge(t *testing.T) {
	client := startTestClient(t, 64<<20)
	ctx := context.Background()
	project := uuid.New()
	for range 5 {
		require.NoError(t, client.PublishIngest(ctx, SignalLogs, project, [][]byte{make([]byte, 100)}))
	}

	pending, err := client.PendingIngest(ctx, SignalLogs, project, 250)

	require.ErrorIs(t, err, ErrPendingTooLarge)
	require.Len(t, pending, 2)
}

func Test_PendingIngest_NothingPending_ReturnsEmpty(t *testing.T) {
	client := startTestClient(t, 64<<20)

	pending, err := client.PendingIngest(context.Background(), SignalLogs, uuid.New(), 64<<20)

	require.NoError(t, err)
	require.Empty(t, pending)
}

func Test_PendingIngest_LargeBatches_ReadsEveryOneInRequestsUnderTheByteCap(t *testing.T) {
	client := startTestClient(t, 256<<20)
	ctx := context.Background()
	project := uuid.New()
	batch := make([]byte, 400<<10)
	for range 100 {
		require.NoError(t, client.PublishIngest(ctx, SignalLogs, project, [][]byte{batch}))
	}
	stream, err := streamFor(SignalLogs)
	require.NoError(t, err)

	first, more, err := client.directGetBatch(ctx, stream, IngestSubject(SignalLogs, project), 1, 0, pendingBatch)
	require.NoError(t, err)
	require.True(t, more)
	size := 0
	for _, msg := range first {
		size += len(msg.Data)
	}
	// The server stops once the cap is reached, so the last batch may cross it.
	require.LessOrEqual(t, size, readMaxBytes+len(batch), "one direct get stays near the cap, not 256 batches (100 MB)")

	pending, err := client.PendingIngest(ctx, SignalLogs, project, 256<<20)
	require.NoError(t, err)
	require.Len(t, pending, 100)
}
