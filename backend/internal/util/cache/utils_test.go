package cache_utils

import (
	"sync"
	"testing"
)

func Test_TestCacheConnection_ManyReplicasStartingAtOnce_NonePanics(t *testing.T) {
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Errorf("connection check panicked: %v", recovered)
				}
			}()
			for range 20 {
				TestCacheConnection()
			}
		})
	}
	wg.Wait()
}
