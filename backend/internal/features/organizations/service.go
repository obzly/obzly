package organizations

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"obzly/internal/config"
	"obzly/internal/features/audit_logs"
	control_plane_services "obzly/internal/features/control_plane/services"
	projects_repositories "obzly/internal/features/projects/repositories"
	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"
	users_repositories "obzly/internal/features/users/repositories"
	users_services "obzly/internal/features/users/services"
	"obzly/internal/storage"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var (
	ErrForbidden = errors.New("insufficient permissions")
	ErrNotFound  = errors.New("not found")
	ErrLastOwner = errors.New("an organization keeps at least one owner")
	// ErrPlanLimit: the organization's plan doesn't allow it.
	ErrPlanLimit = errors.New("plan limit reached")

	errNotOrgMember = errors.New("no member of this organization with that email; add them to the organization first")
)

var (
	slugPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)
	siteWideRoles     = []string{"ADMIN", "MEMBER", "VIEWER"}
	projectRoles      = []string{"PROJECT_ADMIN", "PROJECT_MEMBER", "PROJECT_VIEWER"}
	localSiteRoles    = []string{"MEMBER", "VIEWER"} // no instance-wide ADMIN through a team
	organizationRoles = []string{RoleOwner, RoleAdmin, RoleMember}
)

// Service is organizations' permission-checked operations. Global admins
// do everything; an organization's owners and admins manage its members,
// teams and team grants (only owners make owners); members read it.
// Creating and deleting organizations and moving Sites between them is for
// global admins.
type Service struct {
	repo     *repository
	users    *users_repositories.UserRepository
	projects *projects_repositories.ProjectRepository
	audit    *audit_logs.AuditLogService
	// SCIM: deactivating owned accounts, and their default plan.
	management *users_services.UserManagementService
	plans      *users_repositories.UserPlanRepository
}

// MemberRequest adds a user (by id or email) or changes their role.
type MemberRequest struct {
	UserID *uuid.UUID `json:"userId"`
	Email  string     `json:"email"`
	Role   string     `json:"role"`
}

// GrantRequest gives a team a role on a Site (or a project of it); SiteID
// "" is this instance's own projects on a standalone install.
type GrantRequest struct {
	SiteID    string     `json:"siteId"`
	ProjectID *uuid.UUID `json:"projectId"`
	Role      string     `json:"role"`
}

// OrgDetail is an organization with its Sites.
type OrgDetail struct {
	Organization
	Sites []string `json:"sites"`
}

func (s *Service) ListOrgs(user *users_models.User) ([]*Organization, error) {
	if isAdmin(user) {
		orgs, err := s.repo.orgs()
		if err != nil {
			return nil, err
		}
		_, roles, err := s.repo.orgsOf(user.ID)
		if err != nil {
			return nil, err
		}
		for _, org := range orgs {
			org.MyRole = roles[org.ID]
		}
		return orgs, nil
	}
	orgs, roles, err := s.repo.orgsOf(user.ID)
	for _, org := range orgs {
		org.MyRole = roles[org.ID]
	}
	return orgs, err
}

func (s *Service) GetOrg(orgID uuid.UUID, user *users_models.User) (*OrgDetail, error) {
	role, err := s.access(orgID, user, false)
	if err != nil {
		return nil, err
	}
	org, err := s.repo.org(orgID)
	if err != nil {
		return nil, notFound(err)
	}
	org.MyRole = role
	sites, err := s.repo.sites(orgID)
	if err != nil {
		return nil, err
	}
	return &OrgDetail{Organization: *org, Sites: sites}, nil
}

