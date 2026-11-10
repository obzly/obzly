package synthetics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/valkey-io/valkey-go"

	"obzly/internal/cache"
	"obzly/internal/config"
	"obzly/internal/features/access_tokens"
	users_models "obzly/internal/features/users/models"
	"obzly/internal/synthetics/checks"
	"obzly/internal/synthetics/spec"
	"obzly/internal/util/egress"
	"obzly/internal/util/logger"
)

const (
	// PollIntervalSeconds: how often a probe asks for its config.
	PollIntervalSeconds = 180
	// MaxTaskWaitSeconds caps a task long-poll; probes behind a proxy with
	// a shorter read timeout (CloudFront's default is 30 s) ask for less.
	MaxTaskWaitSeconds = 55
	taskTTL            = 10 * time.Minute
	tokenCacheTTL      = 30 * time.Second
	touchEvery         = time.Minute
	memberTouchEvery   = 15 * time.Second
	valkeyTimeout      = 5 * time.Second
	// maxGroupMembers caps a group's instances seen within memberForgetAfter, so a token holder
	// can't invent members without end and skew who runs what.
	maxGroupMembers = 64
	// memberForgetAfter: an instance silent this long is gone (a spot node
	// reclaimed, a pod rescheduled under a new name) and leaves the list.
	memberForgetAfter = time.Hour
	// maxTaskPolls caps one group's open task long-polls per replica, each
	// holding a Valkey connection for up to MaxTaskWaitSeconds.
	maxTaskPolls = maxGroupMembers
	// configCacheFor rebuilds a cached probe config at least this often,
	// whatever the change marker says.
	configCacheFor = 10 * time.Minute

	tokenCachePrefix = "synthetics:probe-token:"
	taskQueuePrefix  = "synthetics:tasks:"
	taskOwnerPrefix  = "synthetics:task-owner:"
	taskResultPrefix = "synthetics:result:"
	testPrefix       = "synthetics:test:"
	// invalidToken caches a token that matched nothing, so guessing doesn't
	// cost a database query each time.
	invalidToken = "invalid"
)

var (
	ErrInvalidProbeToken = errors.New("invalid probe token")
	ErrTooManyMembers    = fmt.Errorf("this probe group already has %d instances", maxGroupMembers)
	ErrTooManyTaskPolls  = errors.New("too many instances of this probe group are waiting for tests")
)

var (
	lastTouched    sync.Map // probe id, or probe id/instance -> time.Time
	refusedMembers sync.Map // probe id/instance -> time.Time
	taskPolls      sync.Map // probe id -> *atomic.Int32
	lastSweep      atomic.Int64
	probeConfigs   = &configCache{entries: map[uuid.UUID]cachedConfig{}}
)

// configCache keeps each group's built config while configVersion is unchanged, so polls and
// 304s don't decrypt every monitor's secrets again.
type configCache struct {
	mu      sync.Mutex
	entries map[uuid.UUID]cachedConfig
}

type cachedConfig struct {
	marker  string
	builtAt time.Time
	config  *spec.ProbeConfig
	etag    string
}

// AuthenticateProbe resolves a probe token. Revoking one drops it from the
// cache, so the at most tokenCacheTTL lag is for other replicas only.
func (s *Service) AuthenticateProbe(raw string) (*Probe, error) {
	if !strings.HasPrefix(raw, spec.TokenPrefix) {
		return nil, ErrInvalidProbeToken
	}
	hash := access_tokens.HashToken(raw)
	client := cache.GetCache()
	ctx, cancel := context.WithTimeout(context.Background(), valkeyTimeout)
	defer cancel()
	if cached, err := client.Do(ctx, client.B().Get().Key(tokenCachePrefix+hash).Build()).ToString(); err == nil {
		if cached == invalidToken {
			return nil, ErrInvalidProbeToken
		}
		var probe Probe
		if json.Unmarshal([]byte(cached), &probe) == nil {
			return &probe, nil
		}
	}
	probe, err := s.repository.probeByToken(hash)
	if err != nil {
		return nil, err
	}
	if probe == nil {
		_ = client.Do(ctx, client.B().Set().Key(tokenCachePrefix+hash).Value(invalidToken).Ex(tokenCacheTTL).Build()).
			Error()
		return nil, ErrInvalidProbeToken
	}
	if encoded, err := json.Marshal(probe); err == nil {
		_ = client.Do(ctx, client.B().Set().Key(tokenCachePrefix+hash).Value(string(encoded)).Ex(tokenCacheTTL).Build()).
			Error()
	}
	return probe, nil
}

