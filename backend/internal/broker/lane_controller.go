package broker

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"time"

	"obzly/internal/telemetry"
	"obzly/internal/util/leaderlock"

	"github.com/google/uuid"
)

// The lane controller (docs/INGEST_LANES.md) gives a project more lanes
// once its load has been above what half a worker takes for a while, or one
// lane more while every lane of it stays behind, and fewer once its load has
// been well below. It also publishes the shards that are behind, which
// receivers route around. One worker runs it at a time.

// laneState is one project's controller timers, kept in Valkey so a new
// leader carries on (unix ms; 0 for unset).
type laneState struct {
	AboveSince int64 `json:"above,omitempty"`
	BelowSince int64 `json:"below,omitempty"`
	Changed    int64 `json:"changed,omitempty"`
}

// laneLag is how far behind a project's lane shards are: Stuck when every
// one is (and it is the busiest project on one of them), Behind when any is.
type laneLag struct {
	Stuck  bool
	Behind bool
}

type laneParams struct {
	// GrowAfter / ShrinkAfter: how long a load stays above or below before
	// the lane count follows.
	GrowAfter   time.Duration
	ShrinkAfter time.Duration
	// ChangeEvery: at most one change per project this often.
	ChangeEvery time.Duration
	// ShrinkMargin: shrink only below (K-1) lanes' worth x this.
	ShrinkMargin float64
	Max          int
}

const (
	// laneControlEvery: how often the controller runs.
	laneControlEvery = time.Minute
	// laneBytesPerCore: weighted payload bytes per second one core writes:
	// logs cost about 1.6 CPU seconds per MB in the homelab, and the
	// weights are relative to logs. A lane targets half a worker.
	laneBytesPerCore = 0.6 * (1 << 20)
	laneStateKeyBase = "obzly:ingest:lanes:state:"
	// reportedProjects: each worker reports its busiest projects per signal.
	reportedProjects = 10
	// laneBehind: a shard this far behind (seconds to drain its undelivered
	// batches) can't keep up. Receivers send it no batches of a project with
	// other lanes that aren't (and fewer as it nears this, laneWeights);
	// once every lane of its busiest project is behind, that project gets a
	// lane more whatever its load says: load is bytes x a per-signal
	// estimate of their cost, and a shard is bounded by its one fetch-write
	// loop too. In the homelab a metrics backfill filled its stream on one
	// shard at 2 of a worker's 3 cores while its load read 0.64 of the
	// 0.9 MiB/s target.
	laneBehind = 60.0
)

var defaultLaneParams = laneParams{
	GrowAfter:    10 * time.Minute,
	ShrinkAfter:  30 * time.Minute,
	ChangeEvery:  10 * time.Minute,
	ShrinkMargin: 0.6,
	Max:          8,
}

// nextLanes is a project's lane count after this run: grown to what its load
// needs, or by one while every lane of it is behind, once that has lasted
// GrowAfter; cut by one once the load has been below the smaller count's
// worth (with margin) for ShrinkAfter and no lane of it is behind. One lane
// behind among others needs no new lane: receivers route around it.
func nextLanes(
	lanes int, load, target float64, lag laneLag, state laneState, now time.Time, p laneParams,
) (int, laneState) {
	lanes = max(lanes, 1)
	want := lanes
	if target > 0 {
		want = min(max(int(math.Ceil(load/target)), 1), p.Max)
	}
	if lag.Stuck && want <= lanes {
		want = min(lanes+1, p.Max)
	}
	at := now.UnixMilli()
	settled := state.Changed == 0 || now.Sub(time.UnixMilli(state.Changed)) >= p.ChangeEvery
	switch {
	case want > lanes:
		state.BelowSince = 0
		if state.AboveSince == 0 {
			state.AboveSince = at
		}
		if now.Sub(time.UnixMilli(state.AboveSince)) >= p.GrowAfter && settled {
			return want, laneState{Changed: at}
		}
	case lanes > 1 && !lag.Behind && load < float64(lanes-1)*target*p.ShrinkMargin:
		state.AboveSince = 0
		if state.BelowSince == 0 {
			state.BelowSince = at
		}
		if now.Sub(time.UnixMilli(state.BelowSince)) >= p.ShrinkAfter && settled {
			return lanes - 1, laneState{Changed: at}
		}
	default:
		state.AboveSince, state.BelowSince = 0, 0
	}
	return lanes, state
}

