package users_services

import (
	"sync"
	"time"

	"github.com/google/uuid"

	"obzly/internal/util/logger"
)

// activityWriteInterval: how stale a user's last_active_at may get, so
// recording activity isn't a database write per request.
const activityWriteInterval = 5 * time.Minute

var lastActivityWrite sync.Map // uuid.UUID -> time.Time

// RecordActivity notes that userID made an authenticated request. At most
// one write per user per activityWriteInterval per process, off the
// request path.
func (s *UserService) RecordActivity(userID uuid.UUID) {
	now := time.Now().UTC()
	if previous, ok := lastActivityWrite.Load(userID); ok {
		if at, ok := previous.(time.Time); ok && now.Sub(at) < activityWriteInterval {
			return
		}
	}
	lastActivityWrite.Store(userID, now)
	go func() {
		if err := s.userRepository.TouchLastActive(userID, now, activityWriteInterval); err != nil {
			logger.GetLogger().Warn("Failed to record user activity", "user", userID, "error", err)
		}
	}()
}
