package mfa_services

import (
	"context"
	"fmt"
	"time"

	"obzly/internal/cache"

	"github.com/google/uuid"
	"github.com/valkey-io/valkey-go"
)

// ErrTooManyAttempts replaces the verification error once an account reaches
// maxFailedAttempts within the lockout window.
var ErrTooManyAttempts = fmt.Errorf(
	"too many failed codes - wait %d minutes and try again", int(attemptLockoutWindow.Minutes()),
)

const (
	// maxFailedAttempts is tight on purpose: the goal is to make guessing infeasible.
	maxFailedAttempts = 5
	// attemptLockoutWindow matches challengeTokenTTL; a new sign-in mints a new challenge anyway.
	attemptLockoutWindow = challengeTokenTTL
	attemptKeyPrefix     = "mfa_attempts:user:"
)

// recordFailureAndCheckLockoutScript increments and sets the TTL atomically, so
// concurrent guesses can't leave the counter without an expiry.
const recordFailureAndCheckLockoutScript = `
local key = KEYS[1]
local max_attempts = tonumber(ARGV[1])
local ttl_seconds = tonumber(ARGV[2])

local count = redis.call('INCR', key)
if count == 1 then
    redis.call('EXPIRE', key, ttl_seconds)
end

if count >= max_attempts then
    return 1
end
return 0
`

// isLockedOut doesn't count as an attempt; it runs before any verification so a
// locked account can't be probed further.
func isLockedOut(userID uuid.UUID) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := cache.GetCache()
	result := client.Do(ctx, client.B().Get().Key(attemptKey(userID)).Build())
	if result.Error() != nil {
		if valkey.IsValkeyNil(result.Error()) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check MFA attempt count: %w", result.Error())
	}

	count, err := result.AsInt64()
	if err != nil {
		return false, fmt.Errorf("failed to parse MFA attempt count: %w", err)
	}
	return count >= maxFailedAttempts, nil
}

// recordFailedAttempt increments userID's failure counter and reports
// whether that increment just crossed into lockout.
func recordFailedAttempt(userID uuid.UUID) (lockedOut bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := cache.GetCache()
	result := client.Do(ctx, client.B().Eval().
		Script(recordFailureAndCheckLockoutScript).
		Numkeys(1).
		Key(attemptKey(userID)).
		Arg(fmt.Sprintf("%d", maxFailedAttempts)).
		Arg(fmt.Sprintf("%d", int64(attemptLockoutWindow.Seconds()))).
		Build())
	if result.Error() != nil {
		return false, fmt.Errorf("failed to record MFA attempt: %w", result.Error())
	}

	locked, err := result.AsInt64()
	if err != nil {
		return false, fmt.Errorf("failed to parse MFA attempt result: %w", err)
	}
	return locked == 1, nil
}

// resetAttempts runs after a successful verification.
func resetAttempts(userID uuid.UUID) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := cache.GetCache()
	result := client.Do(ctx, client.B().Del().Key(attemptKey(userID)).Build())
	if result.Error() != nil {
		return fmt.Errorf("failed to reset MFA attempt count: %w", result.Error())
	}
	return nil
}

func attemptKey(userID uuid.UUID) string {
	return attemptKeyPrefix + userID.String()
}
