package control_plane_services

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"

	users_interfaces "obzly/internal/features/users/interfaces"
	"obzly/internal/util/logger"

	"github.com/google/uuid"
)

// revocationListener implements users_interfaces.SessionRevocationListener; registered
// only in control-plane mode.
type revocationListener struct{}

// NewRevocationListener returns the listener SetupDependencies registers.
func NewRevocationListener() users_interfaces.SessionRevocationListener {
	return &revocationListener{}
}

// OnUserSessionsRevoked invalidates the cached Site token for every Site the user has a
// grant on, and best-effort logs each token out on the Site. Site failures are only
// logged: the cache invalidation already stops reuse, and the token expires soon anyway.
func (l *revocationListener) OnUserSessionsRevoked(userID uuid.UUID) error {
	grants, err := grantService.ListForUser(userID)
	if err != nil {
		return fmt.Errorf("failed to list grants for revocation: %w", err)
	}

	seen := make(map[string]bool, len(grants))
	for _, grant := range grants {
		if seen[grant.SiteID] {
			continue
		}
		seen[grant.SiteID] = true

		site, err := siteService.GetSite(grant.SiteID)
		if err != nil || site == nil {
			continue
		}

		// Invalidate synchronously; the Site logout runs in the background so one
		// unreachable Site can't stall a central sign-out.
		for _, key := range siteTokenCacheKeys(userID, site.ID) {
			if token, ok := siteTokenService.readCached(key, site.ApiURL); ok {
				go bestEffortSiteLogout(site.ApiURL, token)
			}
		}
		siteTokenService.Invalidate(userID, site.ID)
	}
	return nil
}

// bestEffortSiteLogout calls a Site's logout endpoint with the revoked token. Errors are
// logged, never returned: this is defense in depth on top of cache invalidation.
func bestEffortSiteLogout(apiURL, token string) {
	if err := checkEgressAllowed(apiURL); err != nil {
		logger.GetLogger().Warn("best-effort site logout skipped", "error", err)
		return
	}
	req, err := http.NewRequest(
		http.MethodPost,
		strings.TrimRight(apiURL, "/")+"/api/v1/users/logout",
		bytes.NewReader(nil),
	)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := exchangeHTTPClient.Do(req)
	if err != nil {
		logger.GetLogger().Warn("best-effort site logout failed", "error", err)
		return
	}
	_ = resp.Body.Close()
}
