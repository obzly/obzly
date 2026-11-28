package cache

import (
	"context"
	"errors"
	"math"
	"sync/atomic"
	"time"

	"obzly/internal/telemetry"

	"github.com/valkey-io/valkey-go"
)

// Breaker skips Valkey for a moment after a call fails to reach it. Without it
// every cache call on the ingest path waited out its timeout while Valkey was
// unreachable, several per request.
type Breaker struct {
	openUntil atomic.Int64 // unix nanos
}

// unreachableBackoff: how long calls skip Valkey after one failed to reach it.
const unreachableBackoff = 2 * time.Second

// ErrUnavailable: the call was skipped because Valkey failed to answer a
// moment ago.
var ErrUnavailable = errors.New("valkey is unreachable")

var defaultBreaker Breaker

// GetBreaker is the breaker for GetCache's client.
func GetBreaker() *Breaker {
	return &defaultBreaker
}

// Available is false for unreachableBackoff after a call failed to reach Valkey.
func (b *Breaker) Available() bool {
	return time.Now().UnixNano() >= b.openUntil.Load()
}

// Observe records a call's outcome and returns err. An error that never got a
// reply (timeout, refused or broken connection) opens the breaker; a reply
// error (a nil reply included) and a caller's cancellation don't.
func (b *Breaker) Observe(err error) error {
	if err == nil || errors.Is(err, ErrUnavailable) || errors.Is(err, context.Canceled) {
		return err
	}
	if _, isReply := valkey.IsValkeyErr(err); isReply {
		return err
	}
	now := time.Now()
	if now.UnixNano() >= b.openUntil.Load() {
		telemetry.ValkeyUnreachable.Inc()
	}
	b.openUntil.Store(now.Add(unreachableBackoff).UnixNano())
	return err
}

// HoldOpen keeps the breaker open until the returned func is called: a Valkey
// outage, for tests.
func (b *Breaker) HoldOpen() (restore func()) {
	b.openUntil.Store(math.MaxInt64)
	return func() { b.openUntil.Store(0) }
}
