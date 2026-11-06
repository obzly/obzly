package control_plane_services

import (
	"errors"
	"fmt"

	control_plane_dto "obzly/internal/features/control_plane/dto"
	control_plane_models "obzly/internal/features/control_plane/models"
	control_plane_repositories "obzly/internal/features/control_plane/repositories"
	users_interfaces "obzly/internal/features/users/interfaces"
	users_models "obzly/internal/features/users/models"
	users_repositories "obzly/internal/features/users/repositories"

	"github.com/google/uuid"
)

var (
	ErrGrantForbidden   = errors.New("only an administrator can manage site grants")
	ErrGrantInvalidRole = errors.New("invalid role for this grant")
	// ErrGrantSiteAdminRootOnly: Site-wide ADMIN can set local passwords and reset MFA on
	// that Site, so only the root admin may grant it.
	ErrGrantSiteAdminRootOnly = errors.New("only the root admin can grant Site-wide ADMIN")
)

// siteWideGrantRoles and projectGrantRoles mirror the migration's CHECK constraint so a
// bad role is a clean 400. Site-wide MEMBER / VIEWER give that project role on every
// project of the Site (see SiteWideProjectRole).
var siteWideGrantRoles = map[string]bool{
	GrantRoleSiteAdmin:  true,
	GrantRoleSiteMember: true,
	GrantRoleSiteViewer: true,
}

// Project-scoped roles (ProjectID set).
var projectGrantRoles = map[string]bool{
	"PROJECT_ADMIN":  true,
	"PROJECT_MEMBER": true,
	"PROJECT_VIEWER": true,
}

const (
	GrantRoleSiteAdmin  = "ADMIN"
	GrantRoleSiteMember = "MEMBER"
	GrantRoleSiteViewer = "VIEWER"
)

// SiteWideProjectRole is the project role a Site-wide MEMBER/VIEWER grant confers on
// every project; "" for ADMIN (carried by site_role) or none.
func SiteWideProjectRole(siteRole string) string {
	switch siteRole {
	case GrantRoleSiteMember:
		return "PROJECT_MEMBER"
	case GrantRoleSiteViewer:
		return "PROJECT_VIEWER"
	default:
		return ""
	}
}

type GrantService struct {
	grantRepository *control_plane_repositories.GrantRepository
	userRepository  *users_repositories.UserRepository
	auditLogWriter  users_interfaces.AuditLogWriter
}

func (s *GrantService) SetAuditLogWriter(writer users_interfaces.AuditLogWriter) {
	s.auditLogWriter = writer
}

// EffectiveGrant is every grant a user holds on one Site, reduced to a Site-wide role
// plus per-project roles.
type EffectiveGrant struct {
	SiteRole string
	Projects map[uuid.UUID]string
}

func (g EffectiveGrant) IsAdmin() bool {
	return g.SiteRole == GrantRoleSiteAdmin
}

func (g EffectiveGrant) HasAnyAccess() bool {
	return g.SiteRole != "" || len(g.Projects) > 0
}

// ResolveEffectiveGrant decides whether a user can reach a Site. The proxy calls it on
// every request so a revoked grant takes effect immediately.
func (s *GrantService) ResolveEffectiveGrant(userID uuid.UUID, siteID string) (EffectiveGrant, error) {
	grants, err := s.grantRepository.ListForUserAndSite(userID, siteID)
	if err != nil {
		return EffectiveGrant{}, fmt.Errorf("failed to list grants: %w", err)
	}
	// Teams only add access: the strongest role wins.
	teamGrants, err := s.grantRepository.TeamGrants(userID, siteID)
	if err != nil {
		return EffectiveGrant{}, fmt.Errorf("failed to list team grants: %w", err)
	}

	if effective, ok := reduceGrants(append(grants, teamGrants...))[siteID]; ok {
		return effective, nil
	}
	return EffectiveGrant{Projects: map[uuid.UUID]string{}}, nil
}

// ResolveEffectiveGrants is ResolveEffectiveGrant for every Site at once,
// in two queries; Sites the user can't reach are absent.
func (s *GrantService) ResolveEffectiveGrants(userID uuid.UUID) (map[string]EffectiveGrant, error) {
	grants, err := s.ListForUser(userID)
	if err != nil {
		return nil, fmt.Errorf("failed to list grants: %w", err)
	}
	return reduceGrants(grants), nil
}