// TouchProbe records the group (at most once a minute) and an instance (every memberTouchEvery) as
// alive. A new instance beyond maxGroupMembers gets ErrTooManyMembers; instances unseen for a
// day are forgotten.
func (s *Service) TouchProbe(probe *Probe, version, instance string, checks int) error {
	now := s.now()
	key := probe.ID.String() + "/" + instance
	if refused, ok := refusedMembers.Load(key); ok {
		if at, ok := refused.(time.Time); ok && now.Sub(at) < memberTouchEvery {
			return ErrTooManyMembers
		}
	}
	previous, _ := lastTouched.Load(key)
	if at, ok := previous.(time.Time); !ok || now.Sub(at) >= memberTouchEvery {
		member := &ProbeMember{ProbeID: probe.ID, Name: instance, Version: version, Checks: checks, LastSeenAt: now}
		added, err := s.repository.touchMember(member, maxGroupMembers, now.Add(-memberForgetAfter))
		switch {
		case err != nil:
			logger.GetLogger().Warn("Failed to record probe instance activity", "probe", probe.ID, "error", err)
		case !added:
			refusedMembers.Store(key, now)
			logger.GetLogger().Warn("Refused a probe instance: its group has too many", "probe", probe.ID,
				"instance", instance, "limit", maxGroupMembers)
			return ErrTooManyMembers
		}
		lastTouched.Store(key, now)
	}
	if previous, ok := lastTouched.Load(probe.ID); ok {
		if at, ok := previous.(time.Time); ok && now.Sub(at) < touchEvery {
			return nil
		}
	}
	lastTouched.Store(probe.ID, now)
	if err := s.repository.touchProbe(probe.ID, version, now); err != nil {
		logger.GetLogger().Warn("Failed to record probe activity", "probe", probe.ID, "error", err)
	}
	if err := s.repository.forgetMembers(probe.ID, now.Add(-memberForgetAfter)); err != nil {
		logger.GetLogger().Warn("Failed to forget old probe instances", "probe", probe.ID, "error", err)
	}
	sweepTouched(now)
	return nil
}

// LeaveGroup takes a stopping instance out of the live members at once, rather than after
// memberLiveFor of silence. It stays listed (not live) like any member that went quiet.
func (s *Service) LeaveGroup(probe *Probe, instance string) error {
	// A restarted instance with the same name (a StatefulSet pod) must be recorded on its first poll.
	lastTouched.Delete(probe.ID.String() + "/" + instance)
	return s.repository.leaveMember(probe.ID, instance, s.now().Add(-memberLiveFor-time.Second))
}

// LiveMembers is the group's instances that run its monitors now.
func (s *Service) LiveMembers(probe *Probe) []string {
	names, err := s.repository.liveMembers(probe.ID, s.now().Add(-memberLiveFor))
	if err != nil {
		logger.GetLogger().Warn("Failed to list probe instances", "probe", probe.ID, "error", err)
		return nil
	}
	return names
}

// ProbeConfig is the probe's enabled checks with secrets filled in, their destinations and an
// ETag; rebuilt only when something it comes from changed.
func (s *Service) ProbeConfig(probe *Probe) (*spec.ProbeConfig, string, error) {
	marker, err := s.repository.configVersion()
	if err != nil {
		return nil, "", err
	}
	marker += "|" + probe.Name + "|" + probe.Location
	now := s.now()
	probeConfigs.mu.Lock()
	cached, ok := probeConfigs.entries[probe.ID]
	probeConfigs.mu.Unlock()
	if ok && cached.marker == marker && now.Sub(cached.builtAt) < configCacheFor {
		return cached.config, cached.etag, nil
	}
	config, etag, err := s.buildProbeConfig(probe)
	if err != nil {
		return nil, "", err
	}
	probeConfigs.mu.Lock()
	probeConfigs.entries[probe.ID] = cachedConfig{marker: marker, builtAt: now, config: config, etag: etag}
	probeConfigs.mu.Unlock()
	return config, etag, nil
}

