package broker

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"obzly/internal/cache"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const testTick = 200 * time.Millisecond

// recordingHandler acks every message on Tick and records what it saw.
type recordingHandler struct {
	mu       sync.Mutex
	received map[uuid.UUID]int
	pending  map[int][]Ack
	released map[int]int
}

func Test_ShardRunner_OneWorker_TakesEveryShardAndAcksOnlyWhenTheHandlerDoes(t *testing.T) {
	client := startTestClient(t, 64<<20)
	handler := newRecordingHandler()
	startRunner(t, client, uuid.NewString(), handler)
	projects := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}

	for _, projectID := range projects {
		require.NoError(t, client.PublishIngest(context.Background(), SignalLogs, projectID, [][]byte{[]byte("x")}))
	}

	require.Eventually(t, func() bool {
		return handler.count(projects...) == len(projects)
	}, 10*time.Second, 50*time.Millisecond)
	require.Eventually(t, func() bool {
		return streamMessages(t, client, "INGEST_LOGS") == 0
	}, 10*time.Second, 50*time.Millisecond, "the handler's acks remove the batches")
}

func Test_ShardRunner_TwoWorkers_SplitTheShardsAndReleaseOnStop(t *testing.T) {
	client := startTestClient(t, 64<<20)
	scope := uuid.NewString()
	first, second := newRecordingHandler(), newRecordingHandler()
	firstRunner, stopFirst := startRunner(t, client, scope, first)

	require.Eventually(t, func() bool { return firstRunner.OwnedShards() == IngestShards },
		10*time.Second, 50*time.Millisecond)

	secondRunner, _ := startRunner(t, client, scope, second)
	require.Eventually(t, func() bool {
		return firstRunner.OwnedShards() == IngestShards/2 && secondRunner.OwnedShards() == IngestShards/2
	}, 15*time.Second, 50*time.Millisecond, "a joining worker gets a fair share")
	require.NotEmpty(t, first.releasedShards(), "giving a shard away releases its buffers first")

	stopFirst()
	require.Eventually(t, func() bool { return secondRunner.OwnedShards() == IngestShards },
		15*time.Second, 50*time.Millisecond, "the remaining worker takes the stopped worker's shards")
}

// Compaction follows the ingest shard leases: a worker compacts a project
// only while it holds the project's shard.
func Test_OwnsProject_FollowsTheRegisteredRunnersLeases(t *testing.T) {
	client := startTestClient(t, 64<<20)
	scope := uuid.NewString()
	first, _ := startRunner(t, client, scope, newRecordingHandler())
	RegisterRunner(first)
	t.Cleanup(func() {
		runnersMu.Lock()
		delete(runners, SignalLogs)
		runnersMu.Unlock()
	})

	_, sharded := OwnsProject(SignalSpans, uuid.New())
	require.False(t, sharded, "no runner for spans here: callers fall back to their lease")
	require.Eventually(
		t,
		func() bool { return first.OwnedShards() == IngestShards },
		10*time.Second,
		50*time.Millisecond,
	)
	owns, sharded := OwnsProject(SignalLogs, uuid.New())
	require.True(t, sharded)
	require.True(t, owns, "a lone worker owns every project")

	second, _ := startRunner(t, client, scope, newRecordingHandler())
	require.Eventually(t, func() bool {
		return first.OwnedShards() == IngestShards/2 && second.OwnedShards() == IngestShards/2
	}, 15*time.Second, 50*time.Millisecond)
	owned := 0
	for range 400 {
		if owns, _ := OwnsProject(SignalLogs, uuid.New()); owns {
			owned++
		}
	}
	require.InDelta(t, 200, owned, 60, "about half the projects once a second worker joins")
}

func newRecordingHandler() *recordingHandler {
	return &recordingHandler{received: map[uuid.UUID]int{}, pending: map[int][]Ack{}, released: map[int]int{}}
}

func (h *recordingHandler) Handle(shard int, projectID uuid.UUID, _ []byte, ack Ack) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.received[projectID]++
	h.pending[shard] = append(h.pending[shard], ack)
	return nil
}