func (s *Service) CreateOrg(name, slug string, user *users_models.User) (*Organization, error) {
	if !isAdmin(user) {
		return nil, ErrForbidden
	}
	name, slug = strings.TrimSpace(name), strings.ToLower(strings.TrimSpace(slug))
	if name == "" || len(name) > 200 || !slugPattern.MatchString(slug) {
		return nil, errors.New("a name and a slug of 2-63 lowercase letters, digits or dashes are required")
	}
	org := &Organization{ID: uuid.New(), Name: name, Slug: slug, CreatedAt: time.Now().UTC(), MyRole: RoleOwner}
	err := storage.GetDb().Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(org).Error; err != nil {
			return err
		}
		return tx.Create(&Membership{OrgID: org.ID, UserID: user.ID, Role: RoleOwner, CreatedAt: org.CreatedAt}).Error
	})
	if err != nil {
		if strings.Contains(err.Error(), "idx_organizations_slug") {
			return nil, errors.New("that slug is taken")
		}
		return nil, err
	}
	s.audit.WriteAuditLog("Organization created: "+slug, &user.ID, nil)
	return org, nil
}

func (s *Service) RenameOrg(orgID uuid.UUID, name string, user *users_models.User) (*Organization, error) {
	if _, err := s.access(orgID, user, true); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 200 {
		return nil, errors.New("a name of up to 200 characters is required")
	}
	if err := storage.GetDb().Model(&Organization{}).Where("id = ?", orgID).Update("name", name).Error; err != nil {
		return nil, err
	}
	return s.repo.org(orgID)
}

// DeleteOrg removes an organization, its teams and their grants; its
// Sites belong to no organization until assigned again.
func (s *Service) DeleteOrg(orgID uuid.UUID, user *users_models.User) error {
	if !isAdmin(user) {
		return ErrForbidden
	}
	org, err := s.repo.org(orgID)
	if err != nil {
		return notFound(err)
	}
	if org.Slug == "default" {
		return errors.New("the Default organization can't be deleted")
	}
	affected, err := s.orgGrantReach(orgID)
	if err != nil {
		return err
	}
	if err := storage.GetDb().Delete(&Organization{}, "id = ?", orgID).Error; err != nil {
		return err
	}
	invalidate(affected)
	s.audit.WriteAuditLog("Organization deleted: "+org.Slug, &user.ID, nil)
	return nil
}

func (s *Service) ListMembers(orgID uuid.UUID, user *users_models.User) ([]Membership, error) {
	if _, err := s.access(orgID, user, false); err != nil {
		return nil, err
	}
	return s.repo.members(orgID)
}

// SetMember adds a user to the organization or changes their role. Only
// owners (and global admins) make or unmake owners; the last owner stays.
func (s *Service) SetMember(orgID uuid.UUID, request MemberRequest, user *users_models.User) error {
	callerRole, err := s.access(orgID, user, true)
	if err != nil {
		return err
	}
	if !slices.Contains(organizationRoles, request.Role) {
		return errors.New("role must be ORG_OWNER, ORG_ADMIN or ORG_MEMBER")
	}
	target, err := s.resolveUser(request)
	if err != nil {
		return err
	}
	current, err := s.repo.role(orgID, target.ID)
	if err != nil {
		return err
	}
	ownerChange := request.Role == RoleOwner || current == RoleOwner
	if ownerChange && callerRole != RoleOwner && !isAdmin(user) {
		return errors.New("only an owner can make or change owners")
	}
	return storage.GetDb().Transaction(func(tx *gorm.DB) error {
		if current == "" {
			if err := s.checkMemberQuota(tx, orgID); err != nil {
				return err
			}
		}
		if current == RoleOwner && request.Role != RoleOwner {
			if owners, err := s.repo.countOwners(tx, orgID); err != nil {
				return err
			} else if owners <= 1 {
				return errors.New("an organization keeps at least one owner")
			}
		}
		if err := tx.Exec(
			"INSERT INTO organization_memberships (org_id, user_id, role) VALUES (?, ?, ?) "+
				"ON CONFLICT (org_id, user_id) DO UPDATE SET role = EXCLUDED.role",
			orgID,
			target.ID,
			request.Role,
		).Error; err != nil {
			return err
		}
		s.audit.WriteAuditLog(
			fmt.Sprintf("Organization member set: %s as %s", target.Email, request.Role),
			&user.ID,
			nil,
		)
		return nil
	})
}

