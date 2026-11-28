package rate_limit

import (
	"context"
	"crypto/tls"
	"testing"
	"time"

	"obzly/internal/cache"
	"obzly/internal/config"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valkey-io/valkey-go"
)

func Test_CheckRateLimit_WithinLimits_AllowsRequest(t *testing.T) {
	rateLimiter := NewRateLimiter()
	projectID := uuid.New()
	rpsLimit := 10
	burstLimit := 20

	// Clean up any existing data
	rateLimiter.ResetRateLimit(projectID)

	result, err := rateLimiter.CheckRateLimit(projectID, rpsLimit, burstLimit)

	assert.NoError(t, err)
	assert.True(t, result.Allowed)
	assert.Equal(t, burstLimit-1, result.Remaining) // Should have burst - 1 tokens remaining
	assert.Equal(t, 0, result.RetryAfterSec)
	assert.True(t, result.ResetTime.After(time.Now().Add(-time.Second)))
}

func Test_CheckRateLimit_ExceedsBurstLimit_DeniesRequest(t *testing.T) {
	rateLimiter := NewRateLimiter()
	projectID := uuid.New()
	rpsLimit := 1   // Very low RPS to make it easy to exceed
	burstLimit := 2 // Small burst limit

	// Clean up any existing data
	rateLimiter.ResetRateLimit(projectID)

	// Consume the burst tokens
	for i := 0; i < burstLimit; i++ {
		result, err := rateLimiter.CheckRateLimit(projectID, rpsLimit, burstLimit)
		assert.NoError(t, err)
		assert.True(t, result.Allowed, "Request %d should be allowed", i+1)
	}

	// The next request should be denied
	result, err := rateLimiter.CheckRateLimit(projectID, rpsLimit, burstLimit)
	assert.NoError(t, err)
	assert.False(t, result.Allowed)
	assert.Equal(t, 0, result.Remaining)
	assert.True(t, result.RetryAfterSec > 0)
	assert.True(t, result.ResetTime.After(time.Now()))
}

func Test_CheckRateLimit_TokensRefillOverTime_AllowsRequestsAfterWait(t *testing.T) {
	rateLimiter := NewRateLimiter()
	projectID := uuid.New()
	rpsLimit := 10  // 10 RPS means 1 token every 100ms
	burstLimit := 1 // Only 1 token in the bucket

	// Clean up any existing data
	rateLimiter.ResetRateLimit(projectID)

	// Use the only token
	result, err := rateLimiter.CheckRateLimit(projectID, rpsLimit, burstLimit)
	assert.NoError(t, err)
	assert.True(t, result.Allowed)
	assert.Equal(t, 0, result.Remaining)

	// Immediately try again - should be denied
	result, err = rateLimiter.CheckRateLimit(projectID, rpsLimit, burstLimit)
	assert.NoError(t, err)
	assert.False(t, result.Allowed)

	// Wait for tokens to refill (100ms for 1 token at 10 RPS, plus some buffer)
	time.Sleep(150 * time.Millisecond)

	// Now it should be allowed again
	result, err = rateLimiter.CheckRateLimit(projectID, rpsLimit, burstLimit)
	assert.NoError(t, err)
	assert.True(t, result.Allowed)
	assert.Equal(t, 0, result.Remaining)
}

func Test_CheckRateLimit_DifferentProjects_IsolatedLimits(t *testing.T) {
	rateLimiter := NewRateLimiter()
	projectID1 := uuid.New()
	projectID2 := uuid.New()
	rpsLimit := 1
	burstLimit := 1

	// Clean up any existing data
	rateLimiter.ResetRateLimit(projectID1)
	rateLimiter.ResetRateLimit(projectID2)

	// Use up project1's token
	result1, err := rateLimiter.CheckRateLimit(projectID1, rpsLimit, burstLimit)
	assert.NoError(t, err)
	assert.True(t, result1.Allowed)

	// Project1 should now be denied
	result1, err = rateLimiter.CheckRateLimit(projectID1, rpsLimit, burstLimit)
	assert.NoError(t, err)
	assert.False(t, result1.Allowed)

	// But project2 should still be allowed (isolated buckets)
	result2, err := rateLimiter.CheckRateLimit(projectID2, rpsLimit, burstLimit)
	assert.NoError(t, err)
	assert.True(t, result2.Allowed)
}

