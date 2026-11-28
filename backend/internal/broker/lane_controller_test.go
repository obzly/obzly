package broker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"obzly/internal/cache"
	"obzly/internal/storage"
	"obzly/internal/util/leaderlock"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A load needing three lanes gets them only after it has lasted GrowAfter.
func Test_NextLanes_GrowsOnlyAfterTheLoadLasts(t *testing.T) {
	start := time.Now()
	lanes, state := nextLanes(1, 2.5*mb, 1*mb, laneLag{}, laneState{}, start, defaultLaneParams)
	require.Equal(t, 1, lanes)
	lanes, state = nextLanes(lanes, 2.5*mb, 1*mb, laneLag{}, state, start.Add(9*time.Minute), defaultLaneParams)
	require.Equal(t, 1, lanes, "not yet")

	lanes, state = nextLanes(lanes, 2.5*mb, 1*mb, laneLag{}, state, start.Add(10*time.Minute), defaultLaneParams)

	require.Equal(t, 3, lanes)
	require.NotZero(t, state.Changed)
}

// A burst that ends before GrowAfter resets the timer.
func Test_NextLanes_BurstShorterThanGrowAfter_ChangesNothing(t *testing.T) {
	start := time.Now()
	lanes, state := nextLanes(1, 3*mb, 1*mb, laneLag{}, laneState{}, start, defaultLaneParams)
	lanes, state = nextLanes(lanes, 0.5*mb, 1*mb, laneLag{}, state, start.Add(5*time.Minute), defaultLaneParams)
	lanes, _ = nextLanes(lanes, 3*mb, 1*mb, laneLag{}, state, start.Add(11*time.Minute), defaultLaneParams)

	require.Equal(t, 1, lanes)
}

// Lanes come down one at a time, only once the load has been below the
// smaller count's worth with margin for ShrinkAfter.
func Test_NextLanes_ShrinksByOneAfterALongLull(t *testing.T) {
	start := time.Now()
	old := laneState{Changed: start.Add(-time.Hour).UnixMilli()}
	lanes, state := nextLanes(3, 1.5*mb, 1*mb, laneLag{}, old, start, defaultLaneParams)
	require.Equal(t, 3, lanes, "1.5 MB/s is above 2 lanes x 0.6: no shrink")
	require.Zero(t, state.BelowSince)

	lanes, state = nextLanes(3, 1.1*mb, 1*mb, laneLag{}, old, start, defaultLaneParams)
	lanes, state = nextLanes(lanes, 1.1*mb, 1*mb, laneLag{}, state, start.Add(29*time.Minute), defaultLaneParams)
	require.Equal(t, 3, lanes)
	lanes, _ = nextLanes(lanes, 1.1*mb, 1*mb, laneLag{}, state, start.Add(30*time.Minute), defaultLaneParams)

	require.Equal(t, 2, lanes)
}

// Never more than Max lanes, and no second change within ChangeEvery.
func Test_NextLanes_CapsAndCoolsDown(t *testing.T) {
	start := time.Now()
	params := defaultLaneParams
	params.Max = 4
	lanes, state := nextLanes(1, 20*mb, 1*mb, laneLag{}, laneState{AboveSince: start.Add(-time.Hour).UnixMilli()}, start, params)
	require.Equal(t, 4, lanes)

	recent := laneState{Changed: start.UnixMilli(), BelowSince: start.Add(-time.Hour).UnixMilli()}
	lanes, _ = nextLanes(lanes, 0, 1*mb, laneLag{}, recent, start.Add(5*time.Minute), params)
	require.Equal(t, 4, lanes, "changed 5 minutes ago")
	_ = state
}