// controlLanes, on the worker holding the controller's lease, publishes
// every tick how far behind each shard is (receivers weight lanes by it),
// and once a laneControlEvery runs the controller: per signal, each
// project's load summed over every worker's report, against half a typical
// worker's capacity. Lane counts are read from and written to the LaneStore.
func (b *Balancer) controlLanes(ctx context.Context, input planInput, reports map[string]workerReport) {
	if b.maxLanes < 1 || b.lanes == nil || b.lanes.store == nil {
		return
	}
	held, err := b.laneLock.TryAcquire(ctx)
	if err != nil || !held {
		return
	}
	backlog := map[string]map[int]float64{}
	for _, worker := range input.Workers {
		for _, shard := range worker.Shards {
			if shard.Behind >= laneBacklogMin {
				if backlog[shard.Signal] == nil {
					backlog[shard.Signal] = map[int]float64{}
				}
				backlog[shard.Signal][shard.Shard] = shard.Behind
			}
		}
	}
	for signal := range b.runners {
		b.publishBacklog(ctx, signal, backlog[signal])
	}
	if time.Since(b.lastLaneRun) < laneControlEvery {
		return
	}
	b.lastLaneRun = time.Now()
	if !b.allWorkersRunLanes(ctx, reports) {
		return
	}
	counts, err := b.lanes.store.LoadLanes(ctx)
	if err != nil {
		b.log.Warn("Failed to read ingest lane counts", "error", err)
		return
	}
	capacities := make([]float64, 0, len(input.Workers))
	for _, worker := range input.Workers {
		capacities = append(capacities, capacity(worker))
	}
	sort.Float64s(capacities)
	target := capacities[len(capacities)/2] * laneBytesPerCore / 2
	params := defaultLaneParams
	params.Max = b.maxLanes

	loads := map[string]map[uuid.UUID]float64{}
	for _, report := range reports {
		for _, project := range report.Projects {
			id, err := uuid.Parse(project.Project)
			if err != nil {
				continue
			}
			if loads[project.Signal] == nil {
				loads[project.Signal] = map[uuid.UUID]float64{}
			}
			loads[project.Signal][id] += project.Load
		}
	}
	for signal := range b.runners {
		behind := map[int]bool{}
		for shard, seconds := range backlog[signal] {
			if seconds >= laneBehind {
				behind[shard] = true
			}
		}
		b.controlSignalLanes(ctx, signal, counts[signal], loads[signal], behind, target, params, input.Now)
	}
}

