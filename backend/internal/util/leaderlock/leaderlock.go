// Package leaderlock is a Valkey-backed distributed lease that makes background work and
// one-shot startup actions safe on N replicas. Built on Valkey rather than Kubernetes
// leader election so it works the same under Docker/Compose (ARCHITECTURE.md "High
// availability").
package leaderlock

import (
	"context"
	"fmt"
	"time"

	"obzly/internal/telemetry"

	"github.com/google/uuid"
	"github.com/valkey-io/valkey-go"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const keyPrefix = "obzly:leaderlock:"

// lastRunPrefix keys RunAtMostEvery's interval claims, beside the locks.
const lastRunPrefix = keyPrefix + "last-run:"

// intervalSlackDivisor: an interval claim expires interval/20 early, so a
// replica whose ticker fires exactly one interval later (or a few ms early
// against Valkey's clock) still finds it free instead of skipping a whole
// interval.
const intervalSlackDivisor = 20

// acquireOrRenewScript atomically acquires a free lock, or renews it if this owner holds
// it. Returns 1 if the caller holds the lock afterwards, 0 if someone else does.
const acquireOrRenewScript = `
local current = redis.call('GET', KEYS[1])
if current == false then
	redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
	return 1
elseif current == ARGV[1] then
	redis.call('PEXPIRE', KEYS[1], ARGV[2])
	return 1
else
	return 0
end
`

// releaseScript deletes the lock only if this owner still holds it, so a lost lock is
// never deleted out from under its new holder.
const releaseScript = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
	return redis.call('DEL', KEYS[1])
else
	return 0
end
`

// Lock is one named, renewable lease. Not safe for concurrent use; create one per worker.
type Lock struct {
	client valkey.Client
	key    string
	owner  string
	ttl    time.Duration
}

// Key is the Valkey key of the lock named name, for reading whether it is
// held without taking it.
func Key(name string) string {
	return keyPrefix + name
}

// New creates a lock named name (under a fixed key prefix). Callers holding it across a
// ticker loop should renew well before ttl (e.g. ttl >= 3x the tick) so a slow tick or
// a Valkey hiccup doesn't cause a handoff.
func New(client valkey.Client, name string, ttl time.Duration) *Lock {
	return &Lock{
		client: client,
		key:    keyPrefix + name,
		owner:  uuid.NewString(),
		ttl:    ttl,
	}
}

// TryAcquire is non-blocking: it acquires the lock if free, renews it if already held,
// or returns false if another owner holds it.
func (l *Lock) TryAcquire(ctx context.Context) (bool, error) {
	result := l.client.Do(ctx, l.client.B().Eval().
		Script(acquireOrRenewScript).
		Numkeys(1).
		Key(l.key).
		Arg(l.owner).
		Arg(fmt.Sprintf("%d", l.ttl.Milliseconds())).
		Build())

	if result.Error() != nil {
		return false, fmt.Errorf("leaderlock: acquire/renew %q failed: %w", l.key, result.Error())
	}

	acquired, err := result.AsInt64()
	if err != nil {
		return false, fmt.Errorf("leaderlock: unexpected acquire/renew result for %q: %w", l.key, err)
	}

	return acquired == 1, nil
}

// Release gives up the lock now so another replica can take over without waiting out the
// TTL. Safe to call after the lock was lost (no-op).
func (l *Lock) Release(ctx context.Context) error {
	result := l.client.Do(ctx, l.client.B().Eval().
		Script(releaseScript).
		Numkeys(1).
		Key(l.key).
		Arg(l.owner).
		Build())

	if result.Error() != nil {
		return fmt.Errorf("leaderlock: release %q failed: %w", l.key, result.Error())
	}
	return nil
}

// RunExclusive tries once to acquire name's lock and, if acquired, runs fn and releases
// it (ran is true). If another replica holds it, fn is skipped: for optional one-shot
// work. The lock is renewed every ttl/3 while fn runs; fn's context is cancelled if the
// lock is lost or can't be renewed in time.
func RunExclusive(
	ctx context.Context,
	client valkey.Client,
	name string,
	ttl time.Duration,
	fn func(ctx context.Context) error,
) (ran bool, err error) {
	return runExclusive(ctx, client, name, ttl, nil, fn)
}

// RunAtMostEvery runs fn at most once per interval across all replicas: the
// first replica to get name's lock after the previous run's interval claim
// expired claims the next interval and runs fn (ran is true); everyone else
// skips. For interval tasks whose duplicate passes cost something:
// RunExclusive only keeps passes from overlapping, so N replicas ticking
// every interval run it N times per interval. The lock (same name as
// RunExclusive's, so older replicas mid-rollout still don't overlap) is held
// and renewed for fn's duration as with RunExclusive. A failed pass keeps its
// claim: the next try is one interval later, as on a single replica.
func RunAtMostEvery(
	ctx context.Context,
	client valkey.Client,
	name string,
	interval time.Duration,
	ttl time.Duration,
	fn func(ctx context.Context) error,
) (ran bool, err error) {
	claim := func(ctx context.Context, owner string) (bool, error) {
		return claimInterval(ctx, client, name, owner, interval)
	}
	return runExclusive(ctx, client, name, ttl, claim, fn)
}

// RunExclusiveBlocking polls for name's lock until it is acquired or maxWait elapses,
// then runs fn and releases. For startup actions every replica must wait for
// (migrations): the loser waits instead of skipping. fn must be idempotent; this only
// prevents concurrent runs.
func RunExclusiveBlocking(
	ctx context.Context,
	client valkey.Client,
	name string,
	ttl time.Duration,
	maxWait time.Duration,
	pollInterval time.Duration,
	fn func(ctx context.Context) error,
) error {
	lock := New(client, name, ttl)
	deadline := time.Now().Add(maxWait)

	for {
		acquired, err := lock.TryAcquire(ctx)
		if err != nil {
			return err
		}
		if acquired {
			defer func() { _ = lock.Release(ctx) }()
			return runObserved(ctx, name, fn)
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("leaderlock: timed out after %s waiting for lock %q", maxWait, name)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// keepRenewed renews the lock every ttl/3 until the returned stop is
// called, and calls lost when the lock is taken by another owner or a
// renewal fails so late that the lease would expire before the next one.
func (l *Lock) keepRenewed(ctx context.Context, lost context.CancelFunc) (stop func()) {
	interval := max(l.ttl/3, time.Millisecond)
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		lastRenewed := time.Now()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			held, err := l.TryAcquire(ctx)
			switch {
			case err == nil && held:
				lastRenewed = time.Now()
			case err == nil, time.Since(lastRenewed)+interval >= l.ttl:
				lost()
				return
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}

// runObserved runs fn while this replica holds name's lock and records the
// pass. Callers hold the lock for exactly fn's duration, so leader_lock_held
// is 1 only while a pass runs here.
func runObserved(ctx context.Context, name string, fn func(ctx context.Context) error) error {
	held := telemetry.LeaderLockHeld.WithLabelValues(name)
	held.Set(1)
	defer held.Set(0)

	ctx, span := telemetry.StartRootSpan(
		ctx,
		"task "+name,
		trace.SpanKindInternal,
		attribute.String("obzly.task", name),
	)
	defer span.End()

	start := time.Now()
	err := fn(ctx)
	telemetry.LeaderTaskDuration.WithLabelValues(name).Observe(time.Since(start).Seconds())
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		telemetry.LeaderTaskRuns.WithLabelValues(name, telemetry.ResultError).Inc()
		return err
	}
	telemetry.LeaderTaskRuns.WithLabelValues(name, telemetry.ResultOK).Inc()
	telemetry.LeaderTaskLastSuccess.WithLabelValues(name).SetToCurrentTime()
	return nil
}

// runExclusive acquires name's lock and, if claim (optional) agrees too, runs
// fn while renewing the lock. Every attempt counts in leader_task_runs_total:
// ok/error for a pass that ran, skipped for one another replica holds or
// already ran this interval.
func runExclusive(
	ctx context.Context,
	client valkey.Client,
	name string,
	ttl time.Duration,
	claim func(ctx context.Context, owner string) (bool, error),
	fn func(ctx context.Context) error,
) (bool, error) {
	lock := New(client, name, ttl)

	acquired, err := lock.TryAcquire(ctx)
	if err != nil {
		telemetry.LeaderTaskRuns.WithLabelValues(name, telemetry.ResultError).Inc()
		return false, err
	}
	if !acquired {
		telemetry.LeaderTaskRuns.WithLabelValues(name, telemetry.ResultSkipped).Inc()
		return false, nil
	}
	defer func() { _ = lock.Release(ctx) }()

	if claim != nil {
		claimed, err := claim(ctx, lock.owner)
		if err != nil {
			telemetry.LeaderTaskRuns.WithLabelValues(name, telemetry.ResultError).Inc()
			return false, err
		}
		if !claimed {
			telemetry.LeaderTaskRuns.WithLabelValues(name, telemetry.ResultSkipped).Inc()
			return false, nil
		}
	}

	fnCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopRenewing := lock.keepRenewed(fnCtx, cancel)
	defer stopRenewing()

	return true, runObserved(fnCtx, name, fn)
}

// claimInterval takes name's claim on the current interval: true when no
// run claimed one within the last interval (less the slack). Called with
// name's lock held, so a claim is always followed by a run.
func claimInterval(
	ctx context.Context, client valkey.Client, name, owner string, interval time.Duration,
) (bool, error) {
	expiry := max(interval-interval/intervalSlackDivisor, time.Millisecond)
	err := client.Do(ctx, client.B().Set().
		Key(lastRunPrefix+name).
		Value(owner).
		Nx().
		PxMilliseconds(expiry.Milliseconds()).
		Build()).Error()
	if valkey.IsValkeyNil(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("leaderlock: claim interval of %q failed: %w", name, err)
	}
	return true, nil
}
