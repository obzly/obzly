package rate_limit

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"sync"
	"time"

	"obzly/internal/cache"
	"obzly/internal/telemetry"

	"github.com/google/uuid"
	"github.com/valkey-io/valkey-go"
)

type RateLimiter struct {
	client valkey.Client
	// local decides when Valkey is unreachable, so a Valkey outage never
	// refuses traffic that is within its limit.
	local   localBuckets
	breaker *cache.Breaker
}

type RateLimitResult struct {
	Allowed       bool      `json:"allowed"`
	Remaining     int       `json:"remaining"`
	ResetTime     time.Time `json:"resetTime"`
	RetryAfterSec int       `json:"retryAfterSec,omitempty"`
}

// BatchResult is TakeBatch's decision for a whole batch.
type BatchResult struct {
	Allowed bool
	// RetryAfter: when the bucket will hold enough for the batch (refused only).
	RetryAfter time.Duration
	// Local: Valkey was unreachable and this replica's own bucket decided.
	Local bool
}

// localBuckets is the same token bucket per key in this process's memory.
type localBuckets struct {
	mu      sync.Mutex
	buckets map[uuid.UUID]*localBucket
}

type localBucket struct {
	tokens float64
	last   time.Time
	// full: when the bucket is full again; until then it can't be dropped
	// without forgiving what it owes.
	full time.Time
}

const (
	defaultTimeout = 5 * time.Second
	// batchTimeout bounds TakeBatch's Valkey call: it sits on the ingest path,
	// and the local bucket decides past it.
	batchTimeout = 250 * time.Millisecond
	keyPrefix    = "rate_limit:project:"
	// minTTL: how long an idle bucket's key is kept, at least; the script
	// keeps a bucket that isn't full for as long as its refill takes.
	minTTL = 300 * time.Second
	// maxLocalBuckets bounds memory during a long outage: at the cap the full
	// buckets go, and all of them only if that frees nothing.
	maxLocalBuckets = 100_000
)

// tokenBucketLuaScript atomically refills the bucket by elapsed time, admits a
// request when the bucket holds `need` tokens, takes `cost` tokens (the bucket
// may go into debt when cost > need), and stores the new state. Tokens are
// fractional: flooring the refill on every call lost the fractions, so a
// limit called more often than once per token refilled too slowly.
//
// The key lives until the bucket is full again (a missing key is a full
// bucket), at least min_ttl, worked out here from the stored tokens on every
// call. The caller used to pass a TTL sized for its own request: a 1-row
// request, even a refused one, cut a deep debt's TTL to 5 minutes, so a
// project limited to 1 log/s could send 5,000 logs, one more, wait 5 minutes
// and find a full bucket.
const tokenBucketLuaScript = `
local key = KEYS[1]
local now = tonumber(ARGV[1])
local rate = tonumber(ARGV[2])
local burst = tonumber(ARGV[3])
local min_ttl = tonumber(ARGV[4])
local cost = tonumber(ARGV[5])
local need = tonumber(ARGV[6])

local current = redis.call('HMGET', key, 'tokens', 'last_refill')
local tokens = tonumber(current[1]) or burst
local last = tonumber(current[2]) or now

-- Replicas' clocks differ a little; time never runs backwards for a bucket.
local elapsed = math.max(0, now - last)
tokens = math.min(burst, tokens + elapsed * rate / 1000)

local allowed = 0
if tokens >= need then
    allowed = 1
    tokens = tokens - cost
end

redis.call('HSET', key, 'tokens', string.format('%.6f', tokens), 'last_refill', string.format('%.0f', math.max(now, last)))
local ttl = min_ttl
if tokens < burst then
    -- The margin covers replicas' clocks and rounding.
    ttl = math.max(min_ttl, math.ceil((burst - tokens) / rate) + 60)
end
redis.call('EXPIRE', key, string.format('%.0f', ttl))

local wait = 0
if allowed == 0 then
    wait = math.ceil((need - tokens) * 1000 / rate)
elseif tokens < burst then
    wait = math.ceil((burst - tokens) * 1000 / rate)
end

return {allowed, math.floor(math.max(tokens, 0)), wait}
`

func NewRateLimiter() *RateLimiter {
	return &RateLimiter{
		client:  cache.GetCache(),
		breaker: cache.GetBreaker(),
	}
}

// CheckRateLimit takes one token for id, refusing when the bucket is empty.
func (r *RateLimiter) CheckRateLimit(projectID uuid.UUID, rpsLimit, burstLimit int) (*RateLimitResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	if rpsLimit <= 0 {
		rpsLimit = 100 // Default fallback
	}
	if burstLimit <= 0 {
		burstLimit = max(rpsLimit*5, 500) // Default burst is 5x RPS or 500, whichever is higher
	}

	allowed, remaining, waitMs, err := r.take(ctx, projectID, rpsLimit, burstLimit, 1, 1)
	if err != nil {
		return nil, err
	}

	var retryAfterSec int
	if !allowed {
		retryAfterSec = max(1, int(math.Ceil(float64(waitMs)/1000.0)))
	}

	return &RateLimitResult{
		Allowed:       allowed,
		Remaining:     remaining,
		ResetTime:     time.Now().Add(time.Duration(waitMs) * time.Millisecond),
		RetryAfterSec: retryAfterSec,
	}, nil
}