// RemoveMember removes a user from the organization and its teams.
func (s *Service) RemoveMember(orgID, userID uuid.UUID, user *users_models.User) error {
	callerRole, err := s.access(orgID, user, true)
	if err != nil {
		return err
	}
	current, err := s.repo.role(orgID, userID)
	if err != nil {
		return err
	}
	if current == "" {
		return ErrNotFound
	}
	if current == RoleOwner && callerRole != RoleOwner && !isAdmin(user) {
		return errors.New("only an owner can remove an owner")
	}
	if err := s.dropMember(orgID, userID, current); err != nil {
		return err
	}
	s.audit.WriteAuditLog("Organization member removed: "+userID.String(), &user.ID, nil)
	return nil
}

// AssignSite moves a Site to orgID (global admins); the previous
// organization's team grants on it go.
func (s *Service) AssignSite(orgID uuid.UUID, siteID string, user *users_models.User) error {
	if !isAdmin(user) {
		return ErrForbidden
	}
	if _, err := s.repo.org(orgID); err != nil {
		return notFound(err)
	}
	site, err := control_plane_services.GetSiteService().GetSite(siteID)
	if err != nil || site == nil {
		return ErrNotFound
	}
	previous, assigned, err := s.repo.siteOrg(siteID)
	if err != nil {
		return err
	}
	var affected map[uuid.UUID][]string
	err = storage.GetDb().Transaction(func(tx *gorm.DB) error {
		if assigned && previous != orgID {
			affected, err = s.siteGrantReach(previous, siteID)
			if err != nil {
				return err
			}
			if err := tx.Exec(
				"DELETE FROM team_grants WHERE site_id = ? AND team_id IN (SELECT id FROM teams WHERE org_id = ?)",
				siteID,
				previous,
			).Error; err != nil {
				return err
			}
		}
		if !assigned || previous != orgID {
			if err := s.checkSiteQuota(tx, orgID); err != nil {
				return err
			}
		}
		return tx.Save(&OrgSite{OrgID: orgID, SiteID: siteID}).Error
	})
	if err != nil {
		return err
	}
	invalidate(affected)
	s.audit.WriteAuditLog("Site "+siteID+" assigned to organization "+orgID.String(), &user.ID, nil)
	return nil
}

func (s *Service) ListTeams(orgID uuid.UUID, user *users_models.User) ([]*Team, error) {
	if _, err := s.access(orgID, user, false); err != nil {
		return nil, err
	}
	return s.repo.teams(orgID)
}

func (s *Service) CreateTeam(orgID uuid.UUID, name string, user *users_models.User) (*Team, error) {
	if _, err := s.access(orgID, user, true); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 200 {
		return nil, errors.New("a name of up to 200 characters is required")
	}
	team := &Team{
		ID:        uuid.New(),
		OrgID:     orgID,
		Name:      name,
		CreatedAt: time.Now().UTC(),
		Members:   []Member{},
		Grants:    []Grant{},
	}
	if err := storage.GetDb().Create(team).Error; err != nil {
		if strings.Contains(err.Error(), "idx_teams_org_name") {
			return nil, errors.New("a team with that name exists")
		}
		return nil, err
	}
	s.audit.WriteAuditLog("Team created: "+name, &user.ID, nil)
	return team, nil
}

func (s *Service) RenameTeam(orgID, teamID uuid.UUID, name string, user *users_models.User) (*Team, error) {
	if _, err := s.access(orgID, user, true); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 200 {
		return nil, errors.New("a name of up to 200 characters is required")
	}
	result := storage.GetDb().Model(&Team{}).Where("org_id = ? AND id = ?", orgID, teamID).Update("name", name)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, ErrNotFound
	}
	return s.repo.team(orgID, teamID)
}

func (s *Service) DeleteTeam(orgID, teamID uuid.UUID, user *users_models.User) error {
	if _, err := s.access(orgID, user, true); err != nil {
		return err
	}
	team, err := s.repo.team(orgID, teamID)
	if err != nil {
		return notFound(err)
	}
	if err := s.deleteTeam(teamID); err != nil {
		return err
	}
	s.audit.WriteAuditLog("Team deleted: "+team.Name, &user.ID, nil)
	return nil
}

