package alerting

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"sync"
	"time"

	"obzly/internal/cache"
	users_repositories "obzly/internal/features/users/repositories"
	"obzly/internal/telemetry"
	"obzly/internal/util/leaderlock"

	"github.com/google/uuid"
)

const (
	dispatchTick  = 5 * time.Second
	dispatchBatch = 50
	// maxBatchesPerTick bounds one tick's catch-up, so a backlog yields to
	// the next tick now and then.
	maxBatchesPerTick = 100
	// maxAttempts: a notification still undelivered after this many tries
	// (about 40 minutes of backoff) is given up.
	maxAttempts = 8
	// sendParallelism: deliveries of one batch in flight at once, so a slow
	// receiver doesn't hold the batch (each send is bounded by sendTimeout).
	sendParallelism = 8
	// claimLease: how long a claimed notification is left to its
	// dispatcher; one whose dispatcher died mid-batch is retried after it.
	claimLease = 5 * time.Minute
	// breakerFailures consecutive failures of a channel pause its
	// deliveries for breakerPause, so a dead receiver isn't tried once for
	// every notification queued for it.
	breakerFailures = 5
	breakerPause    = time.Minute
	// keepOutbox: how long delivered and given-up notifications are kept.
	keepOutbox = 7 * 24 * time.Hour
	pruneEvery = time.Hour
	pruneBatch = 1000
)

// Dispatcher delivers the outbox. Rows are claimed with SKIP LOCKED and a
// lease, so any number of worker replicas can run it, and sent outside
// any transaction.
type Dispatcher struct {
	repo    *repository
	secrets *users_repositories.SecretKeyRepository
	sender  *sender
	log     *slog.Logger
	now     func() time.Time
	breaker breaker
}

// breaker counts each channel's consecutive failures and holds when its
// paused deliveries resume (per process).
type breaker struct {
	mu       sync.Mutex
	failures map[uuid.UUID]int
	until    map[uuid.UUID]time.Time
}

func (d *Dispatcher) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(dispatchTick)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			for range maxBatchesPerTick {
				handled, err := d.dispatchOnce(ctx)
				if err != nil {
					// Back off to the next tick instead of retrying at once.
					d.log.Error("notification dispatch failed", "error", err)
					break
				}
				if handled < dispatchBatch {
					break
				}
			}
		}
	}()
	go d.pruneLoop(ctx)
}

// dispatchOnce claims one batch of due notifications and delivers it, up
// to sendParallelism at once; how many it claimed.
func (d *Dispatcher) dispatchOnce(ctx context.Context) (int, error) {
	now := d.now()
	items, err := d.repo.claimOutbox(dispatchBatch, now, now.Add(claimLease))
	if err != nil || len(items) == 0 {
		return 0, err
	}
	silencesByProject := map[uuid.UUID][]*Silence{}
	var toSend []*OutboxItem
	for _, item := range items {
		if item.Status != StatusTest {
			silences, ok := silencesByProject[item.ProjectID]
			if !ok {
				if silences, err = d.repo.activeSilences(item.ProjectID, now); err != nil {
					return len(items), err
				}
				silencesByProject[item.ProjectID] = silences
			}
			if silenced(silences, item.Payload.Labels) {
				telemetry.AlertNotifications.WithLabelValues("silenced").Inc()
				err := d.repo.updateOutbox(item.ID, map[string]any{"sent_at": now, "last_error": "silenced"})
				if err != nil {
					return len(items), err
				}
				continue
			}
		}
		if until, paused := d.breaker.pausedUntil(item.ChannelID, now); paused {
			if err := d.repo.updateOutbox(item.ID, map[string]any{"next_attempt_at": until}); err != nil {
				return len(items), err
			}
			continue
		}
		toSend = append(toSend, item)
	}

	var wg sync.WaitGroup
	slots := make(chan struct{}, sendParallelism)
	errs := make([]error, len(toSend))
	for i, item := range toSend {
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer func() { <-slots; wg.Done() }()
			errs[i] = d.record(item, d.deliver(ctx, item))
		}()
	}
	wg.Wait()
	return len(items), errors.Join(errs...)
}

func (d *Dispatcher) deliver(ctx context.Context, item *OutboxItem) error {
	channel, err := d.repo.channelByID(item.ChannelID)
	if err != nil {
		return err
	}
	cfg, err := d.config(channel)
	if err != nil {
		return err
	}
	return d.sender.send(ctx, channel.Kind, cfg, item.Payload)
}

