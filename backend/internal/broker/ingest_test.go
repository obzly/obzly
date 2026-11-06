package broker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func Test_PublishIngest_StoresEveryPayloadOnTheProjectsShardSubject(t *testing.T) {
	client := startTestClient(t, 64<<20)
	projectID := uuid.New()

	err := client.PublishIngest(context.Background(), SignalLogs, projectID, [][]byte{[]byte("a"), []byte("b")})
	require.NoError(t, err)

	stream, err := client.JetStream.Stream(context.Background(), "INGEST_LOGS")
	require.NoError(t, err)
	msg, err := stream.GetMsg(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, IngestSubject(SignalLogs, projectID), msg.Subject)
	parsed, err := ProjectFromSubject(msg.Subject)
	require.NoError(t, err)
	require.Equal(t, projectID, parsed)
	info, err := stream.Info(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 2, info.State.Msgs)
}

func Test_PublishIngest_WhenStreamFull_ReturnsErrIngestFull(t *testing.T) {
	client := startTestClient(t, 4096)
	payload := []byte(strings.Repeat("x", 1024))

	var err error
	for i := 0; i < 20 && err == nil; i++ {
		err = client.PublishIngest(context.Background(), SignalLogs, uuid.New(), [][]byte{payload})
	}

	require.ErrorIs(t, err, ErrIngestFull)
}

func Test_PublishIngest_WhenBrokerStopped_ReturnsErrIngestUnavailable(t *testing.T) {
	srv := startTestServer(t, ServerConfig{Name: "all-1", StoreDir: t.TempDir(), InProcess: true})
	client, err := ConnectInProcess(srv, "test")
	require.NoError(t, err)
	t.Cleanup(client.Close)
	require.NoError(t, client.EnsureIngestStreams(context.Background(), StreamSettings{Replicas: 1, MaxBytes: 1 << 20}))
	srv.Shutdown()
	srv.WaitForShutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = client.PublishIngest(ctx, SignalLogs, uuid.New(), [][]byte{[]byte("a")})

	require.ErrorIs(t, err, ErrIngestUnavailable)
}

func Test_SplitPayloads_PacksItemsIntoJSONArraysUnderTheLimit(t *testing.T) {
	item := []byte(`"` + strings.Repeat("x", 100_000) + `"`)
	items := make([][]byte, 25)
	for i := range items {
		items[i] = item
	}

	payloads := SplitPayloads(items)

	require.Greater(t, len(payloads), 1)
	total := 0
	for _, payload := range payloads {
		require.LessOrEqual(t, len(payload), MaxIngestPayload)
		var decoded []string
		require.NoError(t, json.Unmarshal(payload, &decoded))
		total += len(decoded)
	}
	require.Equal(t, 25, total)
}

func Test_SplitPayloads_ItemLargerThanTheLimit_GetsItsOwnPayload(t *testing.T) {
	large := []byte(`"` + strings.Repeat("x", MaxIngestPayload) + `"`)

	payloads := SplitPayloads([][]byte{[]byte(`"a"`), large, []byte(`"b"`)})

	require.Len(t, payloads, 3)
	require.Equal(t, `["a"]`, string(payloads[0]))
}

func Test_ProjectFromSubject_RejectsNonIngestSubjects(t *testing.T) {
	for _, subject := range []string{"tail.x", "ingest.logs.x." + uuid.NewString(), "ingest.logs.1"} {
		_, err := ProjectFromSubject(subject)
		require.Error(t, err, subject)
	}
}

func Test_ShardFor_IsStableAndInRange(t *testing.T) {
	projectID := uuid.New()
	shard := ShardFor(projectID)
	require.Equal(t, shard, ShardFor(projectID))
	require.GreaterOrEqual(t, shard, 0)
	require.Less(t, shard, IngestShards)
}

func startTestClient(t *testing.T, maxBytes int64) *Client {
	t.Helper()
	srv := startTestServer(t, ServerConfig{Name: "all-1", StoreDir: t.TempDir(), InProcess: true})
	client, err := ConnectInProcess(srv, "test")
	require.NoError(t, err)
	t.Cleanup(client.Close)
	require.NoError(
		t,
		client.EnsureIngestStreams(context.Background(), StreamSettings{Replicas: 1, MaxBytes: maxBytes}),
	)
	return client
}

func streamMessages(t *testing.T, client *Client, stream string) uint64 {
	t.Helper()
	s, err := client.JetStream.Stream(context.Background(), stream)
	require.NoError(t, err)
	info, err := s.Info(context.Background())
	require.NoError(t, err)
	return info.State.Msgs
}
