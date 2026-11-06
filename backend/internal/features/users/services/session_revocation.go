package users_services

import (
	users_interfaces "obzly/internal/features/users/interfaces"
	"obzly/internal/util/logger"

	"github.com/google/uuid"
)

// sessionRevocationListener is set only in control-plane mode; nil makes
// notifySessionRevocation a no-op.
var sessionRevocationListener users_interfaces.SessionRevocationListener

func SetSessionRevocationListener(listener users_interfaces.SessionRevocationListener) {
	sessionRevocationListener = listener
}

// notifySessionRevocation runs on SignOut and deactivation alike, so a Site
// session never outlives its central one.
func notifySessionRevocation(userID uuid.UUID) {
	if sessionRevocationListener == nil {
		return
	}
	if err := sessionRevocationListener.OnUserSessionsRevoked(userID); err != nil {
		logger.GetLogger().Error("session revocation listener failed", "userId", userID, "error", err)
	}
}
