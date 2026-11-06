// Package querylimit caps how many queries of one kind a user runs at once across
// replicas (a Valkey counter per user and kind). Unlike log search's limiter, Acquire
// waits for a slot: a dashboard opens many panels at once, and they should queue.
package querylimit

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/valkey-io/valkey-go"
)

// ErrTooManyQueries: no slot freed up within the wait. Controllers answer 429.
var ErrTooManyQueries = errors.New("too many concurrent queries: wait for the running ones to finish")

const (
	// keyPrefix is logs_querying's, so its startup cleanup clears these
	// counters too.
	keyPrefix = "concurrent_queries:user:"
	// slotTTL clears a counter a crashed replica left raised.
	slotTTL       = 30 * time.Minute
	retryInterval = 200 * time.Millisecond
)

type Limiter struct {
	client valkey.Client
	kind   string
	max    int64
	wait   time.Duration
}

// New limits each user to maxConcurrent queries of kind, waiting up to
// wait for a slot.
func New(client valkey.Client, kind string, maxConcurrent int, wait time.Duration) *Limiter {
	return &Limiter{client, kind, int64(maxConcurrent), wait}
}

// Acquire takes a slot for userID, waiting up to the limiter's wait (or
// until ctx ends); release frees it.
func (l *Limiter) Acquire(ctx context.Context, userID uuid.UUID) (func(), error) {
	key := keyPrefix + l.kind + ":" + userID.String()
	deadline := time.Now().UTC().Add(l.wait)
	for {
		acquired, err := l.tryAcquire(ctx, key)
		if err != nil {
			return nil, err
		}
		if acquired {
			return func() { l.release(key) }, nil
		}
		if time.Now().UTC().After(deadline) {
			return nil, ErrTooManyQueries
		}
		select {
		case <-ctx.Done():
			return nil, ErrTooManyQueries
		case <-time.After(retryInterval):
		}
	}
}

func (l *Limiter) tryAcquire(ctx context.Context, key string) (bool, error) {
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	count, err := l.client.Do(callCtx, l.client.B().Incr().Key(key).Build()).AsInt64()
	if err != nil {
		return false, fmt.Errorf("failed to take a query slot: %w", err)
	}
	if count > l.max {
		l.client.Do(callCtx, l.client.B().Decr().Key(key).Build())
		return false, nil
	}
	l.client.Do(callCtx, l.client.B().Expire().Key(key).Seconds(int64(slotTTL.Seconds())).Build())
	return true, nil
}

func (l *Limiter) release(key string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	l.client.Do(ctx, l.client.B().Decr().Key(key).Build())
}
