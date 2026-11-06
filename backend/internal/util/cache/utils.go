package cache_utils

import (
	"context"
	"encoding/json"
	"time"

	"obzly/internal/cache"

	"github.com/valkey-io/valkey-go"
)

const (
	DefaultCacheTimeout = 10 * time.Second
	DefaultCacheExpiry  = 10 * time.Minute
	DefaultQueueTimeout = 30 * time.Second
)

type CacheUtil[T any] struct {
	client  valkey.Client
	prefix  string
	timeout time.Duration
	expiry  time.Duration
}

func NewCacheUtil[T any](client valkey.Client, prefix string) *CacheUtil[T] {
	return &CacheUtil[T]{
		client:  client,
		prefix:  prefix,
		timeout: DefaultCacheTimeout,
		expiry:  DefaultCacheExpiry,
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

	testKey := "connection_test"
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

func (c *CacheUtil[T]) Get(key string) *T {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	fullKey := c.prefix + key
	result := c.client.Do(ctx, c.client.B().Get().Key(fullKey).Build())

	if result.Error() != nil {
		return nil
	}

	data, err := result.AsBytes()
	if err != nil {
		return nil
	}

	var item T
	if err := json.Unmarshal(data, &item); err != nil {
		return nil
	}

	return &item
}

func (c *CacheUtil[T]) Set(key string, item *T) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	data, err := json.Marshal(item)
	if err != nil {
		return
	}

	fullKey := c.prefix + key
	c.client.Do(ctx, c.client.B().Set().Key(fullKey).Value(string(data)).Ex(c.expiry).Build())
}

func (c *CacheUtil[T]) Invalidate(key string) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	fullKey := c.prefix + key
	c.client.Do(ctx, c.client.B().Del().Key(fullKey).Build())
}