// Every lane behind keeps falling behind whatever the load estimate says:
// the project gets one lane more once that has lasted GrowAfter, then another.
func Test_NextLanes_EveryLaneBehindWithLoadUnderTarget_GrowsByOneAtATime(t *testing.T) {
	start := time.Now()
	stuck := laneLag{Stuck: true, Behind: true}
	lanes, state := nextLanes(1, 0.6*mb, 1*mb, stuck, laneState{}, start, defaultLaneParams)
	require.Equal(t, 1, lanes)

	lanes, state = nextLanes(lanes, 0.6*mb, 1*mb, stuck, state, start.Add(10*time.Minute), defaultLaneParams)
	require.Equal(t, 2, lanes)

	lanes, state = nextLanes(lanes, 0.9*mb, 1*mb, stuck, state, start.Add(20*time.Minute), defaultLaneParams)
	require.Equal(t, 2, lanes, "behind again from now")
	lanes, _ = nextLanes(lanes, 0.9*mb, 1*mb, stuck, state, start.Add(30*time.Minute), defaultLaneParams)
	require.Equal(t, 3, lanes)
}

// A project with a lane behind keeps its lanes however low its load reads:
// one lane fewer could leave receivers only the lane that is behind.
func Test_NextLanes_ALaneBehindDuringALull_NeverShrinks(t *testing.T) {
	start := time.Now()
	state := laneState{BelowSince: start.Add(-time.Hour).UnixMilli()}
	params := defaultLaneParams
	params.Max = 3

	lanes, _ := nextLanes(3, 0.1*mb, 1*mb, laneLag{Behind: true}, state, start, params)

	require.Equal(t, 3, lanes)
}

// One lane behind among others that keep up gets no new lane: receivers send
// the batches to the others while it drains. In the homelab the controller
// went from 2 to 6 lanes on one backlogged lane, none of which helped it.
func Test_NextLanes_OneLaneBehindOfSeveral_DoesNotGrow(t *testing.T) {
	start := time.Now()
	project := uuid.New()
	lag := lagOf(project, 3, map[int]bool{LaneShard(project, 0): true}, true)
	require.Equal(t, laneLag{Behind: true}, lag)

	lanes, state := nextLanes(3, 0.6*mb, 1*mb, lag, laneState{}, start, defaultLaneParams)
	lanes, _ = nextLanes(lanes, 0.6*mb, 1*mb, lag, state, start.Add(time.Hour), defaultLaneParams)

	require.Equal(t, 3, lanes)
	every := map[int]bool{LaneShard(project, 0): true, LaneShard(project, 1): true, LaneShard(project, 2): true}
	require.Equal(t, laneLag{Stuck: true, Behind: true}, lagOf(project, 3, every, true))
	require.Equal(t, laneLag{Behind: true}, lagOf(project, 3, every, false), "not the busiest on any of them")
}

// Of the projects with a lane on a shard that is behind, the busiest gets
// picked; a shard keeping up picks nobody.
func Test_BehindProjects_PicksTheBusiestProjectWithALaneOnTheShard(t *testing.T) {
	busy, light, elsewhere := uuid.New(), uuid.New(), uuid.New()
	for ShardFor(light) != ShardFor(busy) {
		light = uuid.New()
	}
	for ShardFor(elsewhere) == ShardFor(busy) {
		elsewhere = uuid.New()
	}
	loads := map[uuid.UUID]float64{busy: 0.6 * mb, light: 0.1 * mb, elsewhere: 2 * mb}

	picked := behindProjects(map[int]bool{ShardFor(busy): true}, loads, func(uuid.UUID) int { return 1 })

	assert.Equal(t, map[uuid.UUID]bool{busy: true}, picked)
}

// A shard's seconds behind are its undelivered batches over its smoothed
// drain rate; one draining nothing reports 0.
func Test_SampleBehind_UndeliveredBatchesOverDrainRate(t *testing.T) {
	balancer := &Balancer{drain: map[shardRef]float64{}}
	ref := shardRef{SignalMetrics, 15}

	behind := balancer.sampleBehind(ref, ownedLoad{messages: 40, pending: 12_000}, 10, 0.1, false)
	require.InDelta(t, 3000, behind, 0.1, "4 batches a second, 12,000 waiting")

	stalled := balancer.sampleBehind(shardRef{SignalMetrics, 16}, ownedLoad{pending: 500}, 10, 0.1, false)
	require.Zero(t, stalled)
}

