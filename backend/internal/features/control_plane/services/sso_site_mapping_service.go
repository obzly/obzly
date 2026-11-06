package control_plane_services

import (
	"errors"
	"fmt"
	"strings"

	control_plane_models "obzly/internal/features/control_plane/models"
	control_plane_repositories "obzly/internal/features/control_plane/repositories"
	users_interfaces "obzly/internal/features/users/interfaces"
	users_models "obzly/internal/features/users/models"
	"obzly/internal/util/logger"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrSSOSiteMappingRole     = errors.New("invalid role for this mapping")
	ErrSSOSiteMappingExists   = errors.New("this group is already mapped to that site and project")
	ErrSSOSiteMappingSite     = errors.New("site not found")
	ErrSSOSiteMappingNotFound = errors.New("group mapping not found")
	ErrSSOConnectorNotFound   = errors.New("SSO connector not found")
)

// SSOSiteMappingService maps SSO groups to Site access on a control plane and keeps the
// user's SSO-owned site_grants in line with their groups at sign-in.
type SSOSiteMappingService struct {
	repository      *control_plane_repositories.SSOSiteMappingRepository
	grantRepository *control_plane_repositories.GrantRepository
	auditLogWriter  users_interfaces.AuditLogWriter
}

func (s *SSOSiteMappingService) SetAuditLogWriter(writer users_interfaces.AuditLogWriter) {
	s.auditLogWriter = writer
}

func (s *SSOSiteMappingService) audit(message string, actor *uuid.UUID) {
	if s.auditLogWriter != nil {
		s.auditLogWriter.WriteAuditLog(message, actor, nil)
	}
}

func (s *SSOSiteMappingService) List(
	actor *users_models.User,
	connectorID uuid.UUID,
) ([]*control_plane_models.SSOSiteGroupMapping, error) {
	if !actor.CanUpdateSettings() {
		return nil, ErrGrantForbidden
	}
	return s.repository.ListByConnector(connectorID)
}

// Create adds a mapping, with the same permission and role rules as
// GrantService.CreateGrant (Site-wide ADMIN: root admin only; never OWNER).
func (s *SSOSiteMappingService) Create(
	actor *users_models.User,
	connectorID uuid.UUID,
	groupName, siteID string,
	projectID *uuid.UUID,
	role string,
) (*control_plane_models.SSOSiteGroupMapping, error) {
	if !actor.CanUpdateSettings() {
		return nil, ErrGrantForbidden
	}
	if projectID == nil && !siteWideGrantRoles[role] {
		return nil, ErrSSOSiteMappingRole
	}
	if projectID != nil && !projectGrantRoles[role] {
		return nil, ErrSSOSiteMappingRole
	}
	if projectID == nil && role == GrantRoleSiteAdmin && !actor.IsRootAdmin {
		return nil, ErrGrantSiteAdminRootOnly
	}
	groupName = strings.TrimSpace(groupName)
	if groupName == "" {
		return nil, errors.New("group name is required")
	}

	mapping := &control_plane_models.SSOSiteGroupMapping{
		ConnectorID: connectorID,
		GroupName:   groupName,
		SiteID:      siteID,
		ProjectID:   projectID,
		Role:        role,
	}
	if err := s.repository.Create(mapping); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch {
			case pgErr.Code == "23505":
				return nil, ErrSSOSiteMappingExists
			case pgErr.Code == "23503" && strings.Contains(pgErr.ConstraintName, "connector"):
				return nil, ErrSSOConnectorNotFound
			case pgErr.Code == "23503":
				return nil, ErrSSOSiteMappingSite
			}
		}
		return nil, fmt.Errorf("failed to create group mapping: %w", err)
	}

	s.audit(fmt.Sprintf("SSO group mapping created: %q -> site %s (%s)", groupName, siteID, role), &actor.ID)
	return mapping, nil
}

// Delete removes a mapping. Access it granted is removed from each user at
// their next SSO sign-in (like leaving the group), not immediately.
func (s *SSOSiteMappingService) Delete(actor *users_models.User, connectorID, mappingID uuid.UUID) error {
	if !actor.CanUpdateSettings() {
		return ErrGrantForbidden
	}
	deleted, err := s.repository.Delete(connectorID, mappingID)
	if err != nil {
		return fmt.Errorf("failed to delete group mapping: %w", err)
	}
	if !deleted {
		return ErrSSOSiteMappingNotFound
	}
	s.audit("SSO group mapping (site) deleted", &actor.ID)
	return nil
}

// GroupsGrantSiteAccess reports whether any of groups maps to Site access on
// connectorID (see users_interfaces.SSOSiteGrantSyncer).
func (s *SSOSiteMappingService) GroupsGrantSiteAccess(connectorID uuid.UUID, groups []string) (bool, error) {
	mappings, err := s.repository.ListByConnectorAndGroups(connectorID, groups)
	if err != nil {
		return false, fmt.Errorf("failed to load site group mappings: %w", err)
	}
	return len(mappings) > 0, nil
}

