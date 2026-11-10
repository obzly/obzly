package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"obzly/internal/cloud/discovery"
	"obzly/internal/synthetics/spec"
)

const (
	// cloudFirstScanWithin: a new connection's first discovery comes soon,
	// at a random point so a group's connections don't all start together.
	cloudFirstScanWithin = 2 * time.Minute
	cloudTestTimeout     = 100 * time.Second
	cloudSendTimeout     = 2 * time.Minute
	cloudReportTimeout   = 30 * time.Second
	cloudHTTPTimeout     = 60 * time.Second
	// cloudTokenMargin: a cached federated token is replaced this long
	// before it expires, so an exchange never gets one about to.
	cloudTokenMargin = 2 * time.Minute
)

// cloudTokenCache keeps the control plane's federated tokens until shortly
// before they expire: an Organization's accounts all sign in with one.
type cloudTokenCache struct {
	mu     sync.Mutex
	tokens map[string]spec.CloudToken
}

// cloudState is what the last discovery found, with the health scans'
// updates; kept on disk so a restart doesn't rediscover early.
type cloudState struct {
	Scope       string               `json:"scope"`
	InventoryAt time.Time            `json:"inventoryAt"`
	Resources   []discovery.Resource `json:"resources"`
}

// cloudKey keeps cloud connections apart from checks in the schedule and
// in group ownership.
func cloudKey(id string) string { return "cloud:" + id }

// scheduleCloud starts a scan loop per connection; called from apply with
// p.mu held.
func (p *probe) scheduleCloud(ctx context.Context, config *spec.ProbeConfig,
	destinations map[string]spec.Destination, wanted map[string]bool,
) {
	for _, connection := range config.Cloud {
		destination, ok := destinations[connection.DestinationID]
		if !ok {
			continue
		}
		encoded, _ := json.Marshal(struct {
			Connection  spec.CloudConnection
			Destination spec.Destination
			Probe       spec.ProbeInfo
		}{connection, destination, config.Probe})
		key, fingerprint := cloudKey(connection.ID), string(encoded)
		wanted[key] = true
		if current, ok := p.checks[key]; ok {
			if current.fingerprint == fingerprint {
				continue
			}
			current.cancel()
		}
		loopCtx, cancel := context.WithCancel(ctx)
		p.checks[key] = &scheduled{fingerprint: fingerprint, cancel: cancel}
		go p.cloudLoop(loopCtx, connection, destination, config.Probe)
	}
}

// cloudLoop runs one connection on the member that owns it: a discovery every interval, health
// scans in between. Only health scans send; one follows each discovery so new resources go out now.
func (p *probe) cloudLoop(ctx context.Context, connection spec.CloudConnection, destination spec.Destination,
	info spec.ProbeInfo,
) {
	if readsCost(connection) {
		go p.costLoop(ctx, connection, destination)
	}
	if hasUse(connection, spec.UsePosture) {
		go p.postureLoop(ctx, connection, destination)
	}
	if !hasUse(connection, spec.UseInventory) {
		<-ctx.Done()
		return
	}
	inventoryEvery, healthEvery := cloudIntervals(connection)
	run := &cloudRun{p: p, connection: connection, destination: destination, info: info,
		throttle: discovery.NewThrottle(), healthEvery: healthEvery, aws: discovery.NewAWSCache()}
	state := p.loadCloudState(connection)

	firstScan := time.Duration(rand.Int64N(int64(cloudFirstScanWithin)))
	inventoryIn, healthIn := firstScan, 24*time.Hour
	if state != nil {
		inventoryIn = max(time.Until(state.InventoryAt.Add(inventoryEvery)), firstScan)
		healthIn = time.Duration(rand.Int64N(int64(min(healthEvery, cloudFirstScanWithin))))
	}
	inventory, health := time.NewTimer(inventoryIn), time.NewTimer(healthIn)
	defer inventory.Stop()
	defer health.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-inventory.C:
		case <-health.C:
			health.Reset(healthEvery)
			if !p.owns(cloudKey(connection.ID)) {
				continue
			}
			// A member that hasn't discovered yet (it just took the
			// connection over) discovers first.
			if state != nil {
				state = run.health(ctx, state)
				continue
			}
		}
		inventory.Reset(inventoryEvery)
		if !p.owns(cloudKey(connection.ID)) {
			continue
		}
		var failed bool
		state, failed = run.inventory(ctx, state)
		health.Reset(healthEvery)
		if failed {
			// Don't wait hours after a failed sign-in or listing (a role not
			// deployed yet): try again at the health interval.
			inventory.Reset(healthEvery)
		}
	}
}

// cloudRun is one connection's scans; its throttle paces discovery and
// health scans together, and aws keeps their AWS sign-ins.
type cloudRun struct {
	p           *probe
	connection  spec.CloudConnection
	destination spec.Destination
	info        spec.ProbeInfo
	throttle    *discovery.Throttle
	healthEvery time.Duration
	aws         *discovery.AWSCache
	// saved: the digest of the state last written, to skip unchanged ones.
	saved [sha256.Size]byte
}