func Test_CheckRateLimit_WithDefaultValues_HandlesInvalidParameters(t *testing.T) {
	rateLimiter := NewRateLimiter()
	projectID := uuid.New()

	// Clean up any existing data
	rateLimiter.ResetRateLimit(projectID)

	// Test with invalid RPS limit (should use default)
	result, err := rateLimiter.CheckRateLimit(projectID, 0, 10)
	assert.NoError(t, err)
	assert.True(t, result.Allowed)

	// Test with invalid burst limit (should calculate default)
	rateLimiter.ResetRateLimit(projectID)
	result, err = rateLimiter.CheckRateLimit(projectID, 10, 0)
	assert.NoError(t, err)
	assert.True(t, result.Allowed)
	assert.True(t, result.Remaining >= 49) // Should be at least 50 tokens (5x RPS) minus 1

	// Test with both invalid (should use defaults)
	rateLimiter.ResetRateLimit(projectID)
	result, err = rateLimiter.CheckRateLimit(projectID, -1, -1)
	assert.NoError(t, err)
	assert.True(t, result.Allowed)
}

func Test_ResetRateLimit_ClearsRateLimitData(t *testing.T) {
	rateLimiter := NewRateLimiter()
	projectID := uuid.New()
	rpsLimit := 1
	burstLimit := 1

	// Clean up any existing data
	rateLimiter.ResetRateLimit(projectID)

	// Use up the token
	result, err := rateLimiter.CheckRateLimit(projectID, rpsLimit, burstLimit)
	assert.NoError(t, err)
	assert.True(t, result.Allowed)

	// Should be denied now
	result, err = rateLimiter.CheckRateLimit(projectID, rpsLimit, burstLimit)
	assert.NoError(t, err)
	assert.False(t, result.Allowed)

	// Reset the rate limit
	err = rateLimiter.ResetRateLimit(projectID)
	assert.NoError(t, err)

	// Should be allowed again immediately (full bucket)
	result, err = rateLimiter.CheckRateLimit(projectID, rpsLimit, burstLimit)
	assert.NoError(t, err)
	assert.True(t, result.Allowed)
}

func Test_CheckRateLimit_HighThroughput_MaintainsAccuracy(t *testing.T) {
	rateLimiter := NewRateLimiter()
	projectID := uuid.New()
	rpsLimit := 100
	burstLimit := 10 // Small burst to make testing easier

	// Clean up any existing data
	rateLimiter.ResetRateLimit(projectID)

	allowedCount := 0
	totalRequests := 50

	start := time.Now()
	for i := 0; i < totalRequests; i++ {
		result, err := rateLimiter.CheckRateLimit(projectID, rpsLimit, burstLimit)
		assert.NoError(t, err)

		if result.Allowed {
			allowedCount++
		}
	}

	// The burst, plus what refilled while the requests ran.
	refilled := int(time.Since(start).Seconds()*float64(rpsLimit)) + 1
	assert.GreaterOrEqual(t, allowedCount, burstLimit)
	assert.LessOrEqual(t, allowedCount, burstLimit+refilled)
}

func Test_CheckRateLimit_CalledMoreOftenThanOncePerToken_RefillsAtConfiguredRate(t *testing.T) {
	rateLimiter := NewRateLimiter()
	projectID := uuid.New()
	rpsLimit := 200
	burstLimit := 1
	rateLimiter.ResetRateLimit(projectID)

	// Calls a fraction of a token apart: flooring each refill used to lose them all.
	allowed := 0
	start := time.Now()
	for time.Since(start) < time.Second {
		result, err := rateLimiter.CheckRateLimit(projectID, rpsLimit, burstLimit)
		assert.NoError(t, err)
		if result.Allowed {
			allowed++
		}
	}

	elapsed := time.Since(start).Seconds()
	assert.GreaterOrEqual(t, float64(allowed), 0.8*elapsed*float64(rpsLimit))
	assert.LessOrEqual(t, float64(allowed), elapsed*float64(rpsLimit)+float64(burstLimit)+1)
}