// controlSignalLanes sets signal's lane counts, given the current ones from
// the LaneStore. The timers are in Valkey: losing them only restarts them,
// whereas losing the counts put a busy project back on one lane.
func (b *Balancer) controlSignalLanes(
	ctx context.Context, signal string, current map[uuid.UUID]int, loads map[uuid.UUID]float64,
	behindShards map[int]bool, target float64, params laneParams, now time.Time,
) {
	states, err := b.valkey.Do(ctx, b.valkey.B().Hgetall().Key(laneStateKeyBase+signal).Build()).AsStrMap()
	if err != nil {
		return
	}
	projects := map[uuid.UUID]bool{}
	for id := range loads {
		projects[id] = true
	}
	for id := range current {
		projects[id] = true
	}
	for project := range states {
		if id, err := uuid.Parse(project); err == nil {
			projects[id] = true
		}
	}
	lanesOf := func(id uuid.UUID) int {
		return max(current[id], 1)
	}
	busiest := behindProjects(behindShards, loads, lanesOf)
	withLanes := 0
	for id := range projects {
		lanes := lanesOf(id)
		var state laneState
		_ = json.Unmarshal([]byte(states[id.String()]), &state)
		lag := lagOf(id, lanes, behindShards, busiest[id])
		next, nextState := nextLanes(lanes, loads[id], target, lag, state, now, params)
		if next != lanes {
			if err := b.lanes.store.SetLanes(ctx, signal, id, next); err != nil {
				b.log.Warn("Failed to save ingest lanes", "signal", signal, "project", id, "error", err)
				next, nextState = lanes, state
			} else {
				direction := "up"
				if next < lanes {
					direction = "down"
				}
				b.log.Info("Ingest lanes changed", "signal", signal, "project", id, "from", lanes, "to", next,
					"load", int64(loads[id]), "laneTarget", int64(target), "lanesBehind", lag.Stuck)
				telemetry.IngestLaneChanges.WithLabelValues(direction).Inc()
				b.lanes.setCount(signal, id, next)
			}
		}
		if next > 1 {
			withLanes++
		}
		if nextState == (laneState{}) {
			if _, ok := states[id.String()]; ok {
				_ = b.valkey.Do(ctx, b.valkey.B().Hdel().Key(laneStateKeyBase+signal).Field(id.String()).Build()).
					Error()
			}
			continue
		}
		encoded, _ := json.Marshal(nextState)
		_ = b.valkey.Do(ctx, b.valkey.B().Hset().Key(laneStateKeyBase+signal).FieldValue().
			FieldValue(id.String(), string(encoded)).Build()).Error()
	}
	telemetry.IngestProjectsWithLanes.WithLabelValues(signal).Set(float64(withLanes))
}

// behindProjects is, for each shard that is behind, the busiest project
// with a lane on it: moving part of that project elsewhere relieves the
// shard most. A project missing from the workers' reports isn't picked.
func behindProjects(
	behindShards map[int]bool, loads map[uuid.UUID]float64, lanesOf func(uuid.UUID) int,
) map[uuid.UUID]bool {
	busiest := map[int]uuid.UUID{}
	for id, load := range loads {
		for lane := range lanesOf(id) {
			shard := LaneShard(id, lane)
			if !behindShards[shard] {
				continue
			}
			if current, ok := busiest[shard]; !ok || load > loads[current] {
				busiest[shard] = id
			}
		}
	}
	picked := map[uuid.UUID]bool{}
	for _, id := range busiest {
		picked[id] = true
	}
	return picked
}

// lagOf is project's laneLag given the shards that are behind and whether it
// is the busiest project on one of them.
func lagOf(projectID uuid.UUID, lanes int, behindShards map[int]bool, busiest bool) laneLag {
	behind := 0
	for lane := range lanes {
		if behindShards[LaneShard(projectID, lane)] {
			behind++
		}
	}
	return laneLag{Stuck: busiest && behind == lanes, Behind: behind > 0}
}

// publishBacklog tells receivers how far behind signal's shards are; the
// key outlives a few ticks only, so receivers stop weighting by it once no
// controller runs.
func (b *Balancer) publishBacklog(ctx context.Context, signal string, backlog map[int]float64) {
	key := BacklogKey(signal)
	if len(backlog) == 0 {
		_ = b.valkey.Do(ctx, b.valkey.B().Del().Key(key).Build()).Error()
		return
	}
	_ = b.valkey.Do(ctx, b.valkey.B().Set().Key(key).Value(formatBacklog(backlog)).
		Px(laneBacklogTTL).Build()).Error()
}

// allWorkersRunLanes: every shard runner registered for a signal (old
// versions included: they register too) belongs to a worker whose report says
// it can write lanes. An older worker sharing a buffer between two lanes of
// one project would mix their rows and acks.
func (b *Balancer) allWorkersRunLanes(ctx context.Context, reports map[string]workerReport) bool {
	capable := 0
	for _, report := range reports {
		if report.Lanes {
			capable++
		}
	}
	for _, runner := range b.runners {
		runners, err := b.valkey.Do(ctx, b.valkey.B().Zcard().Key(membersKeyBase+runner.scope).Build()).AsInt64()
		if err != nil || int(runners) > capable {
			return false
		}
	}
	return true
}

func newLaneLock(b *Balancer) *leaderlock.Lock {
	return leaderlock.New(b.valkey, "ingest-lanes-"+b.scope, 2*laneControlEvery)
}
