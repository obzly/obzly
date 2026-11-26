package broker

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/valkey-io/valkey-go"
)

// Ingest lanes (docs/INGEST_LANES.md): a busy project's batches spread over
// K shards of its signal, so several workers write it. Lane 0 is the
// project's own shard; K = 1 for every project unless the lane controller
// raised it. Nothing downstream needs a project's batches in order.

// laneStride spaces a project's lane shards: coprime with IngestShards, so
// up to IngestShards lanes never share a shard.
const laneStride = 17

// laneRefresh: how often a LaneCache rereads the lane counts.
const laneRefresh = 30 * time.Second

// laneBacklogRefresh: how often a LaneCache rereads the shards' backlog.
const laneBacklogRefresh = 10 * time.Second

// backlogKeyBase holds each signal's shards that are behind and by how much,
// as the lane controller last saw them ("15:72,32:4", seconds to drain);
// it expires when no controller runs.
const backlogKeyBase = "obzly:ingest:lanes:backlog:"

const (
	// laneBacklogMin: a shard less behind than this counts as keeping up, so
	// the noise of a healthy shard's few pending batches moves nothing.
	laneBacklogMin = 2.0
	// laneBacklogHalf: a lane this many seconds behind gets half the batches
	// of a lane keeping up, a lane twice as far a third.
	laneBacklogHalf = 10.0
	// laneWeightScale: a lane keeping up has this weight in a route's wheel.
	laneWeightScale = 20
	// laneBacklogTTL: how long the controller's backlog figures stay valid,
	// in Valkey and in a cache that can't reread them.
	laneBacklogTTL = 30 * time.Second
)

var laneSource atomic.Pointer[LaneCache]

// LaneCache is a process's copy of every signal's lane counts (from the
// LaneStore, every laneRefresh) and shards' backlog (from Valkey, every
// laneBacklogRefresh). A failed read keeps what it had: lane counts until a
// read succeeds, the backlog for laneBacklogTTL. A project missing from it
// has one lane.
type LaneCache struct {
	store  LaneStore
	valkey valkey.Client
	log    *slog.Logger

	mu        sync.RWMutex
	counts    map[string]map[uuid.UUID]int
	countsAt  time.Time
	backlog   map[string]map[int]float64
	backlogAt time.Time
	// routes: each project with lanes, the order its batches go to them.
	routes map[string]map[uuid.UUID]*laneRoute
}

// laneRoute is one project's lane shards in the order its batches go to
// them, each lane as often as its weight; its own counter, since with a
// shared one two projects publishing in turn could each land on one lane.
type laneRoute struct {
	wheel []int
	next  atomic.Uint64
}

// LaneShard is lane's shard of project.
func LaneShard(projectID uuid.UUID, lane int) int {
	return (ShardFor(projectID) + laneStride*lane) % IngestShards
}

// ProjectSubjects matches a project's batches on every lane.
func ProjectSubjects(signal string, projectID uuid.UUID) string {
	return fmt.Sprintf("ingest.%s.*.%s", signal, projectID)
}

// BacklogKey is the Valkey string of signal's shards that are behind.
func BacklogKey(signal string) string {
	return backlogKeyBase + signal
}

// NewLaneCache reads lane counts from store and the shards' backlog from
// client.
func NewLaneCache(store LaneStore, client valkey.Client, log *slog.Logger) *LaneCache {
	return &LaneCache{store: store, valkey: client, log: log}
}

// SetLaneCache makes PublishIngest spread batches by cache's lane counts
// (nil: every project on one lane).
func SetLaneCache(cache *LaneCache) {
	laneSource.Store(cache)
}

// DefaultLaneCache is the cache SetLaneCache set, or nil.
func DefaultLaneCache() *LaneCache {
	return laneSource.Load()
}