func (s *Service) buildProbeConfig(probe *Probe) (*spec.ProbeConfig, string, error) {
	all, err := s.repository.enabledChecks()
	if err != nil {
		return nil, "", err
	}
	destinations, err := s.destinationsByID()
	if err != nil {
		return nil, "", err
	}
	config := &spec.ProbeConfig{
		Probe:               spec.ProbeInfo{ID: probe.ID.String(), Name: probe.Name, Location: probe.Location},
		PollIntervalSeconds: PollIntervalSeconds,
		Destinations:        []spec.Destination{},
		Checks:              []spec.ProbeCheck{},
	}
	used := map[uuid.UUID]bool{}
	use := func(destination *Destination) error {
		if used[destination.ID] {
			return nil
		}
		used[destination.ID] = true
		key, err := s.secrets.Decrypt(destination.APIKeyEncrypted)
		if err != nil {
			return fmt.Errorf("failed to decrypt the key of destination %q: %w", destination.Name, err)
		}
		config.Destinations = append(
			config.Destinations,
			spec.Destination{ID: destination.ID.String(), OTLPURL: destination.OTLPURL, APIKey: key},
		)
		return nil
	}
	for _, check := range all {
		if !check.runsAt(probe.Location) {
			continue
		}
		destination, ok := destinations[check.DestinationID]
		if !ok {
			continue
		}
		definition, redact, err := s.resolvedDefinition(check, nil)
		if err != nil {
			return nil, "", err
		}
		config.Checks = append(config.Checks, spec.ProbeCheck{
			ID: check.ID.String(), Name: check.Name, IntervalSeconds: check.IntervalSeconds,
			DestinationID: destination.ID.String(), Definition: definition,
			Target: spec.Definition(check.Definition).TargetLabel(), Redact: redact,
		})
		if err := use(destination); err != nil {
			return nil, "", err
		}
	}
	connections, err := s.cloudConnectionsFor(probe)
	if err != nil {
		return nil, "", err
	}
	tags, err := trackedCostTags()
	if err != nil {
		return nil, "", err
	}
	for _, connection := range connections {
		destination, ok := destinations[connection.DestinationID]
		if !ok {
			continue
		}
		config.Cloud = append(config.Cloud, withTrackedTags(connection.wire(), tags))
		if err := use(destination); err != nil {
			return nil, "", err
		}
	}
	sort.Slice(
		config.Destinations,
		func(i, j int) bool { return config.Destinations[i].ID < config.Destinations[j].ID },
	)
	encoded, err := json.Marshal(config)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(encoded)
	return config, `"` + hex.EncodeToString(sum[:16]) + `"`, nil
}