// AddTeamMember adds a user to a team, and to the organization as a
// member if they aren't in it.
func (s *Service) AddTeamMember(orgID, teamID uuid.UUID, request MemberRequest, user *users_models.User) error {
	if _, err := s.access(orgID, user, true); err != nil {
		return err
	}
	if _, err := s.repo.team(orgID, teamID); err != nil {
		return notFound(err)
	}
	// Teams are picked from the organization's members: only the root
	// admin pulls in other users of the instance (who then join as members).
	// The same answer for unknown and outside users keeps other tenants'
	// emails from being probed.
	target, err := s.resolveUser(request)
	if err != nil {
		if isAdmin(user) {
			return err
		}
		return errNotOrgMember
	}
	if !isAdmin(user) {
		if role, err := s.repo.role(orgID, target.ID); err != nil {
			return err
		} else if role == "" {
			return errNotOrgMember
		}
	}
	if err := s.joinTeam(orgID, teamID, target.ID); err != nil {
		return err
	}
	s.audit.WriteAuditLog("Team member added: "+target.Email+" to "+teamID.String(), &user.ID, nil)
	return nil
}

func (s *Service) RemoveTeamMember(orgID, teamID, userID uuid.UUID, user *users_models.User) error {
	if _, err := s.access(orgID, user, true); err != nil {
		return err
	}
	if _, err := s.repo.team(orgID, teamID); err != nil {
		return notFound(err)
	}
	if removed, err := s.leaveTeam(teamID, userID); err != nil {
		return err
	} else if !removed {
		return ErrNotFound
	}
	s.audit.WriteAuditLog("Team member removed: "+userID.String()+" from "+teamID.String(), &user.ID, nil)
	return nil
}

// AddGrant gives a team a role on one of the organization's Sites (or a
// project of it), or - on a standalone install - on its own projects.
func (s *Service) AddGrant(orgID, teamID uuid.UUID, request GrantRequest, user *users_models.User) (*Grant, error) {
	role, err := s.access(orgID, user, true)
	if err != nil {
		return nil, err
	}
	// Site-wide ADMIN can reset local passwords and MFA on the Site: only
	// an organization owner (or the root admin) hands it to a team.
	if request.ProjectID == nil && request.Role == "ADMIN" && role != RoleOwner && !user.IsRootAdmin {
		return nil, ErrForbidden
	}
	if _, err := s.repo.team(orgID, teamID); err != nil {
		return nil, notFound(err)
	}
	if err := s.validateGrant(orgID, request); err != nil {
		return nil, err
	}
	grant := &Grant{ID: uuid.New(), TeamID: teamID, SiteID: request.SiteID, ProjectID: request.ProjectID,
		Role: request.Role, CreatedBy: &user.ID, CreatedAt: time.Now().UTC()}
	if err := storage.GetDb().Create(grant).Error; err != nil {
		if strings.Contains(err.Error(), "idx_team_grants_unique") {
			return nil, errors.New("the team already has a role there; remove it first")
		}
		return nil, err
	}
	if err := s.invalidateTeam(teamID, request.SiteID); err != nil {
		return nil, err
	}
	s.audit.WriteAuditLog(fmt.Sprintf("Team grant added: team %s, site %q, %s", teamID, request.SiteID, request.Role),
		&user.ID, request.ProjectID)
	return grant, nil
}

func (s *Service) RemoveGrant(orgID, teamID, grantID uuid.UUID, user *users_models.User) error {
	if _, err := s.access(orgID, user, true); err != nil {
		return err
	}
	var grant Grant
	if err := storage.GetDb().Where("id = ? AND team_id IN (SELECT id FROM teams WHERE org_id = ? AND id = ?)",
		grantID, orgID, teamID).First(&grant).Error; err != nil {
		return notFound(err)
	}
	if err := storage.GetDb().Delete(&Grant{}, "id = ?", grantID).Error; err != nil {
		return err
	}
	if err := s.invalidateTeam(teamID, grant.SiteID); err != nil {
		return err
	}
	s.audit.WriteAuditLog(fmt.Sprintf("Team grant removed: team %s, site %q, %s", teamID, grant.SiteID, grant.Role),
		&user.ID, grant.ProjectID)
	return nil
}

