package webanalytics

import (
	"context"
	"errors"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	liveCacheTTL = 30 * time.Second
	// liveScanSlots: raw reads one replica runs at once; more wait.
	liveScanSlots = 2
)

// liveCache shares a live read between viewers and Live refreshes for
// liveCacheTTL, runs one read per key at a time, and at most liveScanSlots
// reads at once.
type liveCache struct {
	mu      sync.Mutex
	entries map[string]*liveEntry
	group   singleflight.Group
	slots   chan struct{}
	now     func() time.Time
}

type liveEntry struct {
	result     *rollup
	to         time.Time
	durationMs int64
	expires    time.Time
}

func newLiveCache(now func() time.Time) *liveCache {
	return &liveCache{entries: map[string]*liveEntry{}, slots: make(chan struct{}, liveScanSlots), now: now}
}

// get returns key's entry, computing it with read when missing or expired.
// The result is shared: callers must not modify it.
func (c *liveCache) get(ctx context.Context, key string, read func() (*rollup, error)) (*liveEntry, bool, error) {
	c.mu.Lock()
	if entry := c.entries[key]; entry != nil && c.now().Before(entry.expires) {
		c.mu.Unlock()
		return entry, true, nil
	}
	c.mu.Unlock()

	value, err, shared := c.group.Do(key, func() (any, error) {
		select {
		case c.slots <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		defer func() { <-c.slots }()
		started := time.Now()
		result, err := read()
		if err != nil {
			return nil, err
		}
		now := c.now()
		entry := &liveEntry{result: result, to: now, durationMs: time.Since(started).Milliseconds(),
			expires: now.Add(liveCacheTTL)}
		c.mu.Lock()
		for existingKey, existing := range c.entries {
			if !now.Before(existing.expires) {
				delete(c.entries, existingKey)
			}
		}
		c.entries[key] = entry
		c.mu.Unlock()
		return entry, nil
	})
	if err != nil {
		return nil, false, err
	}
	entry, ok := value.(*liveEntry)
	if !ok {
		return nil, false, errors.New("web analytics live cache holds an unexpected value")
	}
	return entry, shared, nil
}
