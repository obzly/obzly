package querylimit_test

import (
	"context"
	"testing"
	"time"

	"obzly/internal/cache"
	"obzly/internal/util/querylimit"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func Test_Acquire_AllSlotsTaken_WaitsForOneThenRefuses(t *testing.T) {
	limiter := querylimit.New(cache.GetCache(), "test", 2, 500*time.Millisecond)
	userID := uuid.New()
	ctx := context.Background()

	first, err := limiter.Acquire(ctx, userID)
	require.NoError(t, err)
	second, err := limiter.Acquire(ctx, userID)
	require.NoError(t, err)

	started := time.Now().UTC()
	_, err = limiter.Acquire(ctx, userID)
	require.ErrorIs(t, err, querylimit.ErrTooManyQueries)
	require.GreaterOrEqual(t, time.Since(started), 500*time.Millisecond, "waited for a slot first")

	go func() {
		time.Sleep(200 * time.Millisecond)
		first()
	}()
	third, err := limiter.Acquire(ctx, userID)
	require.NoError(t, err, "a slot freed during the wait is taken")

	other, err := limiter.Acquire(ctx, uuid.New())
	require.NoError(t, err, "slots are per user")
	for _, release := range []func(){second, third, other} {
		release()
	}
}