func (s *Service) validateGrant(orgID uuid.UUID, request GrantRequest) error {
	if request.ProjectID == nil {
		allowed := siteWideRoles
		if request.SiteID == LocalSite {
			allowed = localSiteRoles
		}
		if !slices.Contains(allowed, request.Role) {
			return fmt.Errorf("a site-wide role must be one of %s", strings.Join(allowed, ", "))
		}
	} else if !slices.Contains(projectRoles, request.Role) {
		return errors.New("a project role must be PROJECT_ADMIN, PROJECT_MEMBER or PROJECT_VIEWER")
	}
	if request.SiteID == LocalSite {
		if config.GetEnv().ServiceMode.IsControlPlane() {
			return errors.New("choose a Site")
		}
		if request.ProjectID != nil {
			if _, err := s.projects.GetProjectByID(*request.ProjectID); err != nil {
				return errors.New("no such project")
			}
		}
		return nil
	}
	owner, assigned, err := s.repo.siteOrg(request.SiteID)
	if err != nil {
		return err
	}
	if !assigned || owner != orgID {
		return errors.New("that Site doesn't belong to this organization")
	}
	return nil
}

// access is the caller's role in orgID; manage asks for owner or admin.
// Global admins pass either way.
func (s *Service) access(orgID uuid.UUID, user *users_models.User, manage bool) (string, error) {
	role, err := s.repo.activeRole(orgID, user.ID)
	if err != nil {
		return "", err
	}
	if isAdmin(user) {
		return role, nil
	}
	if role == "" || (manage && role != RoleOwner && role != RoleAdmin) {
		return "", ErrForbidden
	}
	return role, nil
}

func (s *Service) resolveUser(request MemberRequest) (*users_models.User, error) {
	if request.UserID != nil {
		user, err := s.users.GetUserByID(*request.UserID)
		if err != nil {
			return nil, errors.New("no such user")
		}
		return user, nil
	}
	user, err := s.users.GetUserByEmail(strings.TrimSpace(request.Email))
	if err != nil || user == nil {
		return nil, errors.New("no user with that email")
	}
	return user, nil
}

// invalidateTeam drops cached Site tokens of a team's members for siteID,
// so a changed team grant applies on the next request.
func (s *Service) invalidateTeam(teamID uuid.UUID, siteID string) error {
	if siteID == LocalSite {
		return nil
	}
	members, err := s.repo.teamMemberIDs(teamID)
	if err != nil {
		return err
	}
	affected := map[uuid.UUID][]string{}
	for _, member := range members {
		affected[member] = []string{siteID}
	}
	invalidate(affected)
	return nil
}

// teamReach: every member of a team and the Sites its grants are on.
func (s *Service) teamReach(teamID uuid.UUID) (map[uuid.UUID][]string, error) {
	members, err := s.repo.teamMemberIDs(teamID)
	if err != nil {
		return nil, err
	}
	sites, err := s.repo.teamSiteIDs(teamID)
	if err != nil {
		return nil, err
	}
	affected := map[uuid.UUID][]string{}
	for _, member := range members {
		affected[member] = sites
	}
	return affected, nil
}

func (s *Service) orgGrantReach(orgID uuid.UUID) (map[uuid.UUID][]string, error) {
	teams, err := s.repo.teams(orgID)
	if err != nil {
		return nil, err
	}
	affected := map[uuid.UUID][]string{}
	for _, team := range teams {
		reach, err := s.teamReach(team.ID)
		if err != nil {
			return nil, err
		}
		for user, sites := range reach {
			affected[user] = append(affected[user], sites...)
		}
	}
	return affected, nil
}

func (s *Service) siteGrantReach(orgID uuid.UUID, siteID string) (map[uuid.UUID][]string, error) {
	var users []uuid.UUID
	err := storage.GetDb().Raw(`SELECT DISTINCT tm.user_id FROM team_memberships tm
		JOIN team_grants g ON g.team_id = tm.team_id JOIN teams t ON t.id = g.team_id
		WHERE t.org_id = ? AND g.site_id = ?`, orgID, siteID).Scan(&users).Error
	affected := map[uuid.UUID][]string{}
	for _, user := range users {
		affected[user] = []string{siteID}
	}
	return affected, err
}