func Test_TakeBatch_WithinBurst_AdmitsThenRefusesWithRetryAfter(t *testing.T) {
	rateLimiter := NewRateLimiter()
	projectID := uuid.New()
	rateLimiter.ResetRateLimit(projectID)

	first := rateLimiter.TakeBatch(projectID, 800, 100, 1000)
	assert.True(t, first.Allowed)
	assert.False(t, first.Local)

	// 200 tokens left: a 500-row batch waits for about 300 more.
	second := rateLimiter.TakeBatch(projectID, 500, 100, 1000)
	assert.False(t, second.Allowed)
	assert.InDelta(t, 3*time.Second, second.RetryAfter, float64(100*time.Millisecond))
}

func Test_TakeBatch_LargerThanBurst_AdmittedWhenFullAndRepaidBeforeTheNext(t *testing.T) {
	rateLimiter := NewRateLimiter()
	projectID := uuid.New()
	rateLimiter.ResetRateLimit(projectID)

	// A full bucket admits a batch bigger than the burst and goes into debt.
	assert.True(t, rateLimiter.TakeBatch(projectID, 300, 100, 100).Allowed)

	// The debt (-200) is repaid before even one row more gets in.
	next := rateLimiter.TakeBatch(projectID, 1, 100, 100)
	assert.False(t, next.Allowed)
	assert.InDelta(t, 2*time.Second, next.RetryAfter, float64(100*time.Millisecond))
}

func Test_TakeBatch_ValkeyUnreachable_LocalBucketDecides(t *testing.T) {
	rateLimiter := &RateLimiter{client: closedClient(t), breaker: &cache.Breaker{}}
	projectID := uuid.New()

	within := rateLimiter.TakeBatch(projectID, 1000, 1000, 5000)
	assert.True(t, within.Allowed)
	assert.True(t, within.Local)

	over := rateLimiter.TakeBatch(projectID, 5000, 1000, 5000)
	assert.False(t, over.Allowed)
	assert.True(t, over.Local)
	assert.Greater(t, over.RetryAfter, time.Duration(0))
}

// A project limited to 1 log/s could send 5,000 logs, then one more (refused,
// but it cut the key's TTL to 5 minutes), stay quiet for those 5 minutes and
// find a full bucket: about 17 logs/s whatever the limit.
func Test_TakeBatch_RefusedRequestWhileInDebt_KeepsTheBucketUntilTheDebtIsRepaid(t *testing.T) {
	rateLimiter := NewRateLimiter()
	projectID := uuid.New()
	t.Cleanup(func() { _ = rateLimiter.ResetRateLimit(projectID) })

	assert.True(t, rateLimiter.TakeBatch(projectID, 5000, 1, 5).Allowed)
	assert.False(t, rateLimiter.TakeBatch(projectID, 1, 1, 5).Allowed)

	// The bucket owes 4,995 and is full again 5,000 s from now.
	assert.GreaterOrEqual(t, bucketTTL(t, projectID), 4990*time.Second)
}

func Test_TakeBatch_BucketIsFull_KeyExpiresAfterTheMinimum(t *testing.T) {
	rateLimiter := NewRateLimiter()
	projectID := uuid.New()
	t.Cleanup(func() { _ = rateLimiter.ResetRateLimit(projectID) })

	assert.True(t, rateLimiter.TakeBatch(projectID, 10, 100, 500).Allowed)

	ttl := bucketTTL(t, projectID)
	assert.GreaterOrEqual(t, ttl, minTTL-5*time.Second)
	assert.LessOrEqual(t, ttl, minTTL+2*time.Minute)
}

