package downdetect

import (
	"fmt"
	"sync"
	"time"

	logs_core "obzly/internal/features/logs/core"
	"obzly/internal/storage"
	cache_utils "obzly/internal/util/cache"
)

// checkTTL caches IsAvailable's result: the endpoint is unauthenticated and unrated, and
// each check makes 3 backend round trips. Uptime monitors poll far less often.
const checkTTL = 3 * time.Second

// DowndetectService fetches the active log repository at call time rather than storing
// it (see logs/core/di.go SetRepository).
type DowndetectService struct {
	mu          sync.Mutex
	lastChecked time.Time
	lastErr     error
}

func (s *DowndetectService) IsAvailable() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if time.Since(s.lastChecked) < checkTTL {
		return s.lastErr
	}

	s.lastErr = s.checkNow()
	s.lastChecked = time.Now()
	return s.lastErr
}

func (s *DowndetectService) checkNow() error {
	if err := storage.GetDb().Exec("SELECT 1").Error; err != nil {
		return fmt.Errorf("database check failed: %w", err)
	}

	if err := s.testCacheConnection(); err != nil {
		return fmt.Errorf("cache check failed: %w", err)
	}

	if err := logs_core.GetLogCoreRepository().TestConnection(); err != nil {
		return fmt.Errorf("logs storage check failed: %w", err)
	}

	return nil
}

func (s *DowndetectService) testCacheConnection() (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("cache connection test panicked: %v", r)
		}
	}()

	cache_utils.TestCacheConnection()
	return nil
}
