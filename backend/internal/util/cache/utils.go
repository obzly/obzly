package cache_utils

import (
	"context"
	"encoding/json"
	"time"

	"obzly/internal/cache"

	"github.com/google/uuid"
	"github.com/valkey-io/valkey-go"
)

const (
	// DefaultCacheTimeout bounds one call. These caches sit on the request path
	// and every caller falls back to the database on a miss, so a slow Valkey
	// must cost little: at 10 s a Valkey outage held each ingest request for
	// several timeouts.
	DefaultCacheTimeout = 300 * time.Millisecond
	DefaultCacheExpiry  = 10 * time.Minute
	DefaultQueueTimeout = 30 * time.Second
)

type CacheUtil[T any] struct {
	client  valkey.Client
	prefix  string
	timeout time.Duration
	expiry  time.Duration
	breaker *cache.Breaker
}

func NewCacheUtil[T any](client valkey.Client, prefix string) *CacheUtil[T] {
	return &CacheUtil[T]{
		client:  client,
		prefix:  prefix,
		timeout: DefaultCacheTimeout,
		expiry:  DefaultCacheExpiry,
		breaker: cache.GetBreaker(),
	}
}

// WithExpiry overrides the default 10-minute TTL. Use a shorter one for caches keyed on
// attacker-influenced input (e.g. a negative cache of unknown API keys), to bound how
// many bogus entries a flood can accumulate.
func (c *CacheUtil[T]) WithExpiry(expiry time.Duration) *CacheUtil[T] {
	c.expiry = expiry
	return c
}

func TestCacheConnection() {
	client := cache.GetCache()

	cacheUtil := NewCacheUtil[string](client, "test:")

	// One key per process: replicas starting together shared one key, and one
	// replica's Set landing after another's Invalidate failed its start.
	testKey := "connection_test:" + uuid.NewString()
	testValue := "valkey_is_working"

	cacheUtil.Set(testKey, &testValue)

	retrievedValue := cacheUtil.Get(testKey)

	if retrievedValue == nil {
		panic("Cache test failed: could not retrieve cached value")
	}

	if *retrievedValue != testValue {
		panic("Cache test failed: retrieved value does not match expected")
	}

	cacheUtil.Invalidate(testKey)

	cleanupCheck := cacheUtil.Get(testKey)
	if cleanupCheck != nil {
		panic("Cache test failed: test key was not properly invalidated")
	}
}

// Get returns key's item, or nil on a miss or when Valkey is unreachable.
func (c *CacheUtil[T]) Get(key string) *T {
	item, _ := c.Lookup(key)
	return item
}

// Lookup is Get telling a miss (nil, nil) from Valkey being unreachable
// (nil, error), for callers that keep a local fallback.
func (c *CacheUtil[T]) Lookup(key string) (*T, error) {
	if !c.breaker.Available() {
		return nil, cache.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	fullKey := c.prefix + key
	result := c.client.Do(ctx, c.client.B().Get().Key(fullKey).Build())

	if err := result.Error(); err != nil {
		if valkey.IsValkeyNil(err) {
			return nil, nil
		}
		return nil, c.breaker.Observe(err)
	}

	data, err := result.AsBytes()
	if err != nil {
		return nil, nil
	}

	var item T
	if err := json.Unmarshal(data, &item); err != nil {
		return nil, nil
	}

	return &item, nil
}

func (c *CacheUtil[T]) Set(key string, item *T) {
	if !c.breaker.Available() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	data, err := json.Marshal(item)
	if err != nil {
		return
	}

	fullKey := c.prefix + key
	result := c.client.Do(ctx, c.client.B().Set().Key(fullKey).Value(string(data)).Ex(c.expiry).Build())
	_ = c.breaker.Observe(result.Error())
}

func (c *CacheUtil[T]) Invalidate(key string) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	fullKey := c.prefix + key
	_ = c.breaker.Observe(c.client.Do(ctx, c.client.B().Del().Key(fullKey).Build()).Error())
}
