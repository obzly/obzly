package testing

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"obzly/internal/cache"
	"obzly/internal/util/leaderlock"

	"github.com/stretchr/testify/require"
)

// RootAdminLock serializes tests that create or retire the root admin, a singleton shared
// through the one test database (see RunExclusiveGlobal).
const RootAdminLock = "root-admin"

// heldLocks are the locks each running test holds (AcquireExclusiveGlobalQueue).
var heldLocks sync.Map

type heldKey struct {
	t        *testing.T
	lockName string
}

// Nothing renews the lock while a test holds it, so the TTL must outlast the slowest test;
// maxWait allows for many queued tests across packages.
const (
	lockTTL      = 5 * time.Minute
	maxWait      = 10 * time.Minute
	pollInterval = 200 * time.Millisecond
)

// WithExclusiveGlobalQueue runs fn holding a Valkey lock shared by every test process.
// ListPendingUpload is not project-scoped, so tests in different packages (run concurrently
// against one Postgres) that reset or count a stream's global pending queue race each other.
// Use one lockName per stream queue (e.g. "logs-pending-queue") so all such tests serialize.
func WithExclusiveGlobalQueue(t *testing.T, lockName string, fn func()) {
	t.Helper()

	ctx := context.Background()
	err := leaderlock.RunExclusiveBlocking(ctx, cache.GetCache(), lockName, lockTTL, maxWait, pollInterval,
		func(ctx context.Context) error {
			fn()
			return nil
		},
	)
	require.NoError(t, err, "failed to acquire exclusive access to %q's global pending queue", lockName)
}

// AcquireExclusiveGlobalQueue is WithExclusiveGlobalQueue's acquire form: call it near the
// top of a test; the lock is released by t.Cleanup. A second call in the same test is a no-op.
func AcquireExclusiveGlobalQueue(t *testing.T, lockName string) {
	t.Helper()

	held := heldKey{t: t, lockName: lockName}
	if _, already := heldLocks.LoadOrStore(held, struct{}{}); already {
		return
	}
	t.Cleanup(func() { heldLocks.Delete(held) })

	ctx := context.Background()
	client := cache.GetCache()
	lock := leaderlock.New(client, lockName, lockTTL)
	deadline := time.Now().Add(maxWait)

	for {
		acquired, err := lock.TryAcquire(ctx)
		require.NoError(t, err, "failed to acquire exclusive access to %q's global pending queue", lockName)
		if acquired {
			t.Cleanup(func() { _ = lock.Release(ctx) })
			return
		}
		require.False(
			t,
			time.Now().After(deadline),
			"timed out waiting for exclusive access to %q's global pending queue",
			lockName,
		)
		time.Sleep(pollInterval)
	}
}

// RunExclusiveGlobal is WithExclusiveGlobalQueue for helpers without a
// *testing.T: it runs fn holding lockName across every test process.
func RunExclusiveGlobal(lockName string, fn func() error) error {
	return leaderlock.RunExclusiveBlocking(context.Background(), cache.GetCache(), lockName, lockTTL, maxWait,
		pollInterval, func(context.Context) error { return fn() })
}

// AcquireExclusiveGlobal is AcquireExclusiveGlobalQueue for a TestMain (no *testing.T): it
// blocks until lockName is held and returns the release function.
func AcquireExclusiveGlobal(lockName string) (release func(), err error) {
	ctx := context.Background()
	lock := leaderlock.New(cache.GetCache(), lockName, lockTTL)
	deadline := time.Now().Add(maxWait)

	for {
		acquired, err := lock.TryAcquire(ctx)
		if err != nil {
			return nil, err
		}
		if acquired {
			return func() { _ = lock.Release(ctx) }, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for exclusive access to %q", lockName)
		}
		time.Sleep(pollInterval)
	}
}