// Run rereads lane counts and backlog until ctx ends.
func (c *LaneCache) Run(ctx context.Context) {
	ticker := time.NewTicker(laneBacklogRefresh)
	defer ticker.Stop()
	for {
		c.refresh(ctx, time.Now().UTC())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Lanes is project's lane count on signal (1 without lanes).
func (c *LaneCache) Lanes(signal string, projectID uuid.UUID) int {
	if c == nil {
		return 1
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return max(c.counts[signal][projectID], 1)
}

// Counts is every project with lanes on signal.
func (c *LaneCache) Counts(signal string) map[uuid.UUID]int {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return maps.Clone(c.counts[signal])
}

// shardFor is the shard of project's next batch: its own shard without
// lanes, else the next one of its route. Weighting by backlog replaced
// round-robin skipping only lanes 60 s behind: round-robin alone kept
// sending a backlogged lane its share, so in the homelab it drained only by
// the margin between its capacity and that share (9,500 batches over six
// hours), and a lane filling up got its full share until it crossed 60 s.
func (c *LaneCache) shardFor(signal string, projectID uuid.UUID) int {
	if c == nil {
		return ShardFor(projectID)
	}
	c.mu.RLock()
	route := c.routes[signal][projectID]
	c.mu.RUnlock()
	if route == nil {
		return ShardFor(projectID)
	}
	return route.wheel[(route.next.Add(1)-1)%uint64(len(route.wheel))]
}

// setCount records one project's new lane count at once (the controller's
// own process needn't wait for the next read).
func (c *LaneCache) setCount(signal string, projectID uuid.UUID, lanes int) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	counts := maps.Clone(c.counts)
	if counts == nil {
		counts = map[string]map[uuid.UUID]int{}
	}
	perProject := maps.Clone(counts[signal])
	if perProject == nil {
		perProject = map[uuid.UUID]int{}
	}
	if lanes > 1 {
		perProject[projectID] = min(lanes, IngestShards)
	} else {
		delete(perProject, projectID)
	}
	counts[signal] = perProject
	c.counts = counts
	c.rebuildLocked()
}

// set replaces counts and backlog (nil: unchanged) and rebuilds the routes.
func (c *LaneCache) set(counts map[string]map[uuid.UUID]int, backlog map[string]map[int]float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if counts != nil {
		c.counts = counts
	}
	if backlog != nil {
		c.backlog = backlog
	}
	c.rebuildLocked()
}

func (c *LaneCache) refresh(ctx context.Context, now time.Time) {
	ctx, cancel := context.WithTimeout(ctx, laneBacklogRefresh/2)
	defer cancel()
	c.mu.RLock()
	countsDue := now.Sub(c.countsAt) >= laneRefresh
	backlogAge := now.Sub(c.backlogAt)
	c.mu.RUnlock()

	var counts map[string]map[uuid.UUID]int
	if c.store != nil && countsDue {
		loaded, err := c.store.LoadLanes(ctx)
		if err != nil {
			// Keep the last counts: one lane for a busy project is what
			// built the backlog this store exists to prevent.
			if ctx.Err() == nil {
				c.log.Warn("Failed to read ingest lane counts", "error", err)
			}
		} else {
			counts = loaded
			c.mu.Lock()
			c.countsAt = now
			c.mu.Unlock()
		}
	}
	var backlog map[string]map[int]float64
	if c.valkey != nil {
		read, err := c.readBacklog(ctx)
		switch {
		case err == nil:
			backlog = read
			c.mu.Lock()
			c.backlogAt = now
			c.mu.Unlock()
		case backlogAge > laneBacklogTTL:
			backlog = map[string]map[int]float64{}
		}
	}
	if counts != nil || backlog != nil {
		c.set(counts, backlog)
	}
}

// readBacklog reads every signal's backlog; a missing key is no backlog.
func (c *LaneCache) readBacklog(ctx context.Context) (map[string]map[int]float64, error) {
	commands := make(valkey.Commands, 0, len(IngestStreams))
	for _, stream := range IngestStreams {
		commands = append(commands, c.valkey.B().Get().Key(BacklogKey(stream.Signal)).Build())
	}
	backlog := map[string]map[int]float64{}
	for i, result := range c.valkey.DoMulti(ctx, commands...) {
		listed, err := result.ToString()
		if valkey.IsValkeyNil(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		backlog[IngestStreams[i].Signal] = parseBacklog(listed)
	}
	return backlog, nil
}

// rebuildLocked recomputes every project's route; call with c.mu held.
func (c *LaneCache) rebuildLocked() {
	routes := map[string]map[uuid.UUID]*laneRoute{}
	for signal, perProject := range c.counts {
		for projectID, lanes := range perProject {
			if lanes < 2 {
				continue
			}
			if routes[signal] == nil {
				routes[signal] = map[uuid.UUID]*laneRoute{}
			}
			route := &laneRoute{wheel: laneWheel(projectID, lanes, c.backlog[signal])}
			// Carry the position on, so a rebuild doesn't send the next
			// batch of every project to its first lane.
			if old := c.routes[signal][projectID]; old != nil {
				route.next.Store(old.next.Load())
			}
			routes[signal][projectID] = route
		}
	}
	c.routes = routes
}

// laneWeights is each of project's lanes' share of its batches: a lane at
// laneBehind or more gets none while another doesn't (it drains at its
// full rate), and the others less the further behind they are, so a lane
// filling up gets fewer batches before it falls behind. When every lane is
// behind, the least behind get the most.
func laneWeights(projectID uuid.UUID, lanes int, backlog map[int]float64) []int {
	weights := make([]int, lanes)
	anyReady := false
	for lane := range lanes {
		if backlog[LaneShard(projectID, lane)] < laneBehind {
			anyReady = true
		}
	}
	for lane := range lanes {
		behind := backlog[LaneShard(projectID, lane)]
		if anyReady && behind >= laneBehind {
			continue
		}
		if behind < laneBacklogMin {
			behind = 0
		}
		weights[lane] = max(1, int(math.Round(laneWeightScale/(1+behind/laneBacklogHalf))))
	}
	return weights
}

// laneWheel spreads project's lane shards over a cycle by their weights,
// interleaved (smooth weighted round-robin): equal weights are plain
// round-robin.
func laneWheel(projectID uuid.UUID, lanes int, backlog map[int]float64) []int {
	weights := laneWeights(projectID, lanes, backlog)
	divisor := 0
	for _, weight := range weights {
		divisor = gcd(divisor, weight)
	}
	total := 0
	for i := range weights {
		weights[i] /= divisor
		total += weights[i]
	}
	wheel := make([]int, 0, total)
	current := make([]int, lanes)
	for range total {
		best := -1
		for lane, weight := range weights {
			if weight == 0 {
				continue
			}
			current[lane] += weight
			if best < 0 || current[lane] > current[best] {
				best = lane
			}
		}
		current[best] -= total
		wheel = append(wheel, LaneShard(projectID, best))
	}
	return wheel
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// parseBacklog reads "15:72,32:4" (anything not a shard and seconds is
// skipped).
func parseBacklog(listed string) map[int]float64 {
	backlog := map[int]float64{}
	for field := range strings.SplitSeq(listed, ",") {
		shardText, secondsText, ok := strings.Cut(strings.TrimSpace(field), ":")
		if !ok {
			continue
		}
		shard, err := strconv.Atoi(shardText)
		seconds, secondsErr := strconv.ParseFloat(secondsText, 64)
		if err != nil || secondsErr != nil || shard < 0 || shard >= IngestShards || seconds < 0 {
			continue
		}
		backlog[shard] = seconds
	}
	return backlog
}

// formatBacklog writes backlog as parseBacklog reads it, by shard.
func formatBacklog(backlog map[int]float64) string {
	shards := slices.Sorted(maps.Keys(backlog))
	fields := make([]string, len(shards))
	for i, shard := range shards {
		fields[i] = strconv.Itoa(shard) + ":" + strconv.FormatFloat(backlog[shard], 'f', 0, 64)
	}
	return strings.Join(fields, ",")
}

// laneSubject is the subject of one message of project: lane 0 unless the
// project has lanes, then the next lane of its route.
func laneSubject(signal string, projectID uuid.UUID) string {
	shard := laneSource.Load().shardFor(signal, projectID)
	return fmt.Sprintf("ingest.%s.%d.%s", signal, shard, projectID)
}