// inventory discovers everything, keeping what a failed or partial scan missed, then
// health-scans the result; failed means it found nothing and didn't work.
func (r *cloudRun) inventory(ctx context.Context, previous *cloudState) (*cloudState, bool) {
	inventoryEvery, _ := cloudIntervals(r.connection)
	scanCtx, cancel := context.WithTimeout(ctx, scanTimeout(inventoryEvery, 5*time.Minute, 2*time.Hour))
	result := discovery.Discover(scanCtx, r.connection, r.options(false))
	cancel()
	if ctx.Err() != nil {
		return previous, false
	}
	result.Scan.Probe = r.p.instance
	r.report(ctx, result.Scan)
	r.p.log.Info("Cloud discovery", "connection", r.connection.Name, "resources", result.Scan.Resources,
		"seconds", result.Scan.Duration, "throttled", result.Scan.Throttled, "partial", result.Scan.Partial)

	found := result.Resources
	failed := len(found) == 0 && result.Scan.Uses[spec.UseInventory].State != spec.UseOK
	if previous == nil && failed {
		return nil, true
	}
	if previous != nil {
		switch {
		case failed:
			// Signing in or listing failed: keep the last inventory.
			return r.health(ctx, previous), true
		case result.Scan.Partial:
			found = discovery.Merge(previous.Resources, found)
		}
		found = discovery.CarryHealth(previous.Resources, found)
	}
	state := &cloudState{Scope: cloudScope(r.connection), InventoryAt: result.Scan.StartedAt, Resources: found}
	return r.health(ctx, state), false
}

// health refreshes state's resources, sends those still there to the
// destination and saves the state.
func (r *cloudRun) health(ctx context.Context, state *cloudState) *cloudState {
	scanCtx, cancel := context.WithTimeout(ctx, scanTimeout(r.healthEvery, 2*time.Minute, 30*time.Minute))
	result := discovery.CheckHealth(scanCtx, r.connection, state.Resources, r.options(false))
	cancel()
	if ctx.Err() != nil {
		return state
	}
	result.Scan.Probe = r.p.instance
	if len(result.Resources) > 0 {
		// A fresh context: the scan's may have run out, and what it found
		// is still worth sending.
		sendCtx, cancelSend := context.WithTimeout(ctx, cloudSendTimeout)
		points := discovery.Gauges(r.connection, result.Resources, int(r.healthEvery/time.Second))
		err := r.p.sender.out.SendGauges(sendCtx, r.destination, r.info, time.Now().UTC(), points)
		cancelSend()
		if err != nil {
			result.Scan.Errors = append(result.Scan.Errors, "sending to the destination: "+err.Error())
		}
	}
	r.report(ctx, result.Scan)
	updated := &cloudState{
		Scope: state.Scope, InventoryAt: state.InventoryAt,
		Resources: discovery.Merge(state.Resources, result.Resources),
	}
	r.saved = r.p.saveCloudState(r.connection, updated, r.saved)
	return updated
}

func (r *cloudRun) report(ctx context.Context, scan spec.CloudScan) {
	reportCtx, cancel := context.WithTimeout(ctx, cloudReportTimeout)
	defer cancel()
	if err := r.p.api.reportCloudScan(reportCtx, r.connection.ID, scan); err != nil {
		r.p.log.Warn("Failed to report a cloud scan", "connection", r.connection.Name, "kind", scan.Kind,
			"error", err)
	}
}

func (r *cloudRun) options(sample bool) discovery.Options {
	options := r.p.cloudOptions(r.connection, sample)
	options.Throttle, options.AWS = r.throttle, r.aws
	return options
}

// runCloudTask is the wizard's Test access: a dry scan of the first account and a couple of
// regions, nothing sent to a Site. The connection isn't saved, so its token is issued for the task.
func (p *probe) runCloudTask(ctx context.Context, task spec.Task) {
	scanCtx, cancel := context.WithTimeout(ctx, cloudTestTimeout)
	defer cancel()
	options := p.cloudOptions(*task.Cloud, true)
	options.Token = func(ctx context.Context) (string, error) {
		return p.cloudTokens.get(ctx, "task:"+task.ID, func(ctx context.Context) (spec.CloudToken, error) {
			return p.api.taskCloudToken(ctx, task.ID)
		})
	}
	connection := *task.Cloud
	result := discovery.Result{Scan: spec.CloudScan{
		Kind: spec.ScanInventory, Uses: map[string]spec.UseResult{}, StartedAt: time.Now().UTC(),
	}}
	if hasUse(connection, spec.UseInventory) {
		result = discovery.Discover(scanCtx, connection, options)
	}
	if hasUse(connection, spec.UseCost) {
		result.Scan.Uses[spec.UseCost] = discovery.TestCost(scanCtx, connection, options, time.Now())
	}
	if hasUse(connection, spec.UsePosture) {
		// A sampled refresh: what's covered natively, what falls back, and what's denied.
		posture := discovery.CollectPosture(scanCtx, connection, options, time.Now())
		result.Scan.Uses[spec.UsePosture] = posture.Scan.Uses[spec.UsePosture]
		result.Scan.Posture = posture.Scan.Posture
	}
	result.Scan.Probe = p.instance
	if err := p.api.postCloudResult(ctx, task.ID, result.Scan); err != nil {
		p.log.Warn("Failed to report a test access result", "task", task.ID, "error", err)
	}
}

