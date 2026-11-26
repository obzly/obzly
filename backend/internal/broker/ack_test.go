package broker

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
)

// JetStream confirms each ack, and acking a message again (a retry whose
// first attempt did land) is confirmed too, so retries end.
func Test_AckAll_ConfirmedAcks_RemoveTheMessagesAndRetriesAreConfirmedToo(t *testing.T) {
	client := startTestClient(t, 1<<20)
	acks := fetchAcks(t, client, 3)

	require.Empty(t, AckAll(acks))
	require.Zero(t, streamMessages(t, client, "INGEST_LOGS"))
	require.Empty(t, AckAll(acks), "acked again")
}

// With the broker gone, nothing is confirmed: the sink keeps the acks to
// retry instead of losing them.
func Test_AckAll_BrokerGone_ReturnsEveryAckUnconfirmed(t *testing.T) {
	srv := startTestServer(t, ServerConfig{Name: "all-1", StoreDir: t.TempDir(), InProcess: true})
	client, err := ConnectInProcess(srv, "test")
	require.NoError(t, err)
	t.Cleanup(client.Close)
	require.NoError(t, client.EnsureIngestStreams(context.Background(), StreamSettings{Replicas: 1, MaxBytes: 1 << 20}))
	acks := fetchAcks(t, client, 2)
	srv.Shutdown()
	srv.WaitForShutdown()

	require.Len(t, AckAll(acks), 2)
}

func fetchAcks(t *testing.T, client *Client, count int) []Ack {
	t.Helper()
	ctx := context.Background()
	projectID := uuid.New()
	for range count {
		require.NoError(t, client.PublishIngest(ctx, SignalLogs, projectID, [][]byte{[]byte("[]")}))
	}
	consumer, err := client.JetStream.CreateOrUpdateConsumer(ctx, "INGEST_LOGS", jetstream.ConsumerConfig{
		Durable: "ack-test", AckPolicy: jetstream.AckExplicitPolicy, AckWait: time.Minute,
	})
	require.NoError(t, err)
	batch, err := consumer.Fetch(count, jetstream.FetchMaxWait(2*time.Second))
	require.NoError(t, err)
	var acks []Ack
	for msg := range batch.Messages() {
		acks = append(acks, NewAck(client.Conn, msg.Reply()))
	}
	require.Len(t, acks, count)
	return acks
}
