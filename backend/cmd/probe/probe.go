package main

import (
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"obzly/internal/synthetics/checks"
	"obzly/internal/synthetics/spec"
	"obzly/internal/util/egress"
)

const (
	defaultPoll    = 180 * time.Second
	retryPoll      = 30 * time.Second
	revokedPoll    = 60 * time.Second
	maxTaskBackoff = time.Minute
	// maxTestRuns caps editor Test runs at once; they have their own slots so a probe busy with
	// its schedule still answers them.
	maxTestRuns = 8
)

type probe struct {
	api       *controlPlane
	sender    *sender
	log       *slog.Logger
	policy    egress.Policy
	cachePath string
	slots     chan struct{}
	taskWait  time.Duration
	testSlots chan struct{}
	// running counts scheduled runs in progress, so shutdown can wait for their results.
	running atomic.Int64

	instance string
	// env fills the ${env:NAME} placeholders OBZLY_PROBE_ENV_ALLOW allows.
	env *checks.EnvPolicy
	// cloudTokens caches the control plane's federated tokens.
	cloudTokens cloudTokenCache

	mu        sync.Mutex
	members   []string
	config    *spec.ProbeConfig
	etag      string
	checks    map[string]*scheduled
	contactAt time.Time
}

// scheduled is one check's run loop; fingerprint changes when anything
// about the check (or where it reports) does.
type scheduled struct {
	fingerprint string
	cancel      context.CancelFunc
}

type cachedConfig struct {
	ETag   string            `json:"etag"`
	Config *spec.ProbeConfig `json:"config"`
}

// run polls the config until ctx ends, keeping each check's loop in step.
// A cached config starts the checks before the control plane answers.
func (p *probe) run(ctx context.Context) {
	p.loadCache()
	for {
		wait := p.poll(ctx)
		select {
		case <-ctx.Done():
			p.apply(ctx, nil, "")
			return
		case <-time.After(jitter(wait)):
		}
	}
}

func (p *probe) poll(ctx context.Context) time.Duration {
	p.mu.Lock()
	etag := p.etag
	p.mu.Unlock()
	config, newETag, err := p.api.config(ctx, etag)
	switch {
	case errors.Is(err, errRevoked):
		p.log.Error("The control plane refused this probe's token; no checks run until it is replaced")
		p.apply(ctx, nil, "")
		_ = os.Remove(p.cachePath)
		return revokedPoll
	case err != nil:
		if ctx.Err() == nil {
			p.log.Warn("Failed to fetch the config; running the last one", "error", err)
		}
		return retryPoll
	}
	p.mu.Lock()
	p.contactAt = time.Now()
	p.mu.Unlock()
	if config == nil {
		return p.interval()
	}
	p.apply(ctx, config, newETag)
	p.saveCache(config, newETag)
	p.log.Info("Config updated", "checks", len(config.Checks), "location", config.Probe.Location)
	return p.interval()
}

// apply starts, restarts and stops check loops to match config (nil stops
// them all).
func (p *probe) apply(ctx context.Context, config *spec.ProbeConfig, etag string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.config, p.etag = config, etag
	wanted := map[string]bool{}
	if config != nil {
		destinations := map[string]spec.Destination{}
		for _, destination := range config.Destinations {
			destinations[destination.ID] = destination
		}
		for _, check := range config.Checks {
			destination, ok := destinations[check.DestinationID]
			if !ok {
				continue
			}
			encoded, _ := json.Marshal(struct {
				Check       spec.ProbeCheck
				Destination spec.Destination
				Probe       spec.ProbeInfo
			}{check, destination, config.Probe})
			fingerprint := string(encoded)
			wanted[check.ID] = true
			p.sender.setDestination(destination, config.Probe)
			if current, ok := p.checks[check.ID]; ok {
				if current.fingerprint == fingerprint {
					continue
				}
				current.cancel()
			}
			checkCtx, cancel := context.WithCancel(ctx)
			p.checks[check.ID] = &scheduled{fingerprint: fingerprint, cancel: cancel}
			go p.loop(checkCtx, check, destination, config.Probe)
		}
		p.scheduleCloud(ctx, config, destinations, wanted)
	}
	for id, current := range p.checks {
		if !wanted[id] {
			current.cancel()
			delete(p.checks, id)
		}
	}
}

// loop runs one check on its slots (see nextRun).
func (p *probe) loop(ctx context.Context, check spec.ProbeCheck, destination spec.Destination, info spec.ProbeInfo) {
	interval := max(time.Duration(check.IntervalSeconds)*time.Second, spec.MinIntervalSeconds*time.Second)
	runner := &checks.Runner{Location: info.Location, Probe: info.Name, Policy: p.policy, Env: p.env}
	timer := time.NewTimer(time.Until(nextRun(time.Now(), check.ID, interval)))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		timer.Reset(time.Until(nextRun(time.Now(), check.ID, interval)))
		// In a probe group, one live member runs each monitor.
		if ctx.Err() != nil || !p.owns(check.ID) {
			continue
		}
		select {
		case p.slots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		p.running.Add(1)
		// A restart or shutdown doesn't cut a started run short: its result would be a false
		// failure, and the restarted loop only runs from the next slot.
		result := runner.Run(context.WithoutCancel(ctx), check)
		<-p.slots
		p.sender.add(destination, info, result)
		p.running.Add(-1)
	}
}

// waitForRuns waits up to timeout for scheduled runs in progress to finish.
func (p *probe) waitForRuns(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for p.running.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
}

