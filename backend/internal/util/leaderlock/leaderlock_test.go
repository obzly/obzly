package leaderlock_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"obzly/internal/cache"
	"obzly/internal/telemetry"
	"obzly/internal/util/leaderlock"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func uniqueName(t *testing.T) string {
	t.Helper()
	return "test-" + t.Name() + "-" + time.Now().Format("150405.000000000")
}

func Test_TryAcquire_SecondOwnerCannotAcquireWhileHeld(t *testing.T) {
	client := cache.GetCache()
	name := uniqueName(t)

	lockA := leaderlock.New(client, name, 5*time.Second)
	lockB := leaderlock.New(client, name, 5*time.Second)

	acquiredA, err := lockA.TryAcquire(context.Background())
	require.NoError(t, err)
	require.True(t, acquiredA, "first owner should acquire the free lock")

	acquiredB, err := lockB.TryAcquire(context.Background())
	require.NoError(t, err)
	require.False(t, acquiredB, "second owner must not acquire a lock already held by another owner")

	require.NoError(t, lockA.Release(context.Background()))

	acquiredB, err = lockB.TryAcquire(context.Background())
	require.NoError(t, err)
	require.True(t, acquiredB, "lock should be acquirable once the holder releases it")
}

func Test_TryAcquire_SameOwnerRenewsItsOwnLock(t *testing.T) {
	client := cache.GetCache()
	name := uniqueName(t)
	lock := leaderlock.New(client, name, 2*time.Second)

	for i := 0; i < 3; i++ {
		acquired, err := lock.TryAcquire(context.Background())
		require.NoError(t, err)
		require.True(t, acquired, "the same owner must keep renewing its own lock on every call")
	}
}

func Test_TryAcquire_ExpiresAndIsPickedUpByAnotherOwner(t *testing.T) {
	client := cache.GetCache()
	name := uniqueName(t)

	lockA := leaderlock.New(client, name, 200*time.Millisecond)
	lockB := leaderlock.New(client, name, 200*time.Millisecond)

	acquiredA, err := lockA.TryAcquire(context.Background())
	require.NoError(t, err)
	require.True(t, acquiredA)

	// Simulate lockA's process dying without releasing - no more renewals.
	time.Sleep(400 * time.Millisecond)

	acquiredB, err := lockB.TryAcquire(context.Background())
	require.NoError(t, err)
	require.True(t, acquiredB, "an expired lease (holder never renewed) must become acquirable by another owner")
}

func Test_Release_DoesNotDeleteALockTakenOverByAnotherOwner(t *testing.T) {
	client := cache.GetCache()
	name := uniqueName(t)

	lockA := leaderlock.New(client, name, 100*time.Millisecond)
	lockB := leaderlock.New(client, name, 5*time.Second)

	acquiredA, err := lockA.TryAcquire(context.Background())
	require.NoError(t, err)
	require.True(t, acquiredA)

	time.Sleep(200 * time.Millisecond) // let lockA's lease expire without renewing

	acquiredB, err := lockB.TryAcquire(context.Background())
	require.NoError(t, err)
	require.True(t, acquiredB, "lockB should have taken over the expired lease")

	// lockA's stale Release must be a no-op, not delete lockB's live lock.
	require.NoError(t, lockA.Release(context.Background()))

	stillHeldByB, err := lockB.TryAcquire(context.Background())
	require.NoError(t, err)
	require.True(t, stillHeldByB, "lockB's lock must survive lockA's stale release call")
}

func Test_RunExclusive_OnlyOneOfManyConcurrentCallersRuns(t *testing.T) {
	client := cache.GetCache()
	name := uniqueName(t)

	var runCount int64
	const callers = 10

	results := make(chan bool, callers)
	for i := 0; i < callers; i++ {
		go func() {
			ran, err := leaderlock.RunExclusive(context.Background(), client, name, 5*time.Second,
				func(ctx context.Context) error {
					atomic.AddInt64(&runCount, 1)
					time.Sleep(50 * time.Millisecond) // widen the contention window
					return nil
				})
			require.NoError(t, err)
			results <- ran
		}()
	}

	ranCount := 0
	for i := 0; i < callers; i++ {
		if <-results {
			ranCount++
		}
	}

	require.Equal(t, 1, ranCount, "exactly one of the concurrent callers should have won the lock and run fn")
	require.Equal(t, int64(1), atomic.LoadInt64(&runCount))
}

