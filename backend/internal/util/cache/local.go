package cache_utils

import (
	"sync"
	"time"
)

// LocalCache is a small in-process map whose entries expire, for request-path
// lookups to fall back on while Valkey is unreachable instead of querying the
// database on every request.
type LocalCache[V any] struct {
	mu         sync.Mutex
	ttl        time.Duration
	maxEntries int
	items      map[string]localEntry[V]
}

type localEntry[V any] struct {
	value   V
	expires time.Time
}

func NewLocalCache[V any](ttl time.Duration, maxEntries int) *LocalCache[V] {
	return &LocalCache[V]{ttl: ttl, maxEntries: maxEntries, items: map[string]localEntry[V]{}}
}

func (l *LocalCache[V]) Get(key string) (V, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.items[key]
	if !ok || time.Now().After(entry.expires) {
		var zero V
		return zero, false
	}
	return entry.value, true
}

func (l *LocalCache[V]) Set(key string, value V) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.items[key]; !ok && len(l.items) >= l.maxEntries {
		// Full (keys are partly caller-chosen): start over rather than track ages.
		l.items = map[string]localEntry[V]{}
	}
	l.items[key] = localEntry[V]{value: value, expires: time.Now().Add(l.ttl)}
}

func (l *LocalCache[V]) Delete(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.items, key)
}