// setMembers takes the group's live members from the control plane.
func (p *probe) setMembers(members []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !slices.Equal(p.members, members) {
		p.log.Info("Probe group members", "members", members)
	}
	p.members = members
}

// owns uses rendezvous hashing over the live members (itself included, so nothing goes unrun
// while it joins): members agree without talking, and when one leaves only its monitors move.
func (p *probe) owns(checkID string) bool {
	p.mu.Lock()
	members := p.members
	p.mu.Unlock()
	return owner(checkID, members, p.instance) == p.instance
}

func (p *probe) ownedCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.config == nil {
		return 0
	}
	count := 0
	for _, check := range p.config.Checks {
		if owner(check.ID, p.members, p.instance) == p.instance {
			count++
		}
	}
	return count
}

func owner(checkID string, members []string, self string) string {
	if self == "" {
		return ""
	}
	best, bestScore := "", uint64(0)
	consider := func(member string) {
		hash := fnv.New64a()
		_, _ = hash.Write([]byte(checkID + "\x00" + member))
		if score := hash.Sum64(); best == "" || score > bestScore || score == bestScore && member < best {
			best, bestScore = member, score
		}
	}
	for _, member := range members {
		consider(member)
	}
	if !slices.Contains(members, self) {
		consider(self)
	}
	return best
}

// nextRun is the check's first slot after now. Slots sit at a fixed offset (from the check's
// hash) within each interval of UTC wall-clock time, so they spread out, and every member and every
// restarted loop agrees on them: a monitor moving between members, or a loop restarted for a
// changed fingerprint, neither doubles nor skips a run.
func nextRun(now time.Time, checkID string, interval time.Duration) time.Time {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(checkID))
	period := uint64(interval)
	offset := hash.Sum64() % period
	at := uint64(now.UnixNano())
	since := (at + period - offset) % period
	return time.Unix(0, int64(at-since+period)).UTC()
}

// runTasks answers the editor's Test runs through a long-poll.
func (p *probe) runTasks(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		task, err := p.api.nextTask(ctx, p.taskWait)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			wait := backoff
			if errors.Is(err, errRevoked) {
				wait = revokedPoll
			} else {
				p.log.Warn("Waiting for tests failed", "error", err, "retryIn", backoff)
				backoff = min(backoff*2, maxTaskBackoff)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			continue
		}
		backoff = time.Second
		if task != nil {
			go p.runTask(ctx, *task)
		}
	}
}

func (p *probe) runTask(ctx context.Context, task spec.Task) {
	if task.Cloud != nil {
		p.runCloudTask(ctx, task)
		return
	}
	p.mu.Lock()
	info := spec.ProbeInfo{Location: "unknown", Name: "obzly-probe"}
	if p.config != nil {
		info = p.config.Probe
	}
	p.mu.Unlock()
	select {
	case p.testSlots <- struct{}{}:
	case <-ctx.Done():
		return
	}
	runner := &checks.Runner{Location: info.Location, Probe: info.Name, Policy: p.policy, Env: p.env}
	result := runner.Run(ctx, task.Check)
	<-p.testSlots
	if err := p.api.postResult(ctx, task.ID, result); err != nil {
		p.log.Warn("Failed to report a test result", "task", task.ID, "error", err)
	}
}

// serveHealth answers 200 while the probe has a config and has reached the
// control plane recently, else 503.
func (p *probe) serveHealth(address string) {
	handler := http.NewServeMux()
	handler.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		p.mu.Lock()
		hasConfig, contact, interval := p.config != nil, p.contactAt, p.intervalLocked()
		p.mu.Unlock()
		switch {
		case !hasConfig:
			http.Error(w, "no config yet", http.StatusServiceUnavailable)
		case time.Since(contact) > 3*interval:
			http.Error(
				w,
				"the control plane hasn't answered for "+time.Since(contact).Round(time.Second).String(),
				http.StatusServiceUnavailable,
			)
		default:
			_, _ = w.Write([]byte("ok"))
		}
	})
	server := &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	if err := server.ListenAndServe(); err != nil {
		p.log.Error("Health endpoint stopped", "error", err)
	}
}

func (p *probe) interval() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.intervalLocked()
}

func (p *probe) intervalLocked() time.Duration {
	if p.config != nil && p.config.PollIntervalSeconds > 0 {
		return time.Duration(p.config.PollIntervalSeconds) * time.Second
	}
	return defaultPoll
}

func (p *probe) loadCache() {
	data, err := os.ReadFile(p.cachePath)
	if err != nil {
		return
	}
	var cached cachedConfig
	if json.Unmarshal(data, &cached) != nil || cached.Config == nil {
		return
	}
	p.apply(context.Background(), cached.Config, cached.ETag)
	p.log.Info("Running the cached config until the control plane answers", "checks", len(cached.Config.Checks))
}

// saveCache keeps the config (secrets and keys included) readable by this
// user only.
func (p *probe) saveCache(config *spec.ProbeConfig, etag string) {
	data, err := json.Marshal(cachedConfig{ETag: etag, Config: config})
	if err != nil {
		return
	}
	temporary := p.cachePath + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		p.log.Warn("Failed to cache the config", "error", err)
		return
	}
	if err := os.Rename(temporary, p.cachePath); err != nil {
		p.log.Warn("Failed to cache the config", "error", err)
	}
}

// jitter spreads polls ±10% so probes started together don't poll
// together.
func jitter(d time.Duration) time.Duration {
	spread := int64(d) / 10
	if spread <= 0 {
		return d
	}
	return d - time.Duration(spread) + time.Duration(rand.Int64N(2*spread))
}
