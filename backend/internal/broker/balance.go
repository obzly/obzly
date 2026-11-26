package broker

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"obzly/internal/telemetry"
	"obzly/internal/util/leaderlock"

	"github.com/google/uuid"
	"github.com/valkey-io/valkey-go"
)

// Balancer places this worker's ingest shards by load across every signal
// (docs/INGEST_SHARD_BALANCING.md). Each tick it samples its shards' load,
// publishes it, reads every worker's, plans, and carries out its own part:
// handing one shard to a named worker, giving idle shards to workers short
// of their count, claiming free shards. Leases stay the only authority on
// ownership; a stale or wrong view only places shards badly.
type Balancer struct {
	valkey   valkey.Client
	runners  map[string]*ShardRunner
	memberID string
	scope    string
	capacity float64
	tick     time.Duration
	params   planParams
	log      *slog.Logger
	// loadOf, set in tests, replaces the sampled load of a shard.
	loadOf func(shardRef) (float64, bool)
	// maxLanes: the lane controller's ceiling; 0 leaves every project on
	// one lane (INGEST_LANES=off). lanes: the lane counts it places by and
	// whose store it writes.
	maxLanes    int
	lanes       *LaneCache
	laneLock    *leaderlock.Lock
	lastLaneRun time.Time

	mu   sync.Mutex
	ewma map[shardRef]float64
	// drain: each owned shard's smoothed batches per second.
	drain       map[shardRef]float64
	projectLoad map[string]map[uuid.UUID]float64
	lastSample  time.Time
	freeSince   map[shardRef]time.Time
	lastShed    time.Time
}

// BalancerOption adjusts a Balancer.
type BalancerOption func(*Balancer)

// workerReport is what a worker publishes about itself each tick.
type workerReport struct {
	Capacity float64        `json:"capacity"`
	LastShed int64          `json:"lastShed,omitempty"`
	Shards   []reportedLoad `json:"shards"`
	// Lanes: this worker's sink shares a project's buffer between shards,
	// so the lane controller may give projects several lanes.
	Lanes bool `json:"lanes,omitempty"`
	// Projects: the busiest projects per signal and their weighted load.
	Projects []reportedProject `json:"projects,omitempty"`
}

type reportedProject struct {
	Signal  string  `json:"s"`
	Project string  `json:"p"`
	Load    float64 `json:"l"`
}

type reportedLoad struct {
	Signal string  `json:"s"`
	Shard  int     `json:"n"`
	Load   float64 `json:"l"`
	Since  int64   `json:"t"`
	// Behind: seconds the shard needs to drain its undelivered batches.
	Behind float64 `json:"b,omitempty"`
}

const (
	balanceKeyBase = "obzly:ingest:balance:"
	// loadHalfLife smooths each shard's bytes per second: a burst doesn't
	// move a shard, a lasting change shows within a couple of minutes.
	loadHalfLife = time.Minute
)

// signalWeights convert a signal's payload bytes into comparable load,
// relative to logs. Measured in the homelab (2026-11-22) from workers each
// holding one busy shard: logs about 1.6 CPU seconds per MB (pattern
// hashing, field extraction), spans about 0.28, metrics 0.2 to 0.35.
var signalWeights = map[string]float64{
	SignalLogs:    1,
	SignalSpans:   0.2,
	SignalMetrics: 0.2,
}

// WithLanes lets this worker run the lane controller, up to max lanes per
// project (0: every project stays on one lane), keeping counts in lanes'
// store.
func WithLanes(maxLanes int, lanes *LaneCache) BalancerOption {
	return func(b *Balancer) {
		b.maxLanes = maxLanes
		b.lanes = lanes
	}
}

// WithBalancerScope namespaces the shared keys (default: all workers of
// the Site), so tests sharing a Valkey don't see each other.
func WithBalancerScope(scope string) BalancerOption {
	return func(b *Balancer) { b.scope = scope }
}

// NewBalancer balances runners (one per signal, sharing this process's
// identity) and marks them balanced.
func NewBalancer(client valkey.Client, runners []*ShardRunner, log *slog.Logger, opts ...BalancerOption) *Balancer {
	b := &Balancer{
		valkey:      client,
		runners:     map[string]*ShardRunner{},
		scope:       "site",
		capacity:    float64(runtime.GOMAXPROCS(0)),
		tick:        defaultShardTick,
		params:      defaultPlanParams,
		log:         log,
		ewma:        map[shardRef]float64{},
		drain:       map[shardRef]float64{},
		projectLoad: map[string]map[uuid.UUID]float64{},
		freeSince:   map[shardRef]time.Time{},
	}
	for _, runner := range runners {
		b.runners[runner.signal] = runner
		b.tick = runner.tick
		if b.memberID == "" {
			b.memberID = runner.memberID
		}
		runner.SetBalanced(true)
	}
	for _, opt := range opts {
		opt(b)
	}
	b.laneLock = newLaneLock(b)
	return b
}

