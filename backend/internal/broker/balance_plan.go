package broker

import (
	"math"
	"sort"
	"time"
)

// Load-aware shard placement (docs/INGEST_SHARD_BALANCING.md): every worker
// plans from the same shared view and carries out only its own part, a
// shard to hand away and the free shards to claim. The plan is a pure
// function of the view.

// shardRef names one shard of one signal.
type shardRef struct {
	Signal string
	Shard  int
}

// planShard is an owned shard: its weighted load (bytes/s), since when its
// owner has held it, and how far behind it is (seconds to drain its
// undelivered batches; the lane controller's, the planner ignores it).
type planShard struct {
	shardRef
	Load   float64
	Since  time.Time
	Behind float64
}

// planWorker is one live worker as it last reported itself.
type planWorker struct {
	ID       string
	Capacity float64
	Shards   []planShard
	LastShed time.Time
}

// planFree is a shard no lease covers: free since when this worker first
// saw it so, and the worker a handoff names for it ("" for none).
type planFree struct {
	shardRef
	FreeSince time.Time
	HandoffTo string
	// HandoffLoad: the shard's load as its last owner reported it.
	HandoffLoad float64
}

type planInput struct {
	Me      string
	Now     time.Time
	Workers []planWorker
	Free    []planFree
	// Lanes: each project with more than one lane, its shards in lane order.
	Lanes []laneGroup
}

// laneGroup is one project's lane shards of a signal.
type laneGroup struct {
	Signal string
	Shards []int
}

// planHandoff: give Shard to To.
type planHandoff struct {
	shardRef
	To   string
	Load float64
}

type planOutput struct {
	Claims []shardRef
	Shed   *planHandoff
	// Spread: a lane of a project this worker holds two of, to a worker
	// holding none of them (only when nothing is shed).
	Spread *planHandoff
	// Releases: idle shards beyond this worker's fair count of a signal,
	// each to the worker holding fewest. Loads alone never move an idle
	// shard, so a worker joining a quiet cluster would get none and new
	// projects would keep landing on the old workers.
	Releases []planHandoff
	// Unsplittable: this worker's shards busier than any other worker's
	// room while it is overloaded; moving one only moves the overload.
	Unsplittable int
	// LoadRatio: this worker's load against its target.
	LoadRatio float64
}

// planParams are the thresholds, constants in production and shortened in
// tests.
type planParams struct {
	// Tolerance: a worker sheds above target x Tolerance, and a receiver
	// takes a shard only up to it.
	Tolerance float64
	// Noise: an overload smaller than this many weighted bytes/s (256 KB/s
	// of logs is about 0.4 of a homelab core) is left alone.
	Noise float64
	// Floor: every shard counts at least this load. Small: idle shards
	// spread by count through Releases, and at 16 KB/s the 192 floors
	// outweighed a lab's whole weighted load.
	Floor float64
	// MinGain: a move must shrink the overload by this share of the target.
	MinGain float64
	// ShedCooldown: a worker sheds at most one shard this often.
	ShedCooldown time.Duration
	// ShardMinAge: a shard moves only after its owner held it this long,
	// so it moves at most once per ShardMinAge.
	ShardMinAge time.Duration
	// ClaimGrace: a free shard planned for another worker is anyone's after
	// this long, so a stale view never leaves it unowned.
	ClaimGrace time.Duration
	// MaxClaims bounds the leases one worker tries per tick.
	MaxClaims int
	// MaxReleases bounds the idle shards one worker gives away per tick.
	MaxReleases int
	// CountGuard: no worker holds more than CountGuard x ceil(shards /
	// workers) shards of a signal, so a wrong load figure can't pile every
	// idle shard onto one worker.
	CountGuard float64
}

var defaultPlanParams = planParams{
	Tolerance:    1.25,
	Noise:        256 << 10,
	Floor:        1 << 10,
	MinGain:      0.10,
	ShedCooldown: 2 * time.Minute,
	ShardMinAge:  10 * time.Minute,
	ClaimGrace:   2 * defaultShardTick,
	MaxClaims:    3 * IngestShards,
	MaxReleases:  8,
	CountGuard:   3,
}