func (p *probe) cloudOptions(connection spec.CloudConnection, sample bool) discovery.Options {
	return discovery.Options{
		HTTP: &http.Client{Timeout: cloudHTTPTimeout},
		Token: func(ctx context.Context) (string, error) {
			return p.cloudTokens.get(ctx, connection.ID, func(ctx context.Context) (spec.CloudToken, error) {
				return p.api.cloudToken(ctx, connection.ID)
			})
		},
		Sample: sample,
	}
}

func (c *cloudTokenCache) get(
	ctx context.Context, key string, fetch func(context.Context) (spec.CloudToken, error),
) (string, error) {
	now := time.Now().UTC()
	c.mu.Lock()
	if token, ok := c.tokens[key]; ok && now.Before(token.ExpiresAt.Add(-cloudTokenMargin)) {
		c.mu.Unlock()
		return token.Token, nil
	}
	c.mu.Unlock()
	token, err := fetch(ctx)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tokens == nil {
		c.tokens = map[string]spec.CloudToken{}
	}
	for other, cached := range c.tokens {
		if !now.Before(cached.ExpiresAt) {
			delete(c.tokens, other)
		}
	}
	c.tokens[key] = token
	return token.Token, nil
}

// loadCloudState is the saved state of connection, if its scope hasn't
// changed since.
func (p *probe) loadCloudState(connection spec.CloudConnection) *cloudState {
	data, err := os.ReadFile(p.cloudStatePath(connection))
	if err != nil {
		return nil
	}
	var state cloudState
	if json.Unmarshal(data, &state) != nil || state.Scope != cloudScope(connection) {
		return nil
	}
	return &state
}

// saveCloudState skips the write when the digest matches last (a health scan
// that changed nothing); it returns the digest of what is on disk now.
func (p *probe) saveCloudState(
	connection spec.CloudConnection, state *cloudState, last [sha256.Size]byte,
) [sha256.Size]byte {
	path := p.cloudStatePath(connection)
	data, err := json.Marshal(state)
	if err != nil {
		p.log.Warn("Failed to save a cloud connection's state", "connection", connection.Name, "error", err)
		return last
	}
	digest := sha256.Sum256(data)
	if digest == last {
		return last
	}
	err = os.MkdirAll(filepath.Dir(path), 0o700)
	if err == nil {
		err = os.WriteFile(path+".tmp", data, 0o600)
	}
	if err == nil {
		err = os.Rename(path+".tmp", path)
	}
	if err != nil {
		p.log.Warn("Failed to save a cloud connection's state", "connection", connection.Name, "error", err)
		return last
	}
	return digest
}

func (p *probe) cloudStatePath(connection spec.CloudConnection) string {
	return filepath.Join(filepath.Dir(p.cachePath), "cloud", connection.ID+".json")
}

// cloudScope identifies what a connection discovers, so a saved inventory
// isn't reused after its account, access or types change.
func cloudScope(connection spec.CloudConnection) string {
	connection.Name, connection.IntervalSeconds, connection.HealthIntervalSeconds = "", 0, 0
	connection.DestinationID = ""
	// Other uses (cost) and their settings don't change what discovery finds; keeping inventory
	// alone leaves the scope of connections saved before them unchanged.
	uses := []string{}
	for _, use := range connection.Uses {
		if use == spec.UseInventory {
			uses = append(uses, use)
		}
	}
	connection.Cost, connection.Posture, connection.Uses = nil, nil, uses
	encoded, _ := json.Marshal(connection)
	return string(encoded)
}

// cloudIntervals are a connection's discovery and health intervals, with
// the defaults for a control plane that sends neither.
func cloudIntervals(connection spec.CloudConnection) (time.Duration, time.Duration) {
	inventory := connection.IntervalSeconds
	if inventory <= 0 {
		inventory = spec.DefaultCloudInventorySeconds
	}
	health := connection.HealthIntervalSeconds
	if health <= 0 {
		health = spec.DefaultCloudHealthSeconds
	}
	inventory = max(inventory, spec.MinCloudHealthSeconds)
	health = min(max(health, spec.MinCloudHealthSeconds), inventory)
	return time.Duration(inventory) * time.Second, time.Duration(health) * time.Second
}

// scanTimeout leaves a fifth of the interval spare, within bounds.
func scanTimeout(interval, least, most time.Duration) time.Duration {
	return min(max(interval*4/5, least), most)
}