// The controller raises a busy project's lanes in the store once its load
// has lasted, and leaves a quiet one alone.
func Test_ControlSignalLanes_RaisesABusyProjectsLanesInTheStore(t *testing.T) {
	balancer, signal := newLaneTestBalancer(t)
	busy, quiet := uuid.New(), uuid.New()
	loads := map[uuid.UUID]float64{busy: 2.5 * mb, quiet: 0.2 * mb}
	start := time.Now()

	runController(t, balancer, signal, loads, start)
	runController(t, balancer, signal, loads, start.Add(11*time.Minute))

	assert.Equal(t, map[uuid.UUID]int{busy: 3}, storedLanes(t, balancer, signal))
	assert.Equal(t, 3, balancer.lanes.Lanes(signal, busy), "this process routes by it at once")
}

// Valkey restarting empty (a node-kill drill) loses the controller's timers
// and the backlog, but not the lane counts: a receiver starting afterwards
// spreads the busy project over its lanes at once, and the controller keeps
// them. With the counts only in Valkey the project went back to one lane and
// its shard built a 919-batch backlog before the controller grew it again.
func Test_LaneCounts_ValkeyRestartsEmpty_LanesComeBackWithoutABacklog(t *testing.T) {
	client := cache.GetCache()
	ctx := context.Background()
	balancer, signal := newLaneTestBalancer(t)
	busy := uuid.New()
	loads := map[uuid.UUID]float64{busy: 2.5 * mb}
	start := time.Now()
	runController(t, balancer, signal, loads, start)
	runController(t, balancer, signal, loads, start.Add(11*time.Minute))
	require.Equal(t, map[uuid.UUID]int{busy: 3}, storedLanes(t, balancer, signal))

	require.NoError(t, client.Do(ctx, client.B().Del().
		Key(laneStateKeyBase+signal, BacklogKey(signal), "obzly:ingest:lanes:"+signal).Build()).Error())

	receiver := NewLaneCache(balancer.lanes.store, client, discardLog())
	receiver.refresh(ctx, time.Now().UTC())
	require.Equal(t, 3, receiver.Lanes(signal, busy))
	seen := map[int]bool{}
	for range 3 {
		seen[receiver.shardFor(signal, busy)] = true
	}
	require.Len(t, seen, 3, "batches spread over every lane")

	// The first run after the restart has no timers: a load that would have
	// shrunk the project (after ShrinkAfter) changes nothing yet.
	runController(t, balancer, signal, map[uuid.UUID]float64{busy: 0.5 * mb}, start.Add(12*time.Minute))
	assert.Equal(t, map[uuid.UUID]int{busy: 3}, storedLanes(t, balancer, signal))
}

// A receiver that can't read the store keeps its last lane counts rather
// than putting every project back on one lane.
func Test_LaneCache_StoreUnreachable_KeepsLastCounts(t *testing.T) {
	project := uuid.New()
	store := &fakeLaneStore{counts: map[string]map[uuid.UUID]int{SignalLogs: {project: 3}}}
	lanes := NewLaneCache(store, nil, discardLog())
	now := time.Now().UTC()
	lanes.refresh(context.Background(), now)
	require.Equal(t, 3, lanes.Lanes(SignalLogs, project))

	store.err = errors.New("connection refused")
	lanes.refresh(context.Background(), now.Add(time.Minute))

	assert.Equal(t, 3, lanes.Lanes(SignalLogs, project))
}

// The worker holding the controller's lease publishes every tick how far
// behind each shard is, leaving out shards that keep up, with an expiry; it
// clears the key once none is behind.
func Test_ControlLanes_PublishesTheBacklogEveryTick(t *testing.T) {
	client := cache.GetCache()
	ctx := context.Background()
	balancer, signal := newLaneTestBalancer(t)
	balancer.runners = map[string]*ShardRunner{signal: {scope: balancer.scope}}
	balancer.lastLaneRun = time.Now()
	input := planInput{Now: time.Now().UTC(), Workers: []planWorker{{ID: "w1", Shards: []planShard{
		{shardRef: shardRef{signal, 15}, Behind: 72},
		{shardRef: shardRef{signal, 32}, Behind: 4},
		{shardRef: shardRef{signal, 40}, Behind: 1},
	}}}}

	balancer.controlLanes(ctx, input, map[string]workerReport{})

	listed, err := client.Do(ctx, client.B().Get().Key(BacklogKey(signal)).Build()).ToString()
	require.NoError(t, err)
	assert.Equal(t, "15:72,32:4", listed)
	ttl, err := client.Do(ctx, client.B().Pttl().Key(BacklogKey(signal)).Build()).AsInt64()
	require.NoError(t, err)
	assert.Positive(t, ttl)

	input.Workers[0].Shards = nil
	balancer.controlLanes(ctx, input, map[string]workerReport{})

	exists, err := client.Do(ctx, client.B().Exists().Key(BacklogKey(signal)).Build()).AsInt64()
	require.NoError(t, err)
	assert.Zero(t, exists)
}