// Run balances every tick until ctx ends.
func (b *Balancer) Run(ctx context.Context) {
	ticker := time.NewTicker(b.tick)
	defer ticker.Stop()
	for {
		if err := b.balance(ctx); err != nil && ctx.Err() == nil {
			b.log.Warn("Failed to balance ingest shards", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (b *Balancer) balance(ctx context.Context) error {
	now := time.Now().UTC()
	mine, projects := b.sample(now)
	if err := b.publish(ctx, now, mine, projects); err != nil {
		return err
	}
	input, reports, err := b.view(ctx, now, mine)
	if err != nil {
		return err
	}
	reports[b.memberID] = workerReport{Capacity: mine.Capacity, Lanes: true, Projects: projects}
	defer b.controlLanes(ctx, input, reports)
	input.Lanes = b.laneGroups()
	out := plan(input, b.params)
	telemetry.IngestWorkerLoadRatio.Set(out.LoadRatio)
	telemetry.IngestUnsplittableShards.Set(float64(out.Unsplittable))
	for reason, moved := range map[string]*planHandoff{"shed": out.Shed, "lanes": out.Spread} {
		if moved == nil {
			continue
		}
		b.handOff(ctx, *moved, reason)
		b.mu.Lock()
		b.lastShed = now
		b.mu.Unlock()
	}
	for _, release := range out.Releases {
		b.handOff(ctx, release, "release")
	}
	for _, ref := range out.Claims {
		b.claim(ctx, ref, input)
	}
	return nil
}

// sample updates each owned shard's and each project's smoothed load from
// the bytes fetched since the last tick, and returns this worker as the
// planner sees it and its busiest projects per signal.
func (b *Balancer) sample(now time.Time) (planWorker, []reportedProject) {
	b.mu.Lock()
	defer b.mu.Unlock()
	elapsed := now.Sub(b.lastSample).Seconds()
	first := b.lastSample.IsZero()
	b.lastSample = now
	alpha := 1 - math.Exp(-elapsed*math.Ln2/loadHalfLife.Seconds())
	self := planWorker{ID: b.memberID, Capacity: b.capacity, LastShed: b.lastShed}
	seen := map[shardRef]bool{}
	for signal, runner := range b.runners {
		for _, owned := range runner.ownedLoads() {
			ref := shardRef{signal, owned.shard}
			seen[ref] = true
			load := b.ewma[ref]
			if !first && elapsed > 0 {
				rate := float64(owned.bytes) / elapsed * signalWeights[signal]
				load += alpha * (rate - load)
			}
			if b.loadOf != nil {
				if override, ok := b.loadOf(ref); ok {
					load = override
				}
			}
			b.ewma[ref] = load
			behind := b.sampleBehind(ref, owned, elapsed, alpha, first)
			self.Shards = append(self.Shards, planShard{shardRef: ref, Load: load, Since: owned.since, Behind: behind})
			telemetry.IngestShardLoad.WithLabelValues(signal, strconv.Itoa(owned.shard)).Set(load)
			telemetry.IngestShardBehind.WithLabelValues(signal, strconv.Itoa(owned.shard)).Set(behind)
		}
	}
	for ref := range b.ewma {
		if !seen[ref] {
			delete(b.ewma, ref)
			delete(b.drain, ref)
			telemetry.IngestShardLoad.DeleteLabelValues(ref.Signal, strconv.Itoa(ref.Shard))
			telemetry.IngestShardBehind.DeleteLabelValues(ref.Signal, strconv.Itoa(ref.Shard))
		}
	}
	return self, b.sampleProjects(elapsed, alpha, first)
}

// sampleBehind smooths the shard's drain rate and returns how many seconds
// its undelivered batches need at that rate. A shard that drains nothing
// (its uploads failing, say) reports 0: more lanes wouldn't help it.
func (b *Balancer) sampleBehind(ref shardRef, owned ownedLoad, elapsed, alpha float64, first bool) float64 {
	if first || elapsed <= 0 {
		return 0
	}
	rate := float64(owned.messages) / elapsed
	drain, ok := b.drain[ref]
	if ok {
		drain += alpha * (rate - drain)
	} else {
		drain = rate
	}
	b.drain[ref] = drain
	if drain <= 0 || owned.pending <= 0 {
		return 0
	}
	return float64(owned.pending) / drain
}

// sampleProjects smooths each project's load and returns the busiest
// reportedProjects per signal. A project that stopped fades out.
func (b *Balancer) sampleProjects(elapsed, alpha float64, first bool) []reportedProject {
	var busiest []reportedProject
	for signal, runner := range b.runners {
		taken := runner.takeProjectBytes()
		loads := b.projectLoad[signal]
		if loads == nil {
			loads = map[uuid.UUID]float64{}
			b.projectLoad[signal] = loads
		}
		for project, load := range loads {
			if _, ok := taken[project]; !ok && !first && elapsed > 0 {
				load -= alpha * load
				if load < 1 {
					delete(loads, project)
					continue
				}
				loads[project] = load
			}
		}
		for project, bytes := range taken {
			if first || elapsed <= 0 {
				continue
			}
			rate := float64(bytes) / elapsed * signalWeights[signal]
			loads[project] += alpha * (rate - loads[project])
		}
		ranked := make([]reportedProject, 0, len(loads))
		for project, load := range loads {
			ranked = append(ranked, reportedProject{Signal: signal, Project: project.String(), Load: load})
		}
		sort.Slice(ranked, func(i, j int) bool { return ranked[i].Load > ranked[j].Load })
		busiest = append(busiest, ranked[:min(len(ranked), reportedProjects)]...)
	}
	return busiest
}

// publish registers this worker and its report, both expiring after a
// lease TTL so a worker that stops shows as gone.
func (b *Balancer) publish(ctx context.Context, now time.Time, self planWorker, projects []reportedProject) error {
	report := workerReport{
		Capacity: self.Capacity, Shards: make([]reportedLoad, 0, len(self.Shards)), Lanes: true, Projects: projects,
	}
	if !self.LastShed.IsZero() {
		report.LastShed = self.LastShed.UnixMilli()
	}
	for _, shard := range self.Shards {
		report.Shards = append(report.Shards, reportedLoad{
			Signal: shard.Signal, Shard: shard.Shard, Load: shard.Load, Since: shard.Since.UnixMilli(),
			Behind: shard.Behind,
		})
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		return err
	}
	ttl := b.ttl()
	cutoff := now.Add(-ttl).UnixMilli()
	for _, result := range b.valkey.DoMulti(ctx,
		b.valkey.B().Zadd().Key(b.membersKey()).ScoreMember().ScoreMember(float64(now.UnixMilli()), b.memberID).Build(),
		b.valkey.B().Zremrangebyscore().Key(b.membersKey()).Min("-inf").Max(strconv.FormatInt(cutoff, 10)).Build(),
		b.valkey.B().Set().Key(b.workerKey(b.memberID)).Value(string(encoded)).Px(ttl).Build(),
	) {
		if err := result.Error(); err != nil {
			return err
		}
	}
	return nil
}

// view reads every live worker's report, which shards no lease covers and
// the handoffs naming a receiver for them.
func (b *Balancer) view(
	ctx context.Context,
	now time.Time,
	self planWorker,
) (planInput, map[string]workerReport, error) {
	input := planInput{Me: b.memberID, Now: now, Workers: []planWorker{self}}
	reports := map[string]workerReport{}
	members, err := b.valkey.Do(ctx, b.valkey.B().Zrange().Key(b.membersKey()).Min("0").Max("-1").Build()).AsStrSlice()
	if err != nil {
		return input, reports, err
	}
	var others []string
	for _, member := range members {
		if member != b.memberID {
			others = append(others, member)
		}
	}
	if len(others) > 0 {
		keys := make([]string, len(others))
		for i, member := range others {
			keys[i] = b.workerKey(member)
		}
		values, err := b.valkey.Do(ctx, b.valkey.B().Mget().Key(keys...).Build()).ToArray()
		if err != nil {
			return input, reports, err
		}
		for i, value := range values {
			raw, err := value.ToString()
			if err != nil {
				continue // expired between the two reads: gone
			}
			var report workerReport
			if json.Unmarshal([]byte(raw), &report) != nil {
				continue
			}
			reports[others[i]] = report
			worker := planWorker{ID: others[i], Capacity: report.Capacity}
			if report.LastShed > 0 {
				worker.LastShed = time.UnixMilli(report.LastShed).UTC()
			}
			for _, shard := range report.Shards {
				worker.Shards = append(worker.Shards, planShard{
					shardRef: shardRef{shard.Signal, shard.Shard}, Load: shard.Load,
					Since: time.UnixMilli(shard.Since).UTC(), Behind: shard.Behind,
				})
			}
			input.Workers = append(input.Workers, worker)
		}
	}

	var free []shardRef
	for signal, runner := range b.runners {
		keys := make([]string, IngestShards)
		for shard := range IngestShards {
			keys[shard] = leaderlock.Key(runner.leaseName(shard))
		}
		held, err := b.valkey.Do(ctx, b.valkey.B().Mget().Key(keys...).Build()).ToArray()
		if err != nil {
			return input, reports, err
		}
		for shard, value := range held {
			if value.IsNil() {
				free = append(free, shardRef{signal, shard})
			}
		}
	}
	handoffs := map[shardRef]string{}
	if len(free) > 0 {
		keys := make([]string, len(free))
		for i, ref := range free {
			keys[i] = b.handoffKey(ref)
		}
		values, err := b.valkey.Do(ctx, b.valkey.B().Mget().Key(keys...).Build()).ToArray()
		if err != nil {
			return input, reports, err
		}
		for i, value := range values {
			if raw, err := value.ToString(); err == nil {
				handoffs[free[i]] = raw
			}
		}
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	stillFree := map[shardRef]time.Time{}
	for _, ref := range free {
		since, ok := b.freeSince[ref]
		if !ok {
			since = now
		}
		stillFree[ref] = since
		to, load := parseHandoff(handoffs[ref])
		input.Free = append(input.Free, planFree{shardRef: ref, FreeSince: since, HandoffTo: to, HandoffLoad: load})
	}
	b.freeSince = stillFree
	return input, reports, nil
}

// laneGroups is every project with more than one lane, per signal this
// worker runs; none when lanes are off.
func (b *Balancer) laneGroups() []laneGroup {
	if b.maxLanes < 1 {
		return nil
	}
	var groups []laneGroup
	for signal := range b.runners {
		for id, lanes := range b.lanes.Counts(signal) {
			if lanes < 2 {
				continue
			}
			group := laneGroup{Signal: signal}
			for lane := range min(lanes, IngestShards) {
				group.Shards = append(group.Shards, LaneShard(id, lane))
			}
			groups = append(groups, group)
		}
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Signal != groups[j].Signal {
			return groups[i].Signal < groups[j].Signal
		}
		return groups[i].Shards[0] < groups[j].Shards[0]
	})
	return groups
}

// handOff names the receiver, then gives the shard away as on a scale
// down: flush, upload, ack, free the lease.
func (b *Balancer) handOff(ctx context.Context, handoff planHandoff, reason string) {
	runner := b.runners[handoff.Signal]
	if runner == nil {
		return
	}
	value := handoff.To + "|" + strconv.FormatFloat(handoff.Load, 'f', 0, 64)
	err := b.valkey.Do(ctx, b.valkey.B().Set().Key(b.handoffKey(handoff.shardRef)).Value(value).
		Px(b.ttl()).Build()).Error()
	if err != nil {
		b.log.Warn("Failed to name an ingest shard's receiver; keeping the shard",
			"signal", handoff.Signal, "shard", handoff.Shard, "error", err)
		return
	}
	if reason == "shed" {
		b.log.Info("Handing ingest shard to another worker", "signal", handoff.Signal, "shard", handoff.Shard,
			"load", int64(handoff.Load), "to", handoff.To)
	}
	runner.handOff(handoff.Shard)
	telemetry.IngestShardMoves.WithLabelValues(reason).Inc()
}

// claim takes a free shard planned for this worker, and clears the handoff
// that named it.
func (b *Balancer) claim(ctx context.Context, ref shardRef, input planInput) {
	runner := b.runners[ref.Signal]
	if runner == nil {
		return
	}
	acquired, err := runner.claim(ctx, ref.Shard)
	if err != nil {
		b.log.Warn("Failed to claim ingest shard", "signal", ref.Signal, "shard", ref.Shard, "error", err)
		return
	}
	if !acquired {
		return
	}
	telemetry.IngestShardMoves.WithLabelValues("claim").Inc()
	for _, free := range input.Free {
		if free.shardRef == ref && free.HandoffTo == b.memberID {
			// Start from the giver's figure: from zero, the receiver looked
			// idle for minutes and could be handed more.
			b.mu.Lock()
			b.ewma[ref] = free.HandoffLoad
			b.mu.Unlock()
			_ = b.valkey.Do(ctx, b.valkey.B().Del().Key(b.handoffKey(ref)).Build()).Error()
		}
	}
}

// parseHandoff reads a handoff's "receiver|load".
func parseHandoff(raw string) (string, float64) {
	to, loadText, _ := strings.Cut(raw, "|")
	load, _ := strconv.ParseFloat(loadText, 64)
	return to, load
}

func (b *Balancer) ttl() time.Duration {
	return leaseTicks * b.tick
}

func (b *Balancer) membersKey() string {
	return balanceKeyBase + "members:" + b.scope
}

func (b *Balancer) workerKey(member string) string {
	return balanceKeyBase + "worker:" + b.scope + ":" + member
}

func (b *Balancer) handoffKey(ref shardRef) string {
	return balanceKeyBase + "handoff:" + b.scope + ":" + ref.Signal + ":" + strconv.Itoa(ref.Shard)
}