// TakeBatch decides for a whole batch of n rows at once, so a client gets one
// answer it can retry (429) instead of rows dropped inside a success. The
// batch is admitted when the bucket holds min(n, burst) tokens and then takes
// all n, going into debt for a batch larger than the burst: any batch size
// gets through and the average stays at rps. When Valkey is unreachable the
// replica's own bucket decides, so an outage never refuses traffic within its
// limit.
func (r *RateLimiter) TakeBatch(projectID uuid.UUID, n, rps, burst int) BatchResult {
	if n <= 0 || rps <= 0 {
		return BatchResult{Allowed: true}
	}
	burst = max(burst, rps)
	need := min(n, burst)

	allowed, waitMs, err := false, int64(0), error(cache.ErrUnavailable)
	if r.breaker.Available() {
		ctx, cancel := context.WithTimeout(context.Background(), batchTimeout)
		allowed, _, waitMs, err = r.take(ctx, projectID, rps, burst, n, need)
		cancel()
		_ = r.breaker.Observe(err)
	}
	if err != nil {
		telemetry.RateLimitLocalDecisions.Inc()
		allowed, wait := r.local.take(projectID, float64(rps), float64(burst), float64(n), float64(need), time.Now())
		return BatchResult{Allowed: allowed, RetryAfter: wait, Local: true}
	}
	if allowed {
		return BatchResult{Allowed: true}
	}
	return BatchResult{RetryAfter: time.Duration(waitMs) * time.Millisecond}
}

func (r *RateLimiter) ResetRateLimit(projectID uuid.UUID) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	r.local.reset(projectID)
	key := keyPrefix + projectID.String()

	result := r.client.Do(ctx, r.client.B().Del().Key(key).Build())
	return result.Error()
}

func (r *RateLimiter) take(
	ctx context.Context,
	projectID uuid.UUID,
	rps, burst, cost, need int,
) (allowed bool, remaining int, waitMs int64, err error) {
	key := keyPrefix + projectID.String()
	result := r.client.Do(ctx, r.client.B().Eval().
		Script(tokenBucketLuaScript).
		Numkeys(1).
		Key(key).
		Arg(strconv.FormatInt(time.Now().UnixMilli(), 10)).
		Arg(strconv.Itoa(rps)).
		Arg(strconv.Itoa(burst)).
		Arg(strconv.FormatInt(int64(minTTL/time.Second), 10)).
		Arg(strconv.Itoa(cost)).
		Arg(strconv.Itoa(need)).
		Build())

	if result.Error() != nil {
		return false, 0, 0, fmt.Errorf("rate limit check failed: %w", result.Error())
	}

	values, err := result.AsIntSlice()
	if err != nil {
		return false, 0, 0, fmt.Errorf("failed to parse rate limit result: %w", err)
	}
	if len(values) < 3 {
		return false, 0, 0, fmt.Errorf("invalid rate limit result: expected 3 values, got %d", len(values))
	}

	return values[0] == 1, int(values[1]), values[2], nil
}

func (l *localBuckets) take(key uuid.UUID, rate, burst, cost, need float64, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buckets == nil {
		l.buckets = map[uuid.UUID]*localBucket{}
	}
	bucket, ok := l.buckets[key]
	if !ok {
		l.makeRoom(now)
		bucket = &localBucket{tokens: burst, last: now}
		l.buckets[key] = bucket
	}
	if elapsed := now.Sub(bucket.last); elapsed > 0 {
		bucket.tokens = math.Min(burst, bucket.tokens+elapsed.Seconds()*rate)
		bucket.last = now
	}
	allowed := bucket.tokens >= need
	if allowed {
		bucket.tokens -= cost
	}
	bucket.full = now.Add(time.Duration((burst - bucket.tokens) / rate * float64(time.Second)))
	if allowed {
		return true, 0
	}
	return false, time.Duration(math.Ceil((need-bucket.tokens)/rate*1000)) * time.Millisecond
}

// makeRoom keeps the map under maxLocalBuckets: buckets that are full again
// say nothing a new one wouldn't, so they go first and the ones in debt stay.
// Starting over each time, as it did, forgave every debt. Call with l.mu held.
func (l *localBuckets) makeRoom(now time.Time) {
	if len(l.buckets) < maxLocalBuckets {
		return
	}
	for key, bucket := range l.buckets {
		if !bucket.full.After(now) {
			delete(l.buckets, key)
		}
	}
	if len(l.buckets) >= maxLocalBuckets {
		l.buckets = map[uuid.UUID]*localBucket{}
	}
}

func (l *localBuckets) reset(key uuid.UUID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, key)
}