// While any registered shard runner belongs to a worker that can't write
// lanes (an older version), the controller changes nothing.
func Test_AllWorkersRunLanes_FalseWhileAnOlderWorkerRuns(t *testing.T) {
	client := cache.GetCache()
	ctx := context.Background()
	scope := uuid.NewString()
	runner := &ShardRunner{scope: scope}
	balancer := &Balancer{valkey: client, runners: map[string]*ShardRunner{SignalLogs: runner}}
	key := membersKeyBase + scope
	t.Cleanup(func() { _ = client.Do(ctx, client.B().Del().Key(key).Build()).Error() })
	now := float64(time.Now().UnixMilli())
	require.NoError(t, client.Do(ctx, client.B().Zadd().Key(key).ScoreMember().
		ScoreMember(now, "new-worker").ScoreMember(now, "old-worker").Build()).Error())

	reports := map[string]workerReport{"new-worker": {Lanes: true}}
	assert.False(t, balancer.allWorkersRunLanes(ctx, reports))

	reports["other-new-worker"] = workerReport{Lanes: true}
	assert.True(t, balancer.allWorkersRunLanes(ctx, reports))
}

type fakeLaneStore struct {
	counts map[string]map[uuid.UUID]int
	err    error
}

func (s *fakeLaneStore) LoadLanes(context.Context) (map[string]map[uuid.UUID]int, error) {
	return s.counts, s.err
}

func (s *fakeLaneStore) SetLanes(context.Context, string, uuid.UUID, int) error {
	return s.err
}

// newLaneTestBalancer is a controller on its own signal name and scope,
// with lane counts in Postgres.
func newLaneTestBalancer(t *testing.T) (*Balancer, string) {
	t.Helper()
	client := cache.GetCache()
	ctx := context.Background()
	signal := "lanes-test-" + uuid.NewString()[:8]
	store := NewPostgresLaneStore(storage.GetDb())
	balancer := &Balancer{valkey: client, log: discardLog(), maxLanes: 8, runners: map[string]*ShardRunner{},
		scope: uuid.NewString(), lanes: NewLaneCache(store, client, discardLog())}
	balancer.laneLock = newLaneLock(balancer)
	t.Cleanup(func() {
		_ = client.Do(ctx, client.B().Del().Key(laneStateKeyBase+signal, BacklogKey(signal),
			leaderlock.Key("ingest-lanes-"+balancer.scope)).Build()).Error()
		require.NoError(t, storage.GetDb().Exec("DELETE FROM ingest_lanes WHERE signal = ?", signal).Error)
	})
	return balancer, signal
}

// runController runs signal's controller as controlLanes would, with the
// counts read from the store.
func runController(t *testing.T, balancer *Balancer, signal string, loads map[uuid.UUID]float64, now time.Time) {
	t.Helper()
	ctx := context.Background()
	counts, err := balancer.lanes.store.LoadLanes(ctx)
	require.NoError(t, err)
	balancer.controlSignalLanes(ctx, signal, counts[signal], loads, nil, 1*mb, defaultLaneParams, now)
}

func storedLanes(t *testing.T, balancer *Balancer, signal string) map[uuid.UUID]int {
	t.Helper()
	counts, err := balancer.lanes.store.LoadLanes(context.Background())
	require.NoError(t, err)
	if counts[signal] == nil {
		return map[uuid.UUID]int{}
	}
	return counts[signal]
}

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
