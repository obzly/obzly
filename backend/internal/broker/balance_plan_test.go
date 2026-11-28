package broker

import (
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const mb = 1 << 20

var planSignals = []string{SignalLogs, SignalSpans, SignalMetrics}

// With no load, the free shards split evenly by count between workers, and
// together they claim every shard exactly once.
func Test_Plan_IdleCluster_SplitsEveryShardEvenly(t *testing.T) {
	now := time.Now()
	workers := []planWorker{{ID: "a", Capacity: 3}, {ID: "b", Capacity: 3}}
	var free []planFree
	for _, signal := range planSignals {
		for shard := range IngestShards {
			free = append(free, planFree{shardRef: shardRef{signal, shard}, FreeSince: now})
		}
	}

	claimed := map[shardRef]string{}
	perWorker := map[string]map[string]int{"a": {}, "b": {}}
	for _, me := range []string{"a", "b"} {
		out := plan(planInput{Me: me, Now: now, Workers: workers, Free: free}, defaultPlanParams)
		for _, ref := range out.Claims {
			require.NotContains(t, claimed, ref, "a shard is planned for one worker")
			claimed[ref] = me
			perWorker[me][ref.Signal]++
		}
	}
	require.Len(t, claimed, len(free))
	for _, signal := range planSignals {
		assert.Equal(t, IngestShards/2, perWorker["a"][signal], signal)
		assert.Equal(t, IngestShards/2, perWorker["b"][signal], signal)
	}
}

// The lab's case: three of four busy shards on one worker. It hands one
// away, the cluster settles within tolerance and then nothing moves.
func Test_Plan_ThreeBusyShardsOnOneWorker_SettlesWithinToleranceAndStops(t *testing.T) {
	now := time.Now().Add(-time.Hour)
	workers := idleWorkers(now, "a", "b")
	makeBusy(workers, "a", shardRef{SignalLogs, 1}, 3*mb)
	makeBusy(workers, "a", shardRef{SignalMetrics, 2}, 3*mb)
	makeBusy(workers, "a", shardRef{SignalSpans, 3}, 3*mb)
	makeBusy(workers, "b", shardRef{SignalLogs, 4}, 3*mb)

	moves := simulate(t, workers, now, 30*time.Minute, nil)

	require.Equal(t, 1, moves, "one shard moves")
	assert.InDelta(t, 2, busyShards(workers, "a"), 0)
	assert.InDelta(t, 2, busyShards(workers, "b"), 0)
}

// A third worker joining an unbalanced pair takes load without the others
// trading shards back and forth.
func Test_Plan_WorkerJoins_TakesItsShareWithBoundedMoves(t *testing.T) {
	now := time.Now().Add(-time.Hour)
	workers := idleWorkers(now, "a", "b")
	for shard := range 6 {
		makeBusy(workers, "a", shardRef{SignalLogs, shard}, 2*mb)
	}
	makeBusy(workers, "b", shardRef{SignalSpans, 10}, 2*mb)
	makeBusy(workers, "b", shardRef{SignalSpans, 11}, 2*mb)
	workers = append(workers, planWorker{ID: "c", Capacity: 3})

	moves := simulate(t, workers, now, time.Hour, nil)

	assert.LessOrEqual(t, moves, 4)
	for _, worker := range workers {
		assert.LessOrEqual(t, ratio(workers, worker.ID), defaultPlanParams.Tolerance, worker.ID)
	}
}

// A worker joining a quiet cluster gets its fair count of idle shards, so
// new projects land on it too, and no busy shard moves.
func Test_Plan_WorkerJoinsAQuietCluster_GetsItsFairCountOfIdleShards(t *testing.T) {
	now := time.Now().Add(-time.Hour)
	workers := idleWorkers(now, "a", "b")
	workers = append(workers, planWorker{ID: "c", Capacity: 3})

	moves := simulate(t, workers, now, 10*time.Minute, nil)

	assert.Zero(t, moves, "no busy shard to move")
	for _, worker := range workers {
		perSignal := map[string]int{}
		for _, shard := range worker.Shards {
			perSignal[shard.Signal]++
		}
		for _, signal := range planSignals {
			assert.InDelta(t, IngestShards/3, perSignal[signal], 2, "%s %s", worker.ID, signal)
		}
	}
}

// Loads that wobble by 10% from tick to tick never move a shard.
func Test_Plan_NoisyLoads_DoNotMoveShards(t *testing.T) {
	now := time.Now().Add(-time.Hour)
	workers := idleWorkers(now, "a", "b")
	makeBusy(workers, "a", shardRef{SignalLogs, 1}, 3*mb)
	makeBusy(workers, "b", shardRef{SignalLogs, 2}, 3*mb)
	rng := rand.New(rand.NewSource(1))

	moves := simulate(t, workers, now, time.Hour, func(load float64) float64 {
		return load * (0.9 + 0.2*rng.Float64())
	})

	assert.Zero(t, moves)
}

// One shard busier than the other worker's room stays put (moving it only
// moves the overload) and is reported.
func Test_Plan_OneShardBiggerThanAnyRoom_StaysAndIsReported(t *testing.T) {
	now := time.Now().Add(-time.Hour)
	workers := idleWorkers(now, "a", "b")
	makeBusy(workers, "a", shardRef{SignalLogs, 1}, 10*mb)

	out := plan(planInput{Me: "a", Now: now, Workers: workers}, defaultPlanParams)

	assert.Nil(t, out.Shed)
	assert.Equal(t, 1, out.Unsplittable)
	assert.Greater(t, out.LoadRatio, 1.5)
}

// A shard held for less than ShardMinAge, or a worker that shed within
// ShedCooldown, doesn't move.
func Test_Plan_Cooldowns_HoldBackAMove(t *testing.T) {
	now := time.Now()
	young := idleWorkers(now.Add(-time.Hour), "a", "b")
	makeBusy(young, "a", shardRef{SignalLogs, 1}, 3*mb)
	makeBusy(young, "a", shardRef{SignalLogs, 2}, 3*mb)
	for i := range young[0].Shards {
		young[0].Shards[i].Since = now.Add(-time.Minute)
	}
	assert.Nil(t, plan(planInput{Me: "a", Now: now, Workers: young}, defaultPlanParams).Shed, "too young")

	recent := idleWorkers(now.Add(-time.Hour), "a", "b")
	makeBusy(recent, "a", shardRef{SignalLogs, 1}, 3*mb)
	makeBusy(recent, "a", shardRef{SignalLogs, 2}, 3*mb)
	recent[0].LastShed = now.Add(-time.Minute)
	assert.Nil(t, plan(planInput{Me: "a", Now: now, Workers: recent}, defaultPlanParams).Shed, "shed lately")

	recent[0].LastShed = now.Add(-time.Hour)
	assert.NotNil(t, plan(planInput{Me: "a", Now: now, Workers: recent}, defaultPlanParams).Shed)
}

// Loads follow capacity: a worker with twice the cores targets twice the
// load, so it isn't overloaded where an even split would say it is.
func Test_Plan_MixedCapacity_TargetsLoadInProportion(t *testing.T) {
	now := time.Now().Add(-time.Hour)
	workers := idleWorkers(now, "big", "small")
	workers[0].Capacity, workers[1].Capacity = 4, 2
	makeBusy(workers, "big", shardRef{SignalLogs, 1}, 4*mb)
	makeBusy(workers, "big", shardRef{SignalLogs, 2}, 4*mb)
	makeBusy(workers, "small", shardRef{SignalLogs, 3}, 4*mb)

	assert.Nil(t, plan(planInput{Me: "big", Now: now, Workers: workers}, defaultPlanParams).Shed)
	assert.InDelta(t, 1, ratio(workers, "big"), 0.1)
}

// A shard handed to a named worker is claimed by it even when another has
// more room; others wait until it has been free for ClaimGrace.
func Test_Plan_HandoffToANamedWorker_OnlyItClaimsUntilStale(t *testing.T) {
	now := time.Now()
	workers := idleWorkers(now.Add(-time.Hour), "a", "b", "c")
	makeBusy(workers, "b", shardRef{SignalLogs, 9}, 2*mb)
	ref := shardRef{SignalLogs, 5}
	fresh := []planFree{{shardRef: ref, FreeSince: now, HandoffTo: "b"}}

	assert.Contains(t, plan(planInput{Me: "b", Now: now, Workers: workers, Free: fresh}, defaultPlanParams).Claims, ref)
	assert.NotContains(t, plan(planInput{Me: "a", Now: now, Workers: workers, Free: fresh}, defaultPlanParams).Claims, ref)

	stale := []planFree{{shardRef: ref, FreeSince: now.Add(-time.Minute), HandoffTo: "gone"}}
	assert.Contains(t, plan(planInput{Me: "a", Now: now, Workers: workers, Free: stale}, defaultPlanParams).Claims, ref,
		"a handoff to a worker that left is an ordinary free shard")
}

// A worker never plans more than CountGuard times its fair count of one
// signal, whatever the loads say.
func Test_Plan_CountGuard_CapsShardsPerSignal(t *testing.T) {
	now := time.Now()
	// b looks overloaded, so every free shard would go to a.
	workers := []planWorker{{ID: "a", Capacity: 3}, {ID: "b", Capacity: 3, Shards: []planShard{
		{shardRef: shardRef{SignalLogs, 63}, Load: 100 * mb, Since: now.Add(-time.Hour)},
	}}}
	var free []planFree
	for shard := range IngestShards - 1 {
		free = append(free, planFree{shardRef: shardRef{SignalLogs, shard}, FreeSince: now})
	}
	params := defaultPlanParams
	params.CountGuard = 1

	out := plan(planInput{Me: "a", Now: now, Workers: workers, Free: free}, params)

	assert.Len(t, out.Claims, IngestShards/2)
}

// idleWorkers: workers holding no shards yet; idleShards spreads the 192
// shards between them, owned since `since`.
// The lab's case: a backlogged metrics project's second lane sat on its
// first lane's worker, which by its byte-weighted load was the least
// loaded. It moves to a worker holding none of the project's lanes, and
// shedding never brings it back, even when the receiver ends up over
// tolerance and the first worker has most room.
func Test_Plan_TwoLanesOfAProjectOnOneWorker_SpreadOnceAndStay(t *testing.T) {
	now := time.Now().Add(-time.Hour)
	workers := idleWorkers(now, "a", "b", "c")
	makeBusy(workers, "a", shardRef{SignalMetrics, 15}, 0.25*mb)
	makeBusy(workers, "a", shardRef{SignalMetrics, 32}, 0.25*mb)
	makeBusy(workers, "b", shardRef{SignalLogs, 49}, 0.68*mb)
	makeBusy(workers, "b", shardRef{SignalSpans, 0}, 0.78*mb)
	makeBusy(workers, "c", shardRef{SignalLogs, 32}, 1.1*mb)
	lanes := []laneGroup{{Signal: SignalMetrics, Shards: []int{15, 32}}}

	moves := simulateWithLanes(t, workers, lanes, now, time.Hour)

	require.Equal(t, 1, moves)
	assert.Equal(t, "a", ownerOf(workers, shardRef{SignalMetrics, 15}))
	assert.Equal(t, "c", ownerOf(workers, shardRef{SignalMetrics, 32}), "the least loaded worker without a lane")
}

// Lanes already on different workers, or more lanes than workers, move
// nothing.
func Test_Plan_LanesOnEveryWorkerAlready_NothingSpreads(t *testing.T) {
	now := time.Now().Add(-time.Hour)
	workers := idleWorkers(now, "a", "b")
	makeBusy(workers, "a", shardRef{SignalLogs, 1}, 0.5*mb)
	makeBusy(workers, "a", shardRef{SignalLogs, 18}, 0.5*mb)
	makeBusy(workers, "b", shardRef{SignalLogs, 35}, 0.5*mb)
	lanes := []laneGroup{{Signal: SignalLogs, Shards: []int{1, 18, 35}}}

	moves := simulateWithLanes(t, workers, lanes, now, time.Hour)

	require.Zero(t, moves)
}

func simulateWithLanes(t *testing.T, workers []planWorker, lanes []laneGroup, start time.Time,
	duration time.Duration,
) int {
	t.Helper()
	moves := 0
	for now := start; now.Before(start.Add(duration)); now = now.Add(defaultShardTick) {
		for i := range workers {
			out := plan(planInput{Me: workers[i].ID, Now: now, Workers: workers, Lanes: lanes}, defaultPlanParams)
			for _, moved := range []*planHandoff{out.Shed, out.Spread} {
				if moved == nil {
					continue
				}
				moves++
				require.Less(t, moves, 20, "shards keep moving")
				moveShard(workers, i, moved, now)
			}
		}
	}
	return moves
}

func ownerOf(workers []planWorker, ref shardRef) string {
	for _, worker := range workers {
		for _, shard := range worker.Shards {
			if shard.shardRef == ref {
				return worker.ID
			}
		}
	}
	return ""
}

func idleWorkers(since time.Time, ids ...string) []planWorker {
	workers := make([]planWorker, len(ids))
	for i, id := range ids {
		workers[i] = planWorker{ID: id, Capacity: 3}
	}
	n := 0
	for _, signal := range planSignals {
		for shard := range IngestShards {
			w := &workers[n%len(workers)]
			w.Shards = append(w.Shards, planShard{shardRef: shardRef{signal, shard}, Since: since})
			n++
		}
	}
	return workers
}

// makeBusy gives ref to worker id with load bytes/s.
func makeBusy(workers []planWorker, id string, ref shardRef, load float64) {
	for i := range workers {
		for j := 0; j < len(workers[i].Shards); j++ {
			if workers[i].Shards[j].shardRef == ref {
				workers[i].Shards = append(workers[i].Shards[:j], workers[i].Shards[j+1:]...)
				j--
			}
		}
	}
	for i := range workers {
		if workers[i].ID == id {
			workers[i].Shards = append(workers[i].Shards,
				planShard{shardRef: ref, Load: load, Since: time.Unix(0, 0)})
		}
	}
}

// simulate ticks every worker's plan for duration, carrying out sheds, and
// returns how many shards moved. wobble, if set, changes each load per tick.
func simulate(t *testing.T, workers []planWorker, start time.Time, duration time.Duration,
	wobble func(float64) float64,
) int {
	t.Helper()
	base := map[shardRef]float64{}
	for _, worker := range workers {
		for _, shard := range worker.Shards {
			base[shard.shardRef] = shard.Load
		}
	}
	moves := 0
	for now := start; now.Before(start.Add(duration)); now = now.Add(defaultShardTick) {
		if wobble != nil {
			for i := range workers {
				for j := range workers[i].Shards {
					workers[i].Shards[j].Load = wobble(base[workers[i].Shards[j].shardRef])
				}
			}
		}
		for i := range workers {
			out := plan(planInput{Me: workers[i].ID, Now: now, Workers: workers}, defaultPlanParams)
			for _, release := range out.Releases {
				lastShed := workers[i].LastShed
				moveShard(workers, i, &release, now)
				workers[i].LastShed = lastShed // an idle release isn't a shed
			}
			if out.Shed == nil {
				continue
			}
			moves++
			require.Less(t, moves, 50, "shards keep moving")
			moveShard(workers, i, out.Shed, now)
		}
	}
	return moves
}

func moveShard(workers []planWorker, from int, handoff *planHandoff, now time.Time) {
	var moved planShard
	shards := workers[from].Shards[:0]
	for _, shard := range workers[from].Shards {
		if shard.shardRef == handoff.shardRef {
			moved = shard
			continue
		}
		shards = append(shards, shard)
	}
	workers[from].Shards = shards
	workers[from].LastShed = now
	for i := range workers {
		if workers[i].ID == handoff.To {
			moved.Since = now
			workers[i].Shards = append(workers[i].Shards, moved)
		}
	}
}

func busyShards(workers []planWorker, id string) int {
	n := 0
	for _, worker := range workers {
		if worker.ID != id {
			continue
		}
		for _, shard := range worker.Shards {
			if shard.Load > defaultPlanParams.Floor {
				n++
			}
		}
	}
	return n
}

func ratio(workers []planWorker, id string) float64 {
	out := plan(planInput{Me: id, Now: time.Now(), Workers: workers}, defaultPlanParams)
	return out.LoadRatio
}
