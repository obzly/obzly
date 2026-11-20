package segments

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A slow store still finishes as long as bytes keep moving.
func Test_WatchTransfer_SteadyProgress_IsNotCancelled(t *testing.T) {
	ctx, progress, stop := watchTransfer(context.Background(), 80*time.Millisecond)
	defer stop()

	for range 20 {
		time.Sleep(15 * time.Millisecond)
		progress()
	}

	require.NoError(t, ctx.Err())
}

func Test_WatchTransfer_NoProgress_CancelsAsStalled(t *testing.T) {
	ctx, _, stop := watchTransfer(context.Background(), 40*time.Millisecond)
	defer stop()

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("a transfer moving no bytes was not cancelled")
	}
	require.True(t, errors.Is(context.Cause(ctx), errTransferStalled))
	require.ErrorIs(t, stalledOr(ctx, context.Canceled), errTransferStalled)
}
