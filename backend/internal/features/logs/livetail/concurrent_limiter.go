package logs_livetail

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/valkey-io/valkey-go"
)

// ConcurrentTailLimiter caps one user's open Live Tail connections (Valkey
// INCR/DECR with TTL, like ConcurrentQueryLimiter): each tail holds a
// subscription and matches every published log in process.
type ConcurrentTailLimiter struct {
	client valkey.Client
	logger *slog.Logger
}

func NewConcurrentTailLimiter(client valkey.Client, logger *slog.Logger) *ConcurrentTailLimiter {
	return &ConcurrentTailLimiter{client: client, logger: logger}
}

const (
	// maxConcurrentTails is above the query limit (3): a tail is cheap (an idle
	// subscription) but held open indefinitely.
	maxConcurrentTails = 5
	tailKeyPrefix      = "concurrent_live_tails:user:"
	// tailSlotTTL is refreshed every heartbeat: a tail has no natural end, and a
	// crashed tab should free its slot within about one missed heartbeat.
	tailSlotTTL = 2 * heartbeatInterval
)

// ErrTooManyConcurrentTails is returned once a user is at maxConcurrentTails
// (mapped to 429).
var ErrTooManyConcurrentTails = errors.New("too many concurrent live tail connections")

func (l *ConcurrentTailLimiter) key(userID uuid.UUID) string {
	return tailKeyPrefix + userID.String()
}

// AcquireTailSlot increments the user's open-tail counter, undoing the
// increment on ErrTooManyConcurrentTails so a rejection never inflates it.
func (l *ConcurrentTailLimiter) AcquireTailSlot(userID uuid.UUID) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	key := l.key(userID)

	result := l.client.Do(ctx, l.client.B().Incr().Key(key).Build())
	if result.Error() != nil {
		return fmt.Errorf("failed to increment live tail counter: %w", result.Error())
	}

	currentCount, err := result.AsInt64()
	if err != nil {
		return fmt.Errorf("failed to get current live tail count: %w", err)
	}

	if currentCount > int64(maxConcurrentTails) {
		l.client.Do(ctx, l.client.B().Decr().Key(key).Build())
		return ErrTooManyConcurrentTails
	}

	l.client.Do(ctx, l.client.B().Expire().Key(key).Seconds(int64(tailSlotTTL.Seconds())).Build())

	return nil
}

// RefreshTailSlot extends the slot's TTL on every heartbeat, so a live tail
// keeps its slot and a dead one ages out.
func (l *ConcurrentTailLimiter) RefreshTailSlot(userID uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	key := l.key(userID)
	result := l.client.Do(ctx, l.client.B().Expire().Key(key).Seconds(int64(tailSlotTTL.Seconds())).Build())
	if result.Error() != nil {
		l.logger.Error("Failed to refresh live tail slot",
			slog.String("userId", userID.String()),
			slog.String("error", result.Error().Error()))
	}
}

// ReleaseTailSlot decrements the counter when a tail connection ends.
func (l *ConcurrentTailLimiter) ReleaseTailSlot(userID uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	key := l.key(userID)
	result := l.client.Do(ctx, l.client.B().Decr().Key(key).Build())
	if result.Error() != nil {
		l.logger.Error("Failed to release live tail slot",
			slog.String("userId", userID.String()),
			slog.String("error", result.Error().Error()))
	}
}

// CleanupAllTailSlots clears tail slots at startup. Hygiene only: stale slots
// also expire via tailSlotTTL.
func (l *ConcurrentTailLimiter) CleanupAllTailSlots() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pattern := tailKeyPrefix + "*"
	keysResult := l.client.Do(ctx, l.client.B().Keys().Pattern(pattern).Build())
	if keysResult.Error() != nil {
		l.logger.Error("Failed to find live tail tracking keys",
			slog.String("error", keysResult.Error().Error()))
		return fmt.Errorf("failed to find live tail tracking keys: %w", keysResult.Error())
	}

	keys, err := keysResult.AsStrSlice()
	if err != nil {
		return fmt.Errorf("failed to parse keys result: %w", err)
	}

	if len(keys) == 0 {
		return nil
	}

	delResult := l.client.Do(ctx, l.client.B().Del().Key(keys...).Build())
	if delResult.Error() != nil {
		l.logger.Error("Failed to delete stale live tail tracking keys",
			slog.String("error", delResult.Error().Error()))
		return fmt.Errorf("failed to delete stale keys: %w", delResult.Error())
	}

	deletedCount, _ := delResult.AsInt64()
	if deletedCount > 0 {
		l.logger.Info("Cleaned up stale live tail tracking keys",
			slog.Int("count", int(deletedCount)))
	}

	return nil
}