func (h *recordingHandler) Tick(_ context.Context, shard int) {
	h.ackAll(shard)
}

func (h *recordingHandler) Release(_ context.Context, shard int) {
	h.ackAll(shard)
	h.mu.Lock()
	h.released[shard]++
	h.mu.Unlock()
}

func (h *recordingHandler) ackAll(shard int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ack := range h.pending[shard] {
		_ = ack.Ack()
	}
	delete(h.pending, shard)
}

func (h *recordingHandler) count(projects ...uuid.UUID) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	total := 0
	for _, projectID := range projects {
		total += h.received[projectID]
	}
	return total
}

func (h *recordingHandler) releasedShards() map[int]int {
	h.mu.Lock()
	defer h.mu.Unlock()
	copied := map[int]int{}
	for shard, n := range h.released {
		copied[shard] = n
	}
	return copied
}

// startRunner runs a ShardRunner until the returned stop (or test end).
func startRunner(t *testing.T, client *Client, scope string, handler ShardHandler) (*ShardRunner, func()) {
	t.Helper()
	runner, err := NewShardRunner(client, cache.GetCache(), SignalLogs, time.Minute, handler,
		slog.New(slog.NewTextHandler(io.Discard, nil)), WithLeaseScope(scope), WithTick(testTick))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runner.Run(ctx)
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
	t.Cleanup(stop)
	return runner, stop
}

func Test_ShardRunner_LargeBatches_FetchesAndAcksThemAll(t *testing.T) {
	client := startTestClient(t, 256<<20)
	handler := newRecordingHandler()
	_, stop := startRunner(t, client, "large", handler)
	defer stop()
	project := uuid.New()
	batch := make([]byte, 400<<10)
	for range 100 {
		require.NoError(t, client.PublishIngest(context.Background(), SignalLogs, project, [][]byte{batch}))
	}

	require.Eventually(t, func() bool { return handler.count(project) == 100 }, 20*time.Second, 50*time.Millisecond,
		"40 MB of 400 KB batches arrive in byte-capped fetches")
}

func Test_NextFetchBatch_BringsAboutTheByteCap(t *testing.T) {
	require.Equal(t, 30, nextFetchBatch(16, 16, 16*(400<<10)), "400 KB batches: ~12 MB per fetch")
	require.Equal(t, maxFetchBatch, nextFetchBatch(16, 16, 16*1024), "small batches: the count cap")
	require.Equal(t, 1, nextFetchBatch(16, 2, 2*(16<<20)), "one batch larger than the cap still comes")
	require.Equal(t, 30, nextFetchBatch(30, 0, 0), "an empty fetch keeps the size")
}

// Sized from tiny messages alone, the next fetch asked for 256: at 1 MB
// each that was 256 MB, past the 64 MB a client may have pending.
func Test_FetchBatchCap_ShardSwitchingToLargeMessages_StaysUnderThePendingLimit(t *testing.T) {
	const clientPendingLimit = 64 << 20

	afterTinyMessages := min(nextFetchBatch(16, 16, 16*1024), fetchBatchCap(1024))
	require.Less(t, afterTinyMessages*MaxIngestPayload, clientPendingLimit)
	require.Greater(t, afterTinyMessages, firstFetchBatch, "small messages still come many per fetch")

	require.LessOrEqual(t, fetchBatchCap(MaxIngestPayload)*MaxIngestPayload, readMaxBytes,
		"once seen, 1 MB messages are fetched about readMaxBytes at a time")
	require.Equal(t, 1, fetchBatchCap(maxServerPayload), "a message over the cap still comes, alone")
}

func Test_Poisoned_NeedsManyDeliveriesAndADay(t *testing.T) {
	now := time.Now().UTC()

	require.True(t, poisoned(poisonDeliveries, now.Add(-poisonAge), now))
	require.False(t, poisoned(poisonDeliveries, now.Add(-time.Hour), now), "an outage under a day loses nothing")
	require.False(t, poisoned(1, now.Add(-3*poisonAge), now), "a backlog's first delivery is written")
}