func Test_RunExclusiveBlocking_SecondCallerWaitsThenRunsAfterFirstReleases(t *testing.T) {
	client := cache.GetCache()
	name := uniqueName(t)

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})

	go func() {
		_ = leaderlock.RunExclusiveBlocking(
			context.Background(),
			client,
			name,
			5*time.Second,
			5*time.Second,
			20*time.Millisecond,
			func(ctx context.Context) error {
				close(firstStarted)
				<-releaseFirst
				return nil
			},
		)
	}()

	<-firstStarted

	secondRan := make(chan bool, 1)
	go func() {
		var ran bool
		err := leaderlock.RunExclusiveBlocking(
			context.Background(),
			client,
			name,
			5*time.Second,
			5*time.Second,
			20*time.Millisecond,
			func(ctx context.Context) error {
				ran = true
				return nil
			},
		)
		require.NoError(t, err)
		secondRan <- ran
	}()

	select {
	case <-secondRan:
		t.Fatal("second caller must not run fn while the first still holds the lock")
	case <-time.After(150 * time.Millisecond):
		// expected: second caller is still blocked/polling
	}

	close(releaseFirst)

	select {
	case ran := <-secondRan:
		require.True(t, ran, "second caller should run fn once the first releases the lock")
	case <-time.After(5 * time.Second):
		t.Fatal("second caller never acquired the lock after it was released")
	}
}

func Test_RunExclusiveBlocking_TimesOutIfLockNeverFrees(t *testing.T) {
	client := cache.GetCache()
	name := uniqueName(t)

	holder := leaderlock.New(client, name, 5*time.Second)
	acquired, err := holder.TryAcquire(context.Background())
	require.NoError(t, err)
	require.True(t, acquired)
	// Deliberately never released within the test.

	err = leaderlock.RunExclusiveBlocking(
		context.Background(),
		client,
		name,
		5*time.Second,
		150*time.Millisecond,
		20*time.Millisecond,
		func(ctx context.Context) error {
			t.Fatal("fn must not run when the lock is never released within maxWait")
			return nil
		},
	)
	require.Error(t, err)
}

func Test_RunExclusive_RecordsEachPassByResult(t *testing.T) {
	client := cache.GetCache()
	name := uniqueName(t)
	runs := func(result string) float64 {
		return testutil.ToFloat64(telemetry.LeaderTaskRuns.WithLabelValues(name, result))
	}

	var heldDuringRun float64
	ran, err := leaderlock.RunExclusive(context.Background(), client, name, 5*time.Second,
		func(context.Context) error {
			heldDuringRun = testutil.ToFloat64(telemetry.LeaderLockHeld.WithLabelValues(name))
			return nil
		})
	require.NoError(t, err)
	require.True(t, ran)
	require.Equal(t, 1.0, heldDuringRun, "leader_lock_held is 1 while the pass runs")
	require.Equal(t, 0.0, testutil.ToFloat64(telemetry.LeaderLockHeld.WithLabelValues(name)))
	require.Equal(t, 1.0, runs(telemetry.ResultOK))
	require.Greater(t, testutil.ToFloat64(telemetry.LeaderTaskLastSuccess.WithLabelValues(name)), 0.0)

	_, err = leaderlock.RunExclusive(context.Background(), client, name, 5*time.Second,
		func(context.Context) error { return errors.New("pass failed") })
	require.Error(t, err)
	require.Equal(t, 1.0, runs(telemetry.ResultError))

	other := leaderlock.New(client, name, 5*time.Second)
	acquired, err := other.TryAcquire(context.Background())
	require.NoError(t, err)
	require.True(t, acquired)
	defer func() { _ = other.Release(context.Background()) }()

	ran, err = leaderlock.RunExclusive(context.Background(), client, name, 5*time.Second,
		func(context.Context) error { return nil })
	require.NoError(t, err)
	require.False(t, ran)
	require.Equal(t, 1.0, runs(telemetry.ResultSkipped))
}

func Test_RunExclusive_PassLongerThanTTL_KeepsTheLockAndASecondRunnerSkips(t *testing.T) {
	client := cache.GetCache()
	name := uniqueName(t)
	const ttl = 300 * time.Millisecond

	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, err := leaderlock.RunExclusive(context.Background(), client, name, ttl, func(ctx context.Context) error {
			close(started)
			select {
			case <-time.After(4 * ttl):
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		result <- err
	}()
	<-started

	deadline := time.Now().Add(3 * ttl)
	for time.Now().Before(deadline) {
		ran, err := leaderlock.RunExclusive(context.Background(), client, name, ttl,
			func(context.Context) error { return nil })
		require.NoError(t, err)
		require.False(t, ran, "the lock must stay held past its TTL while the first pass runs")
		time.Sleep(ttl / 4)
	}
	require.NoError(t, <-result, "the first pass is not cancelled while it keeps the lock")
}

func Test_RunExclusive_LockTakenOver_CancelsThePass(t *testing.T) {
	client := cache.GetCache()
	name := uniqueName(t)
	const ttl = 300 * time.Millisecond

	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, err := leaderlock.RunExclusive(context.Background(), client, name, ttl, func(ctx context.Context) error {
			close(started)
			select {
			case <-time.After(10 * time.Second):
				return errors.New("never cancelled")
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		result <- err
	}()
	<-started

	// Another owner takes the key over, as if the lease had expired.
	key := "obzly:leaderlock:" + name
	require.NoError(t, client.Do(context.Background(),
		client.B().Set().Key(key).Value("someone-else").Px(5*time.Second).Build()).Error())
	defer func() { _ = client.Do(context.Background(), client.B().Del().Key(key).Build()).Error() }()

	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("a pass that lost its lock must be cancelled")
	}
}