// CreateTest queues the definition for one online probe per location (all
// locations with one when none are given), or runs it here.
func (s *Service) CreateTest(ctx context.Context, user *users_models.User, request TestRequest) (*TestRun, error) {
	if !user.CanUpdateSettings() {
		return nil, ErrForbidden
	}
	check, err := s.testCheck(request)
	if err != nil {
		return nil, err
	}
	run := &TestRun{ID: uuid.NewString()}
	if request.FromControlPlane {
		// Public addresses only, as for alert webhooks: unlike a probe, the
		// control plane sits next to Obzly's own services.
		env := config.GetEnv()
		allow, _ := egress.ParseCIDRs(env.AlertEgressAllowlist)
		deny, _ := egress.ParseCIDRs(env.AlertEgressDenylist)
		runner := &checks.Runner{
			Location:         "control-plane",
			Probe:            "control-plane",
			Policy:           egress.PublicOnly(allow, deny),
			FromControlPlane: true,
		}
		result := runner.Run(ctx, check)
		run.Tasks = []TestTask{
			{ID: uuid.NewString(), Location: result.Location, Probe: result.Probe, Status: TaskDone, Result: &result},
		}
		return run, nil
	}

	byLocation, err := s.onlineProbesByLocation()
	if err != nil {
		return nil, err
	}
	locations := request.Locations
	if len(locations) == 0 {
		for location := range byLocation {
			locations = append(locations, location)
		}
		sort.Strings(locations)
	}
	if len(locations) == 0 {
		return nil, errors.New("no probe is online; start one, or test from the control plane")
	}
	client := cache.GetCache()
	valkeyCtx, cancel := context.WithTimeout(ctx, valkeyTimeout)
	defer cancel()
	for _, location := range locations {
		probe, ok := byLocation[location]
		if !ok {
			run.Tasks = append(run.Tasks, TestTask{ID: uuid.NewString(), Location: location, Status: TaskNoProbe})
			continue
		}
		task := spec.Task{ID: uuid.NewString(), Check: check}
		encoded, err := json.Marshal(task)
		if err != nil {
			return nil, err
		}
		queue := taskQueuePrefix + probe.ID.String()
		for _, result := range client.DoMulti(valkeyCtx,
			client.B().Set().Key(taskOwnerPrefix+task.ID).Value(probe.ID.String()).Ex(taskTTL).Build(),
			client.B().Rpush().Key(queue).Element(string(encoded)).Build(),
			client.B().Expire().Key(queue).Seconds(int64(taskTTL.Seconds())).Build(),
		) {
			if err := result.Error(); err != nil {
				return nil, fmt.Errorf("failed to queue the test: %w", err)
			}
		}
		run.Tasks = append(run.Tasks, TestTask{ID: task.ID, Location: location, Probe: probe.Name, Status: TaskQueued})
	}
	encoded, err := json.Marshal(run)
	if err != nil {
		return nil, err
	}
	if err := client.Do(valkeyCtx, client.B().Set().Key(testPrefix+run.ID).Value(string(encoded)).Ex(taskTTL).Build()).
		Error(); err != nil {
		return nil, fmt.Errorf("failed to store the test: %w", err)
	}
	return run, nil
}

