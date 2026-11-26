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

// The lab's case end to end, with real leases: one worker holds every shard,
// three of them busy. A second worker joins; idle shards spread by count,
// the busy ones end up two and one, every batch is still handled and acked.
func Test_Balancer_WorkerJoins_SpreadsBusyAndIdleShardsWithoutLosingBatches(t *testing.T) {
	client := startTestClient(t, 64<<20)
	leaseScope, balanceScope := uuid.NewString(), uuid.NewString()
	busy := map[int]bool{1: true, 2: true, 3: true}
	loadOf := func(ref shardRef) (float64, bool) {
		if busy[ref.Shard] {
			return 3 * mb, true
		}
		return 0, true
	}
	first, second := newRecordingHandler(), newRecordingHandler()
	firstRunner := startBalancedWorker(t, client, leaseScope, balanceScope, first, loadOf)
	require.Eventually(t, func() bool { return firstRunner.OwnedShards() == IngestShards },
		10*time.Second, 50*time.Millisecond, "a lone worker claims every shard")

	projects := make([]uuid.UUID, 40)
	for i := range projects {
		projects[i] = uuid.New()
		require.NoError(t, client.PublishIngest(context.Background(), SignalLogs, projects[i], [][]byte{[]byte("x")}))
	}

	secondRunner := startBalancedWorker(t, client, leaseScope, balanceScope, second, loadOf)
	require.Eventually(t, func() bool {
		a, b := firstRunner.OwnedShards(), secondRunner.OwnedShards()
		busyA, busyB := ownedOf(firstRunner, busy), ownedOf(secondRunner, busy)
		return a+b == IngestShards && a >= IngestShards/2-2 && b >= IngestShards/2-2 &&
			busyA+busyB == 3 && busyA >= 1 && busyB >= 1
	}, 20*time.Second, 100*time.Millisecond, "idle shards split by count, busy ones two and one")

	for _, projectID := range projects {
		require.NoError(t, client.PublishIngest(context.Background(), SignalLogs, projectID, [][]byte{[]byte("y")}))
	}
	require.Eventually(t, func() bool {
		for _, projectID := range projects {
			if first.count(projectID)+second.count(projectID) < 2 {
				return false
			}
		}
		return streamMessages(t, client, "INGEST_LOGS") == 0
	}, 15*time.Second, 50*time.Millisecond, "every batch handled and acked across the moves")
}

// A project with three lanes is written by whichever workers own its lane
// shards: each worker handles exactly the batches of the lanes it holds, and
// every batch is acked.
func Test_Lanes_ProjectAcrossThreeWorkers_EachWritesItsLanesBatches(t *testing.T) {
	client := startTestClient(t, 64<<20)
	leaseScope, balanceScope := uuid.NewString(), uuid.NewString()
	idle := func(shardRef) (float64, bool) { return 0, true }
	handlers := []*recordingHandler{newRecordingHandler(), newRecordingHandler(), newRecordingHandler()}
	var runners []*ShardRunner
	for _, handler := range handlers {
		runners = append(runners, startBalancedWorker(t, client, leaseScope, balanceScope, handler, idle))
	}
	require.Eventually(t, func() bool {
		total := 0
		for _, runner := range runners {
			owned := runner.OwnedShards()
			if owned < IngestShards/3-1 {
				return false
			}
			total += owned
		}
		return total == IngestShards
	}, 30*time.Second, 100*time.Millisecond, "the idle shards spread over the three workers")
	project := uuid.New()
	withLanes(t, SignalLogs, project, 3)

	for range 60 {
		require.NoError(t, client.PublishIngest(context.Background(), SignalLogs, project, [][]byte{[]byte("x")}))
	}

	require.Eventually(t, func() bool { return streamMessages(t, client, "INGEST_LOGS") == 0 },
		15*time.Second, 50*time.Millisecond, "every lane's batches handled and acked")
	for i, runner := range runners {
		lanes := 0
		for lane := range 3 {
			if runner.isOwned(LaneShard(project, lane)) {
				lanes++
			}
		}
		require.Equal(t, 20*lanes, handlers[i].count(project), "worker %d holds %d lanes", i, lanes)
	}
}

// INGEST_SHARD_BALANCE=count leaves runners as before: no balancer, an even
// split by count whatever the load.
func Test_Balancer_NotStarted_RunnersSplitByCount(t *testing.T) {
	client := startTestClient(t, 64<<20)
	scope := uuid.NewString()
	firstRunner, _ := startRunner(t, client, scope, newRecordingHandler())
	secondRunner, _ := startRunner(t, client, scope, newRecordingHandler())

	require.Eventually(t, func() bool {
		return firstRunner.OwnedShards() == IngestShards/2 && secondRunner.OwnedShards() == IngestShards/2
	}, 15*time.Second, 50*time.Millisecond)
	require.False(t, firstRunner.balanced.Load())
}

// A handoff carries the shard's load, so the receiver starts from it; a
// handoff written by an older build without one reads as no load.
func Test_ParseHandoff_ReadsReceiverAndLoad(t *testing.T) {
	to, load := parseHandoff("member-b|2090295")
	require.Equal(t, "member-b", to)
	require.InDelta(t, 2090295, load, 0)
	to, load = parseHandoff("member-c")
	require.Equal(t, "member-c", to)
	require.Zero(t, load)
}

// startBalancedWorker runs a logs runner and its Balancer with shortened
// thresholds and the given loads, until the test ends.
func startBalancedWorker(
	t *testing.T, client *Client, leaseScope, balanceScope string, handler ShardHandler,
	loadOf func(shardRef) (float64, bool),
) *ShardRunner {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner, err := NewShardRunner(client, cache.GetCache(), SignalLogs, time.Minute, handler, log,
		WithLeaseScope(leaseScope), WithTick(testTick))
	require.NoError(t, err)
	params := defaultPlanParams
	params.ShedCooldown, params.ShardMinAge, params.ClaimGrace = 500*time.Millisecond, 500*time.Millisecond, 2*testTick
	params.Noise = 64 << 10
	balancer := NewBalancer(cache.GetCache(), []*ShardRunner{runner}, log,
		WithBalancerScope(balanceScope), withPlanParams(params), withLoadOf(loadOf))

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		balancer.Run(ctx)
	}()
	go func() {
		defer wg.Done()
		runner.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})
	return runner
}

func ownedOf(runner *ShardRunner, shards map[int]bool) int {
	n := 0
	for shard := range shards {
		if runner.isOwned(shard) {
			n++
		}
	}
	return n
}

// withPlanParams replaces the thresholds.
func withPlanParams(params planParams) BalancerOption {
	return func(b *Balancer) { b.params = params }
}

// withLoadOf replaces sampled loads.
func withLoadOf(loadOf func(shardRef) (float64, bool)) BalancerOption {
	return func(b *Balancer) { b.loadOf = loadOf }
}