// During a Valkey outage a full map of local buckets started over, which
// forgave every bucket's debt.
func Test_LocalBuckets_WhenTheMapIsFull_KeepBucketsInDebt(t *testing.T) {
	var buckets localBuckets
	start := time.Now().UTC()
	inDebt := uuid.New()
	allowed, _ := buckets.take(inDebt, 1, 5, 5000, 5, start)
	require.True(t, allowed)
	for range maxLocalBuckets - 1 {
		buckets.take(uuid.New(), 1, 5, 1, 1, start)
	}

	// Ten seconds on the others are full again; a new project arrives.
	later := start.Add(10 * time.Second)
	buckets.take(uuid.New(), 1, 5, 1, 1, later)

	allowed, wait := buckets.take(inDebt, 1, 5, 1, 1, later)
	assert.False(t, allowed)
	assert.Greater(t, wait, time.Hour)
	assert.LessOrEqual(t, len(buckets.buckets), maxLocalBuckets)
}

func Test_LocalBuckets_WhenEveryBucketIsInDebt_StayBounded(t *testing.T) {
	var buckets localBuckets
	now := time.Now().UTC()
	for range maxLocalBuckets + 10 {
		buckets.take(uuid.New(), 1, 5, 5000, 5, now)
	}

	assert.LessOrEqual(t, len(buckets.buckets), maxLocalBuckets)
}

func bucketTTL(t *testing.T, projectID uuid.UUID) time.Duration {
	client := cache.GetCache()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	seconds, err := client.Do(ctx, client.B().Ttl().Key(keyPrefix+projectID.String()).Build()).AsInt64()
	require.NoError(t, err)
	return time.Duration(seconds) * time.Second
}

func closedClient(t *testing.T) valkey.Client {
	env := config.GetEnv()
	options := valkey.ClientOption{
		InitAddress: []string{env.ValkeyHost + ":" + env.ValkeyPort},
		Password:    env.ValkeyPassword,
		Username:    env.ValkeyUsername,
	}
	if env.ValkeyIsSsl {
		options.TLSConfig = &tls.Config{ServerName: env.ValkeyHost}
	}
	client, err := valkey.NewClient(options)
	require.NoError(t, err)
	client.Close()
	return client
}

func Test_CheckRateLimit_RetryAfterSeconds_CalculatedCorrectly(t *testing.T) {
	rateLimiter := NewRateLimiter()
	projectID := uuid.New()
	rpsLimit := 2 // 2 RPS = 500ms per token
	burstLimit := 1

	// Clean up any existing data
	rateLimiter.ResetRateLimit(projectID)

	// Use the only token
	result, err := rateLimiter.CheckRateLimit(projectID, rpsLimit, burstLimit)
	assert.NoError(t, err)
	assert.True(t, result.Allowed)

	// Next request should be denied with appropriate retry-after
	result, err = rateLimiter.CheckRateLimit(projectID, rpsLimit, burstLimit)
	assert.NoError(t, err)
	assert.False(t, result.Allowed)
	assert.True(t, result.RetryAfterSec > 0)
	// For 2 RPS, retry after should be at most 1 second (500ms rounded up)
	assert.True(t, result.RetryAfterSec <= 1)
}

func Test_CheckRateLimit_ResetTimeCalculation_IsAccurate(t *testing.T) {
	rateLimiter := NewRateLimiter()
	projectID := uuid.New()
	rpsLimit := 10 // 10 RPS = 100ms per token
	burstLimit := 5

	// Clean up any existing data
	rateLimiter.ResetRateLimit(projectID)

	// Use all tokens
	for i := 0; i < burstLimit; i++ {
		result, err := rateLimiter.CheckRateLimit(projectID, rpsLimit, burstLimit)
		assert.NoError(t, err)
		assert.True(t, result.Allowed)
	}

	// Next request should be denied with reset time in the future
	result, err := rateLimiter.CheckRateLimit(projectID, rpsLimit, burstLimit)
	assert.NoError(t, err)
	assert.False(t, result.Allowed)

	now := time.Now()
	assert.True(t, result.ResetTime.After(now))
	// Reset time should be reasonable (within a few seconds for full refill)
	assert.True(t, result.ResetTime.Before(now.Add(10*time.Second)))
}