// GetTest is a test run with each result that has arrived.
func (s *Service) GetTest(ctx context.Context, user *users_models.User, id string) (*TestRun, error) {
	if !user.CanUpdateSettings() {
		return nil, ErrForbidden
	}
	client := cache.GetCache()
	ctx, cancel := context.WithTimeout(ctx, valkeyTimeout)
	defer cancel()
	encoded, err := client.Do(ctx, client.B().Get().Key(testPrefix+id).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var run TestRun
	if err := json.Unmarshal([]byte(encoded), &run); err != nil {
		return nil, err
	}
	for i := range run.Tasks {
		task := &run.Tasks[i]
		if task.Status != TaskQueued {
			continue
		}
		result, err := client.Do(ctx, client.B().Get().Key(taskResultPrefix+task.ID).Build()).ToString()
		if valkey.IsValkeyNil(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var decoded spec.RunResult
		if json.Unmarshal([]byte(result), &decoded) == nil {
			task.Status, task.Result = TaskDone, &decoded
		}
	}
	return &run, nil
}

// NextTask waits up to wait for a test queued for probe; nil when none.
func (s *Service) NextTask(ctx context.Context, probe *Probe, wait time.Duration) (*spec.Task, error) {
	counter, _ := taskPolls.LoadOrStore(probe.ID, &atomic.Int32{})
	polls, _ := counter.(*atomic.Int32)
	defer polls.Add(-1)
	if polls.Add(1) > maxTaskPolls {
		return nil, ErrTooManyTaskPolls
	}
	client := cache.GetCache()
	ctx, cancel := context.WithTimeout(ctx, wait+valkeyTimeout)
	defer cancel()
	reply, err := client.Do(ctx, client.B().Blpop().Key(taskQueuePrefix+probe.ID.String()).Timeout(wait.Seconds()).Build()).
		AsStrSlice()
	if valkey.IsValkeyNil(err) || errors.Is(err, context.Canceled) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(reply) != 2 {
		return nil, nil
	}
	var task spec.Task
	if err := json.Unmarshal([]byte(reply[1]), &task); err != nil {
		return nil, err
	}
	return &task, nil
}

// PostResult stores a test result for the editor, if the task was this
// probe's.
func (s *Service) PostResult(ctx context.Context, probe *Probe, taskID string, result spec.RunResult) error {
	client := cache.GetCache()
	ctx, cancel := context.WithTimeout(ctx, valkeyTimeout)
	defer cancel()
	owner, err := client.Do(ctx, client.B().Get().Key(taskOwnerPrefix+taskID).Build()).ToString()
	if valkey.IsValkeyNil(err) || (err == nil && owner != probe.ID.String()) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	result.Location, result.Probe = probe.Location, probe.Name
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return client.Do(ctx, client.B().Set().Key(taskResultPrefix+taskID).Value(string(encoded)).Ex(taskTTL).Build()).
		Error()
}

func (s *Service) testCheck(request TestRequest) (spec.ProbeCheck, error) {
	definition := request.Check.Definition
	if err := definition.Normalize(); err != nil {
		return spec.ProbeCheck{}, err
	}
	stored := &Check{Definition: DefinitionJSON(definition)}
	id := ""
	if request.CheckID != nil {
		existing, err := s.findCheck(*request.CheckID)
		if err != nil {
			return spec.ProbeCheck{}, err
		}
		stored.SecretsEncrypted, id = existing.SecretsEncrypted, existing.ID.String()
	}
	resolved, redact, err := s.resolvedDefinition(stored, request.Check.Secrets)
	if err != nil {
		return spec.ProbeCheck{}, err
	}
	name := strings.TrimSpace(request.Check.Name)
	if name == "" {
		name = "Test"
	}
	return spec.ProbeCheck{
		ID: id, Name: name, Definition: resolved, Target: definition.TargetLabel(), Redact: redact,
	}, nil
}

// resolvedDefinition fills the check's placeholders from its stored secrets, overridden by extra
// (unsaved editor values), and returns the filled values so results can redact them.
func (s *Service) resolvedDefinition(check *Check, extra map[string]string) (spec.Definition, []string, error) {
	values, err := s.decryptSecrets(check.SecretsEncrypted)
	if err != nil {
		return spec.Definition{}, nil, err
	}
	for name, value := range extra {
		if value != "" {
			values[name] = value
		}
	}
	definition := spec.Definition(check.Definition)
	var used []string
	for _, name := range definition.Placeholders() {
		if value, ok := values[name]; ok {
			used = append(used, value)
		}
	}
	return definition.Resolve(func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}), used, nil
}

// onlineProbesByLocation picks, per location, the probe seen most recently.
func (s *Service) onlineProbesByLocation() (map[string]*Probe, error) {
	probes, err := s.repository.probes()
	if err != nil {
		return nil, err
	}
	byLocation := map[string]*Probe{}
	for _, probe := range probes {
		if !s.isOnline(probe) {
			continue
		}
		if current, ok := byLocation[probe.Location]; !ok || probe.LastSeenAt.After(*current.LastSeenAt) {
			byLocation[probe.Location] = probe
		}
	}
	return byLocation, nil
}

// sweepTouched drops, at most every few minutes, the touch times of
// groups and instances gone quiet, so the maps don't grow without end.
func sweepTouched(now time.Time) {
	last := lastSweep.Load()
	if now.UnixNano()-last < int64(5*time.Minute) || !lastSweep.CompareAndSwap(last, now.UnixNano()) {
		return
	}
	for _, touched := range []*sync.Map{&lastTouched, &refusedMembers} {
		touched.Range(func(key, value any) bool {
			if at, ok := value.(time.Time); !ok || now.Sub(at) > time.Hour {
				touched.Delete(key)
			}
			return true
		})
	}
}

func forgetProbeTokens(hashes []string) {
	if len(hashes) == 0 {
		return
	}
	client := cache.GetCache()
	ctx, cancel := context.WithTimeout(context.Background(), valkeyTimeout)
	defer cancel()
	keys := make([]string, len(hashes))
	for i, hash := range hashes {
		keys[i] = tokenCachePrefix + hash
	}
	if err := client.Do(ctx, client.B().Del().Key(keys...).Build()).Error(); err != nil {
		logger.GetLogger().Warn("Failed to drop revoked probe tokens from the cache", "error", err)
	}
}
