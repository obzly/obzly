// Package partcache keeps aggregation results per time partition, as
// OpenObserve's result cache does: a partition that ended before the delay
// window no longer changes, so a later query over it reads the stored
// counts and computes only the partitions it lacks.
package partcache

import (
	"context"
	"time"

	"obzly/internal/telemetry"

	"github.com/valkey-io/valkey-go"
)

// Store holds partition results by key.
type Store interface {
	MGet(ctx context.Context, keys []string) (map[string][]byte, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
}

// Cache is a Store with its retention and delay window.
type Cache struct {
	store Store
	ttl   time.Duration
	// delay: partitions ending within it of now are never cached.
	delay time.Duration
}

const keyPrefix = "query_partition:"

// New returns a Cache over store, or nil (no caching) when ttl is 0.
func New(store Store, ttl, delay time.Duration) *Cache {
	if ttl <= 0 || store == nil {
		return nil
	}
	return &Cache{store: store, ttl: ttl, delay: delay}
}

// Cacheable reports whether a partition ending at end may be cached now.
func (c *Cache) Cacheable(end, now time.Time) bool {
	return !end.After(now.Add(-c.delay))
}

// Get returns the stored partitions among keys; a failure counts as misses.
func (c *Cache) Get(ctx context.Context, keys []string) map[string][]byte {
	if len(keys) == 0 {
		return nil
	}
	found, err := c.store.MGet(ctx, prefixed(keys))
	if err != nil {
		telemetry.QueryPartitionCache.WithLabelValues("error").Add(float64(len(keys)))
		return nil
	}
	result := make(map[string][]byte, len(found))
	for _, key := range keys {
		if value, ok := found[keyPrefix+key]; ok {
			result[key] = value
		}
	}
	telemetry.QueryPartitionCache.WithLabelValues("hit").Add(float64(len(result)))
	telemetry.QueryPartitionCache.WithLabelValues("miss").Add(float64(len(keys) - len(result)))
	return result
}

// Put stores one partition; a failure only means it is computed again.
func (c *Cache) Put(ctx context.Context, key string, value []byte) {
	if err := c.store.Set(ctx, keyPrefix+key, value, c.ttl); err != nil {
		telemetry.QueryPartitionCache.WithLabelValues("error").Inc()
	}
}

func prefixed(keys []string) []string {
	result := make([]string, len(keys))
	for i, key := range keys {
		result[i] = keyPrefix + key
	}
	return result
}

// Valkey is a Store in Valkey.
type Valkey struct {
	Client valkey.Client
}

func (v Valkey) MGet(ctx context.Context, keys []string) (map[string][]byte, error) {
	values, err := v.Client.Do(ctx, v.Client.B().Mget().Key(keys...).Build()).ToArray()
	if err != nil {
		return nil, err
	}
	result := make(map[string][]byte, len(keys))
	for i, value := range values {
		raw, err := value.AsBytes()
		if err != nil {
			continue // nil: not stored
		}
		result[keys[i]] = raw
	}
	return result, nil
}

func (v Valkey) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return v.Client.Do(ctx, v.Client.B().Set().Key(key).Value(valkey.BinaryString(value)).
		Ex(ttl).Build()).Error()
}

// Memory is a Store in process memory (tests).
type Memory struct {
	Values map[string][]byte
	Gets   int
}

func (m *Memory) MGet(_ context.Context, keys []string) (map[string][]byte, error) {
	m.Gets++
	result := map[string][]byte{}
	for _, key := range keys {
		if value, ok := m.Values[key]; ok {
			result[key] = value
		}
	}
	return result, nil
}

func (m *Memory) Set(_ context.Context, key string, value []byte, _ time.Duration) error {
	if m.Values == nil {
		m.Values = map[string][]byte{}
	}
	m.Values[key] = value
	return nil
}