// plan decides this worker's (in.Me) claims and its one shard to hand away.
func plan(in planInput, p planParams) planOutput {
	workers := append([]planWorker(nil), in.Workers...)
	sort.Slice(workers, func(i, j int) bool { return workers[i].ID < workers[j].ID })
	if len(workers) == 0 {
		return planOutput{}
	}
	index := map[string]int{}
	loads := make([]float64, len(workers))
	counts := make([]map[string]int, len(workers))
	capacities := 0.0
	total := 0.0
	for i, worker := range workers {
		index[worker.ID] = i
		counts[i] = map[string]int{}
		for _, shard := range worker.Shards {
			load := math.Max(shard.Load, p.Floor)
			loads[i] += load
			counts[i][shard.Signal]++
		}
		total += loads[i]
		capacities += capacity(worker)
	}
	total += float64(len(in.Free)) * p.Floor
	targets := make([]float64, len(workers))
	for i, worker := range workers {
		targets[i] = total * capacity(worker) / capacities
	}
	me, ok := index[in.Me]
	if !ok {
		return planOutput{}
	}
	guard := int(math.Ceil(p.CountGuard * math.Ceil(float64(IngestShards)/float64(len(workers)))))
	out := planOutput{LoadRatio: loads[me] / targets[me]}

	// The one shard to hand away, from the reported loads (before claims).
	lanes := laneHolders(in.Lanes, workers)
	out.Shed, out.Unsplittable = shedFor(in, p, workers, me, loads, targets, counts, guard, lanes)
	moved := out.Shed
	if moved == nil {
		out.Spread = spreadLanes(in, p, workers, me, loads, counts, guard, lanes)
		moved = out.Spread
	}
	out.Releases = idleReleases(p, workers, me, counts, moved)

	// Free shards go to the worker with the most room, a named receiver
	// first; this worker claims its own and any left too long.
	free := append([]planFree(nil), in.Free...)
	sort.Slice(free, func(i, j int) bool {
		if (free[i].HandoffTo != "") != (free[j].HandoffTo != "") {
			return free[i].HandoffTo != ""
		}
		if free[i].Signal != free[j].Signal {
			return free[i].Signal < free[j].Signal
		}
		return free[i].Shard < free[j].Shard
	})
	for _, shard := range free {
		owner := -1
		if to, ok := index[shard.HandoffTo]; ok {
			owner = to
		} else {
			for i := range workers {
				if counts[i][shard.Signal] >= guard {
					continue
				}
				if owner < 0 || targets[i]-loads[i] > targets[owner]-loads[owner] {
					owner = i
				}
			}
		}
		if owner >= 0 {
			loads[owner] += p.Floor
			counts[owner][shard.Signal]++
		}
		stale := in.Now.Sub(shard.FreeSince) >= p.ClaimGrace
		namedElsewhere := shard.HandoffTo != "" && shard.HandoffTo != in.Me && owner >= 0
		mine := owner == me || (stale && !namedElsewhere && counts[me][shard.Signal] < guard)
		if mine && len(out.Claims) < p.MaxClaims {
			out.Claims = append(out.Claims, shard.shardRef)
		}
	}
	return out
}

// shedFor picks the shard this overloaded worker hands away: the busiest
// one no bigger than its overload, to the worker with the most room that
// stays within tolerance, when that shrinks the overload by MinGain.
func shedFor(
	in planInput, p planParams, workers []planWorker, me int, loads, targets []float64,
	counts []map[string]int, guard int, lanes laneOwnership,
) (*planHandoff, int) {
	self := workers[me]
	excess := loads[me] - targets[me]
	if loads[me] <= targets[me]*p.Tolerance || excess <= p.Noise {
		return nil, 0
	}
	maxRoom := 0.0
	for i := range workers {
		if i != me {
			maxRoom = math.Max(maxRoom, targets[i]*p.Tolerance-loads[i])
		}
	}
	shards := append([]planShard(nil), self.Shards...)
	sort.Slice(shards, func(i, j int) bool {
		if shards[i].Load != shards[j].Load {
			return shards[i].Load > shards[j].Load
		}
		if shards[i].Signal != shards[j].Signal {
			return shards[i].Signal < shards[j].Signal
		}
		return shards[i].Shard < shards[j].Shard
	})
	unsplittable := 0
	for _, shard := range shards {
		if shard.Load > maxRoom && shard.Load > p.Floor {
			unsplittable++
		}
	}
	if in.Now.Sub(self.LastShed) < p.ShedCooldown {
		return nil, unsplittable
	}
	for _, shard := range shards {
		if shard.Load <= p.Floor || shard.Load > excess || in.Now.Sub(shard.Since) < p.ShardMinAge {
			continue
		}
		receiver := -1
		for i := range workers {
			if i == me || counts[i][shard.Signal] >= guard || loads[i]+shard.Load > targets[i]*p.Tolerance ||
				lanes.holdsOtherLane(i, shard.shardRef) {
				continue
			}
			if receiver < 0 || targets[i]-loads[i] > targets[receiver]-loads[receiver] {
				receiver = i
			}
		}
		if receiver < 0 {
			continue
		}
		after := math.Max(loads[me]-shard.Load-targets[me], loads[receiver]+shard.Load-targets[receiver])
		if excess-after < p.MinGain*targets[me] {
			continue
		}
		return &planHandoff{shardRef: shard.shardRef, To: workers[receiver].ID, Load: shard.Load}, unsplittable
	}
	return nil, unsplittable
}