// ListForUser is every grant userID holds, own and through teams.
func (s *GrantService) ListForUser(userID uuid.UUID) ([]*control_plane_models.SiteGrant, error) {
	grants, err := s.grantRepository.ListForUser(userID)
	if err != nil {
		return nil, err
	}
	teamGrants, err := s.grantRepository.TeamGrants(userID, "")
	if err != nil {
		return nil, err
	}
	return append(grants, teamGrants...), nil
}

var roleRank = map[string]int{
	GrantRoleSiteViewer: 1, GrantRoleSiteMember: 2, GrantRoleSiteAdmin: 3,
	"PROJECT_VIEWER": 1, "PROJECT_MEMBER": 2, "PROJECT_ADMIN": 3,
}

// reduceGrants folds grants into one EffectiveGrant per Site.
func reduceGrants(grants []*control_plane_models.SiteGrant) map[string]EffectiveGrant {
	bySite := map[string]EffectiveGrant{}
	for _, grant := range grants {
		effective, ok := bySite[grant.SiteID]
		if !ok {
			effective = EffectiveGrant{Projects: map[uuid.UUID]string{}}
		}
		if grant.ProjectID == nil {
			effective.SiteRole = strongerRole(effective.SiteRole, grant.Role)
		} else {
			effective.Projects[*grant.ProjectID] = strongerRole(effective.Projects[*grant.ProjectID], grant.Role)
		}
		bySite[grant.SiteID] = effective
	}
	return bySite
}

func strongerRole(current, candidate string) string {
	if roleRank[candidate] > roleRank[current] {
		return candidate
	}
	return current
}

func (s *GrantService) ListForSite(actor *users_models.User, siteID string) ([]*control_plane_models.SiteGrant, error) {
	if !actor.CanUpdateSettings() {
		return nil, ErrGrantForbidden
	}
	return s.grantRepository.ListForSite(siteID)
}

func (s *GrantService) CreateGrant(
	actor *users_models.User, request control_plane_dto.CreateGrantRequestDTO,
) (*control_plane_models.SiteGrant, error) {
	if !actor.CanUpdateSettings() {
		return nil, ErrGrantForbidden
	}
	// Mirrors the migration's CHECK constraint, for a clean 400.
	if request.ProjectID == nil && !siteWideGrantRoles[request.Role] {
		return nil, ErrGrantInvalidRole
	}
	if request.ProjectID != nil && !projectGrantRoles[request.Role] {
		return nil, ErrGrantInvalidRole
	}
	if request.ProjectID == nil && request.Role == GrantRoleSiteAdmin && !actor.IsRootAdmin {
		return nil, ErrGrantSiteAdminRootOnly
	}

	grant := &control_plane_models.SiteGrant{
		UserID:    request.UserID,
		SiteID:    request.SiteID,
		ProjectID: request.ProjectID,
		Role:      request.Role,
		CreatedBy: actor.ID,
	}
	if err := s.grantRepository.Create(grant); err != nil {
		return nil, fmt.Errorf("failed to create grant: %w", err)
	}

	// The cached Site token carries the old role, and a cached refusal would keep the
	// user locked out; drop both so the grant works on the next request.
	siteTokenService.Invalidate(grant.UserID, grant.SiteID)

	if s.auditLogWriter != nil {
		s.auditLogWriter.WriteAuditLog(
			fmt.Sprintf("Site grant created: user %s -> site %s (%s)", request.UserID, request.SiteID, request.Role),
			&actor.ID, nil,
		)
	}
	return grant, nil
}

// DeleteGrant revokes access for future proxied requests (checked per request) and
// invalidates the cached Site token for (userID, siteID).
func (s *GrantService) DeleteGrant(
	actor *users_models.User,
	grantID uuid.UUID,
) (*control_plane_models.SiteGrant, error) {
	if !actor.CanUpdateSettings() {
		return nil, ErrGrantForbidden
	}

	grant, err := s.grantRepository.GetByID(grantID)
	if err != nil {
		return nil, fmt.Errorf("failed to load grant: %w", err)
	}
	if grant == nil {
		return nil, nil
	}

	if err := s.grantRepository.Delete(grantID); err != nil {
		return nil, fmt.Errorf("failed to delete grant: %w", err)
	}

	// Best-effort: a revoked grant should stop working on the next request, not when
	// the cached token expires.
	siteTokenService.Invalidate(grant.UserID, grant.SiteID)

	if s.auditLogWriter != nil {
		s.auditLogWriter.WriteAuditLog(
			fmt.Sprintf("Site grant removed: user %s -> site %s", grant.UserID, grant.SiteID),
			&actor.ID, nil,
		)
	}
	return grant, nil
}