// record stores one delivery's outcome and feeds the channel's breaker.
func (d *Dispatcher) record(item *OutboxItem, sendErr error) error {
	done := d.now()
	updates := map[string]any{"attempts": item.Attempts + 1}
	switch {
	case sendErr == nil:
		updates["sent_at"], updates["last_error"] = done, ""
		telemetry.AlertNotifications.WithLabelValues("sent").Inc()
		telemetry.AlertNotificationLatency.Observe(done.Sub(item.CreatedAt).Seconds())
	case item.Attempts+1 >= maxAttempts:
		updates["failed_at"], updates["last_error"] = done, truncate(sendErr.Error(), 1000)
		telemetry.AlertNotifications.WithLabelValues("failed").Inc()
	default:
		updates["next_attempt_at"] = done.Add(backoff(item.Attempts + 1))
		updates["last_error"] = truncate(sendErr.Error(), 1000)
		telemetry.AlertNotifications.WithLabelValues("retried").Inc()
	}
	d.breaker.observe(item.ChannelID, sendErr == nil, done)
	return d.repo.updateOutbox(item.ID, updates)
}

func (d *Dispatcher) config(channel *NotificationChannel) (ChannelConfig, error) {
	var cfg ChannelConfig
	plain, err := d.secrets.Decrypt(channel.ConfigEncrypted)
	if err != nil {
		return cfg, err
	}
	return cfg, json.Unmarshal([]byte(plain), &cfg)
}

// pruneLoop deletes old finished notifications on one replica at a time.
func (d *Dispatcher) pruneLoop(ctx context.Context) {
	ticker := time.NewTicker(pruneEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if _, err := leaderlock.RunExclusive(ctx, cache.GetCache(), "alert-outbox-prune", 10*time.Minute,
			d.prune); err != nil {
			d.log.Error("notification outbox prune failed", "error", err)
		}
	}
}

func (d *Dispatcher) prune(ctx context.Context) error {
	before := d.now().Add(-keepOutbox)
	for ctx.Err() == nil {
		deleted, err := d.repo.pruneOutbox(ctx, before, pruneBatch)
		if err != nil || deleted < pruneBatch {
			return err
		}
	}
	return ctx.Err()
}

func (b *breaker) pausedUntil(channelID uuid.UUID, now time.Time) (time.Time, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	until, ok := b.until[channelID]
	return until, ok && now.Before(until)
}

func (b *breaker) observe(channelID uuid.UUID, ok bool, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failures == nil {
		b.failures, b.until = map[uuid.UUID]int{}, map[uuid.UUID]time.Time{}
	}
	if ok {
		delete(b.failures, channelID)
		delete(b.until, channelID)
		return
	}
	b.failures[channelID]++
	if b.failures[channelID] >= breakerFailures {
		b.until[channelID] = now.Add(breakerPause)
	}
}

// backoff after attempt n: 10 s doubling, at most 15 minutes.
func backoff(attempt int) time.Duration {
	delay := 10 * time.Second << min(attempt-1, 10)
	return min(delay, 15*time.Minute)
}

func silenced(silences []*Silence, labels Labels) bool {
	for _, silence := range silences {
		if matchesAll(silence.Matchers, labels) {
			return true
		}
	}
	return false
}

func matchesAll(matchers Matchers, labels Labels) bool {
	if len(matchers) == 0 {
		return false
	}
	for _, matcher := range matchers {
		value := labels[matcher.Name]
		if matcher.Regex {
			re, err := matcherRegex(matcher.Value)
			if err != nil || !re.MatchString(value) {
				return false
			}
		} else if value != matcher.Value {
			return false
		}
	}
	return true
}

// matcherRegexes caches compiled matcher regexes: a maintenance silence
// matches hundreds of check ids, and is checked on every notification.
var matcherRegexes = struct {
	sync.Mutex
	byPattern map[string]*regexp.Regexp
}{byPattern: map[string]*regexp.Regexp{}}

func matcherRegex(pattern string) (*regexp.Regexp, error) {
	matcherRegexes.Lock()
	defer matcherRegexes.Unlock()
	if re, ok := matcherRegexes.byPattern[pattern]; ok {
		return re, nil
	}
	re, err := regexp.Compile("^(?:" + pattern + ")$")
	if err != nil {
		return nil, err
	}
	if len(matcherRegexes.byPattern) >= 1000 {
		clear(matcherRegexes.byPattern)
	}
	matcherRegexes.byPattern[pattern] = re
	return re, nil
}
