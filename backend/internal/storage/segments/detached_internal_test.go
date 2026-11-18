package segments

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A fetch whose query is cancelled keeps going into the cache (so a retry
// is warm) while the query returns at once.
func Test_ResolveDetached_WhenQueryIsCancelled_FetchFinishes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	returned := make(chan error, 1)
	go func() {
		_, _, err := resolveDetached(ctx, func(fetchCtx context.Context) (string, string, error) {
			close(started)
			<-release
			finished <- fetchCtx.Err()
			return "cached.parquet", "object_store", nil
		})
		returned <- err
	}()
	<-started
	cancel()
	select {
	case err := <-returned:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled query waited for its fetch")
	}
	close(release)
	require.NoError(t, <-finished, "the fetch ran on after its query")
}

// Past maxDetachedFetches a cancelled query's fetch stops, and nothing new
// starts for a query already over.
func Test_ResolveDetached_WhenDetachedSlotsAreFull_FetchStops(t *testing.T) {
	for range maxDetachedFetches {
		detachedFetches <- struct{}{}
	}
	t.Cleanup(func() {
		for range maxDetachedFetches {
			<-detachedFetches
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	started, stopped := make(chan struct{}), make(chan error, 1)
	go func() {
		_, _, _ = resolveDetached(ctx, func(fetchCtx context.Context) (string, string, error) {
			close(started)
			<-fetchCtx.Done()
			stopped <- fetchCtx.Err()
			return "", "", fetchCtx.Err()
		})
	}()
	<-started
	cancel()
	select {
	case err := <-stopped:
		require.True(t, errors.Is(err, context.Canceled))
	case <-time.After(5 * time.Second):
		t.Fatal("the fetch kept running with no detached slot")
	}

	called := false
	_, _, err := resolveDetached(ctx, func(context.Context) (string, string, error) {
		called = true
		return "", "", nil
	})
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, called)
}
