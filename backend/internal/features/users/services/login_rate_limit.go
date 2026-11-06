package users_services

import (
	"context"
	"errors"
	"fmt"
	"time"

	"obzly/internal/cache"

	"github.com/valkey-io/valkey-go"
)

// ErrLoginRateLimited is returned when the account or the caller's IP has too
// many recent failed sign-ins.
var ErrLoginRateLimited = errors.New("too many failed sign-in attempts - please wait a while and try again")

const (
	// maxFailedLoginAttempts is per key (one email, one IP): loose for typos, tight
	// for credential stuffing.
	maxFailedLoginAttempts = 10
	loginLockoutWindow     = 15 * time.Minute

	loginAttemptKeyPrefix = "login_attempts:"
)

// Atomic INCR plus conditional EXPIRE: separate calls could race into never setting a TTL.
const recordLoginFailureScript = `
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

// loginRateLimited reports whether key (an email or an IP) has already hit
// the failure threshold in the current window, without itself counting as
// an attempt.
func loginRateLimited(key string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := cache.GetCache()
	result := client.Do(ctx, client.B().Get().Key(loginAttemptKeyPrefix+key).Build())
	if result.Error() != nil {
		if valkey.IsValkeyNil(result.Error()) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check login attempt count: %w", result.Error())
	}

	count, err := result.AsInt64()
	if err != nil {
		return false, fmt.Errorf("failed to parse login attempt count: %w", err)
	}
	return count >= maxFailedLoginAttempts, nil
}

// recordLoginFailure increments key's failure counter and reports whether
// that increment just crossed into lockout.
func recordLoginFailure(key string) (lockedOut bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := cache.GetCache()
	result := client.Do(ctx, client.B().Eval().
		Script(recordLoginFailureScript).
		Numkeys(1).
		Key(loginAttemptKeyPrefix+key).
		Arg(fmt.Sprintf("%d", maxFailedLoginAttempts)).
		Arg(fmt.Sprintf("%d", int64(loginLockoutWindow.Seconds()))).
		Build())
	if result.Error() != nil {
		return false, fmt.Errorf("failed to record login failure: %w", result.Error())
	}

	locked, err := result.AsInt64()
	if err != nil {
		return false, fmt.Errorf("failed to parse login failure result: %w", err)
	}
	return locked == 1, nil
}

const (
	// Generous enough for an office behind one NAT, tight enough that one
	// source can't burn CPU on bcrypt or fan out IdP token calls.
	maxSignUpsPerIP       = 20
	signUpWindow          = 15 * time.Minute
	maxSSOCallbacksPerIP  = 120
	ssoCallbackWindow     = time.Minute
	requestCountKeyPrefix = "request_count:"
)

// countRequest counts one request against key and reports whether it is
// over limit within window. Same atomic script as recordLoginFailure.
func countRequest(key string, limit int, window time.Duration) (limited bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := cache.GetCache()
	result := client.Do(ctx, client.B().Eval().
		Script(recordLoginFailureScript).
		Numkeys(1).
		Key(requestCountKeyPrefix+key).
		Arg(fmt.Sprintf("%d", limit+1)).
		Arg(fmt.Sprintf("%d", int64(window.Seconds()))).
		Build())
	if result.Error() != nil {
		return false, fmt.Errorf("failed to count request: %w", result.Error())
	}

	over, err := result.AsInt64()
	if err != nil {
		return false, fmt.Errorf("failed to parse request count: %w", err)
	}
	return over == 1, nil
}

func resetLoginFailures(key string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := cache.GetCache()
	result := client.Do(ctx, client.B().Del().Key(loginAttemptKeyPrefix+key).Build())
	if result.Error() != nil {
		return fmt.Errorf("failed to reset login failure count: %w", result.Error())
	}
	return nil
}