// dropMember removes a member (role: their current one) and their team
// memberships; an organization keeps at least one owner.
func (s *Service) dropMember(orgID, userID uuid.UUID, role string) error {
	sites, err := s.userSitesInOrg(orgID, userID)
	if err != nil {
		return err
	}
	err = storage.GetDb().Transaction(func(tx *gorm.DB) error {
		if role == RoleOwner {
			if owners, err := s.repo.countOwners(tx, orgID); err != nil {
				return err
			} else if owners <= 1 {
				return ErrLastOwner
			}
		}
		if err := tx.Exec(
			"DELETE FROM team_memberships WHERE user_id = ? AND team_id IN (SELECT id FROM teams WHERE org_id = ?)",
			userID,
			orgID,
		).Error; err != nil {
			return err
		}
		return tx.Delete(&Membership{}, "org_id = ? AND user_id = ?", orgID, userID).Error
	})
	if err != nil {
		return err
	}
	invalidate(map[uuid.UUID][]string{userID: sites})
	return nil
}

// joinTeam adds a user to a team, and to its organization if needed.
func (s *Service) joinTeam(orgID, teamID, userID uuid.UUID) error {
	err := storage.GetDb().Transaction(func(tx *gorm.DB) error {
		var member int64
		if err := tx.Table("organization_memberships").Where("org_id = ? AND user_id = ?", orgID, userID).
			Count(&member).Error; err != nil {
			return err
		}
		if member == 0 {
			if err := s.checkMemberQuota(tx, orgID); err != nil {
				return err
			}
		}
		if err := tx.Exec(
			"INSERT INTO organization_memberships (org_id, user_id, role) VALUES (?, ?, ?) ON CONFLICT DO NOTHING",
			orgID,
			userID,
			RoleMember,
		).Error; err != nil {
			return err
		}
		return tx.Exec("INSERT INTO team_memberships (team_id, user_id) VALUES (?, ?) ON CONFLICT DO NOTHING",
			teamID, userID).Error
	})
	if err != nil {
		return err
	}
	return s.invalidateMember(teamID, userID)
}

func (s *Service) leaveTeam(teamID, userID uuid.UUID) (bool, error) {
	result := storage.GetDb().Delete(&teamMembership{}, "team_id = ? AND user_id = ?", teamID, userID)
	if result.Error != nil || result.RowsAffected == 0 {
		return false, result.Error
	}
	return true, s.invalidateMember(teamID, userID)
}

func (s *Service) deleteTeam(teamID uuid.UUID) error {
	affected, err := s.teamReach(teamID)
	if err != nil {
		return err
	}
	if err := storage.GetDb().Delete(&Team{}, "id = ?", teamID).Error; err != nil {
		return err
	}
	invalidate(affected)
	return nil
}

func (s *Service) invalidateMember(teamID, userID uuid.UUID) error {
	sites, err := s.repo.teamSiteIDs(teamID)
	if err != nil {
		return err
	}
	invalidate(map[uuid.UUID][]string{userID: sites})
	return nil
}

func (s *Service) userSitesInOrg(orgID, userID uuid.UUID) ([]string, error) {
	var sites []string
	err := storage.GetDb().Raw(`SELECT DISTINCT g.site_id FROM team_grants g
		JOIN team_memberships tm ON tm.team_id = g.team_id JOIN teams t ON t.id = g.team_id
		WHERE t.org_id = ? AND tm.user_id = ? AND g.site_id <> ''`, orgID, userID).Scan(&sites).Error
	return sites, err
}

func invalidate(affected map[uuid.UUID][]string) {
	if !config.GetEnv().ServiceMode.IsControlPlane() {
		return
	}
	for user, sites := range affected {
		control_plane_services.GetSiteTokenService().InvalidateAllForUser(user, sites)
	}
}

func isAdmin(user *users_models.User) bool {
	return user.Role == users_enums.UserRoleAdmin
}

func notFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}