// laneOwnership: which workers hold a lane of each lane group, and each lane
// shard's group.
type laneOwnership struct {
	groupOf map[shardRef]int
	holders []map[int]int
}

func laneHolders(groups []laneGroup, workers []planWorker) laneOwnership {
	owners := map[shardRef]int{}
	for i, worker := range workers {
		for _, shard := range worker.Shards {
			owners[shard.shardRef] = i
		}
	}
	lanes := laneOwnership{groupOf: map[shardRef]int{}, holders: make([]map[int]int, len(groups))}
	for g, group := range groups {
		lanes.holders[g] = map[int]int{}
		for _, shard := range group.Shards {
			ref := shardRef{group.Signal, shard}
			lanes.groupOf[ref] = g
			if owner, ok := owners[ref]; ok {
				lanes.holders[g][owner]++
			}
		}
	}
	return lanes
}

// holdsOtherLane: worker holds a lane of ref's project other than ref.
func (l laneOwnership) holdsOtherLane(worker int, ref shardRef) bool {
	g, ok := l.groupOf[ref]
	return ok && l.holders[g][worker] > 0
}

// spreadLanes hands a lane of a project this worker holds two or more of
// (its highest) to the least loaded worker holding none of that project's
// lanes. A project gets lanes because one worker can't keep up with it, so
// two on one worker gain nothing; the loads aren't consulted, as their
// estimate is what missed it: in the homelab a backlogged metrics project's
// second lane stayed on its first lane's worker, which by bytes x weight was
// the least loaded. Shedding never sends a lane back (holdsOtherLane).
func spreadLanes(
	in planInput, p planParams, workers []planWorker, me int, loads []float64,
	counts []map[string]int, guard int, lanes laneOwnership,
) *planHandoff {
	if in.Now.Sub(workers[me].LastShed) < p.ShedCooldown {
		return nil
	}
	mine := map[shardRef]planShard{}
	for _, shard := range workers[me].Shards {
		mine[shard.shardRef] = shard
	}
	for g, group := range in.Lanes {
		if lanes.holders[g][me] < 2 {
			continue
		}
		var highest *planShard
		for _, number := range group.Shards {
			if shard, ok := mine[shardRef{group.Signal, number}]; ok {
				highest = &shard
			}
		}
		if highest == nil || in.Now.Sub(highest.Since) < p.ShardMinAge {
			continue
		}
		receiver := -1
		for i := range workers {
			if i == me || lanes.holders[g][i] > 0 || counts[i][group.Signal] >= guard {
				continue
			}
			if receiver < 0 || loads[i]/capacity(workers[i]) < loads[receiver]/capacity(workers[receiver]) {
				receiver = i
			}
		}
		if receiver >= 0 {
			return &planHandoff{shardRef: highest.shardRef, To: workers[receiver].ID, Load: highest.Load}
		}
	}
	return nil
}

// idleReleases hands idle shards beyond this worker's fair count of each
// signal (ceil(shards / workers)) to the workers holding fewest, at most
// MaxReleases a tick. An idle shard buffers nothing, so moving it is free.
func idleReleases(
	p planParams,
	workers []planWorker,
	me int,
	counts []map[string]int,
	shed *planHandoff,
) []planHandoff {
	fair := int(math.Ceil(float64(IngestShards) / float64(len(workers))))
	var releases []planHandoff
	for _, shard := range workers[me].Shards {
		if len(releases) >= p.MaxReleases {
			break
		}
		if shard.Load > p.Floor || counts[me][shard.Signal] <= fair ||
			(shed != nil && shed.shardRef == shard.shardRef) {
			continue
		}
		receiver := -1
		for i := range workers {
			if i == me || counts[i][shard.Signal] >= fair {
				continue
			}
			if receiver < 0 || counts[i][shard.Signal] < counts[receiver][shard.Signal] {
				receiver = i
			}
		}
		if receiver < 0 {
			continue
		}
		counts[me][shard.Signal]--
		counts[receiver][shard.Signal]++
		releases = append(releases, planHandoff{shardRef: shard.shardRef, To: workers[receiver].ID})
	}
	return releases
}

func capacity(worker planWorker) float64 {
	if worker.Capacity <= 0 {
		return 1
	}
	return worker.Capacity
}