// grantScope identifies one grant slot: a Site, and a project on it or the
// whole Site - the same key site_grants' unique index uses.
type grantScope struct {
	siteID    string
	projectID uuid.UUID // uuid.Nil = Site-wide
}

func scopeOf(siteID string, projectID *uuid.UUID) grantScope {
	if projectID == nil {
		return grantScope{siteID: siteID}
	}
	return grantScope{siteID: siteID, projectID: *projectID}
}

// grantRoleRank orders roles within one scope: Site-wide ADMIN > MEMBER >
// VIEWER, and PROJECT_ADMIN > PROJECT_MEMBER > PROJECT_VIEWER.
func grantRoleRank(role string) int {
	switch role {
	case GrantRoleSiteAdmin, "PROJECT_ADMIN":
		return 3
	case GrantRoleSiteMember, "PROJECT_MEMBER":
		return 2
	case GrantRoleSiteViewer, "PROJECT_VIEWER":
		return 1
	default:
		return 0
	}
}

// SyncSSOSiteGrants makes the site_grants connectorID owns for userID match the user's
// current groups; the highest mapped role wins. Hand-made grants and other connectors'
// grants are never touched and take precedence. Changed Sites get their cached
// token/refusal invalidated.
func (s *SSOSiteMappingService) SyncSSOSiteGrants(userID, connectorID uuid.UUID, groups []string) error {
	mappings, err := s.repository.ListByConnectorAndGroups(connectorID, groups)
	if err != nil {
		return fmt.Errorf("failed to load site group mappings: %w", err)
	}

	type desiredGrant struct {
		siteID    string
		projectID *uuid.UUID
		role      string
	}
	desired := map[grantScope]desiredGrant{}
	for _, m := range mappings {
		key := scopeOf(m.SiteID, m.ProjectID)
		if current, ok := desired[key]; !ok || grantRoleRank(m.Role) > grantRoleRank(current.role) {
			desired[key] = desiredGrant{siteID: m.SiteID, projectID: m.ProjectID, role: m.Role}
		}
	}

	owned, err := s.repository.ListSSOGrants(userID, connectorID)
	if err != nil {
		return fmt.Errorf("failed to load SSO-owned site grants: %w", err)
	}

	var firstErr error
	record := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}
	touched := map[string]bool{}
	ownedScopes := map[grantScope]bool{}

	for _, grant := range owned {
		key := scopeOf(grant.SiteID, grant.ProjectID)
		ownedScopes[key] = true
		want, ok := desired[key]
		switch {
		case !ok:
			if err := s.grantRepository.Delete(grant.ID); err != nil {
				record(fmt.Errorf("failed to remove grant on site %s: %w", grant.SiteID, err))
				continue
			}
			touched[grant.SiteID] = true
			s.audit(fmt.Sprintf("SSO group sync: removed %s access to site %s (no longer in a mapped group)",
				grant.Role, grant.SiteID), &userID)
		case want.role != grant.Role:
			if err := s.repository.UpdateGrantRole(grant.ID, want.role); err != nil {
				record(fmt.Errorf("failed to update grant on site %s: %w", grant.SiteID, err))
				continue
			}
			touched[grant.SiteID] = true
			s.audit(fmt.Sprintf("SSO group sync: site %s access changed from %s to %s",
				grant.SiteID, grant.Role, want.role), &userID)
		}
	}

	for key, want := range desired {
		if ownedScopes[key] {
			continue
		}
		// A hand-made (or other connector's) grant already holds this slot; leave it.
		existing, err := s.grantRepository.ListForUserAndSite(userID, want.siteID)
		if err != nil {
			record(fmt.Errorf("failed to check grants on site %s: %w", want.siteID, err))
			continue
		}
		taken := false
		for _, g := range existing {
			if scopeOf(g.SiteID, g.ProjectID) == key {
				taken = true
				break
			}
		}
		if taken {
			continue
		}
		connector := connectorID
		grant := &control_plane_models.SiteGrant{
			UserID:         userID,
			SiteID:         want.siteID,
			ProjectID:      want.projectID,
			Role:           want.role,
			CreatedBy:      userID, // granted by their own sign-in, via the connector's mapping
			SSOConnectorID: &connector,
		}
		if err := s.grantRepository.Create(grant); err != nil {
			record(fmt.Errorf("failed to grant access to site %s: %w", want.siteID, err))
			continue
		}
		touched[want.siteID] = true
		s.audit(fmt.Sprintf("SSO group sync: granted %s access to site %s", want.role, want.siteID), &userID)
	}

	for siteID := range touched {
		siteTokenService.Invalidate(userID, siteID)
	}
	if firstErr != nil {
		logger.GetLogger().Error("SSO site grant sync incomplete", "userId", userID, "error", firstErr)
	}
	return firstErr
}
