package testing

import (
	"sync"
	"testing"

	"obzly/internal/cache"
)

// ValkeyUnreachable makes request-path Valkey calls (caches, the logs rate
// limit, ingestion counts) behave as during a Valkey outage until the
// returned stop is called or the test ends. Process-wide: don't use it in a
// t.Parallel test.
func ValkeyUnreachable(t *testing.T) (stop func()) {
	restore := cache.GetBreaker().HoldOpen()
	var once sync.Once
	stop = func() { once.Do(restore) }
	t.Cleanup(stop)
	return stop
}
