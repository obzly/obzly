package organizations

import (
	"errors"

	"obzly/internal/storage"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type repository struct{}

func (r *repository) orgs() ([]*Organization, error) {
	var orgs []*Organization
	err := storage.GetDb().Order("name").Find(&orgs).Error
	return orgs, err
}

func (r *repository) orgsOf(userID uuid.UUID) ([]*Organization, map[uuid.UUID]string, error) {
	var memberships []Membership
	if err := storage.GetDb().Where("user_id = ?", userID).Find(&memberships).Error; err != nil {
		return nil, nil, err
	}
	roles := map[uuid.UUID]string{}
	ids := make([]uuid.UUID, 0, len(memberships))
	for _, membership := range memberships {
		roles[membership.OrgID] = membership.Role
		ids = append(ids, membership.OrgID)
	}
	if len(ids) == 0 {
		return []*Organization{}, roles, nil
	}
	var orgs []*Organization
	err := storage.GetDb().Where("id IN ?", ids).Order("name").Find(&orgs).Error
	return orgs, roles, err
}

func (r *repository) org(orgID uuid.UUID) (*Organization, error) {
	var org Organization
	err := storage.GetDb().Where("id = ?", orgID).First(&org).Error
	return &org, err
}

func (r *repository) role(orgID, userID uuid.UUID) (string, error) {
	var membership Membership
	err := storage.GetDb().Where("org_id = ? AND user_id = ?", orgID, userID).First(&membership).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	return membership.Role, err
}

// activeRole is role, but "" for a suspended member: deprovisioning
// takes the organization away, not just its teams' grants.
func (r *repository) activeRole(orgID, userID uuid.UUID) (string, error) {
	var membership Membership
	err := storage.GetDb().Where("org_id = ? AND user_id = ? AND NOT suspended", orgID, userID).
		First(&membership).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	return membership.Role, err
}

func (r *repository) members(orgID uuid.UUID) ([]Membership, error) {
	var members []Membership
	err := storage.GetDb().Table("organization_memberships m").
		Select("m.org_id, m.user_id, m.role, m.created_at, m.external_id, m.suspended, u.email, u.name").
		Joins("JOIN users u ON u.id = m.user_id").
		Where("m.org_id = ?", orgID).Order("u.email").Scan(&members).Error
	return members, err
}

func (r *repository) countOwners(tx *gorm.DB, orgID uuid.UUID) (int64, error) {
	var count int64
	err := tx.Model(&Membership{}).Where("org_id = ? AND role = ?", orgID, RoleOwner).Count(&count).Error
	return count, err
}

func (r *repository) sites(orgID uuid.UUID) ([]string, error) {
	var sites []string
	err := storage.GetDb().Model(&OrgSite{}).Where("org_id = ?", orgID).Order("site_id").Pluck("site_id", &sites).Error
	return sites, err
}

func (r *repository) siteOrg(siteID string) (uuid.UUID, bool, error) {
	var orgSite OrgSite
	err := storage.GetDb().Where("site_id = ?", siteID).First(&orgSite).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return uuid.Nil, false, nil
	}
	return orgSite.OrgID, err == nil, err
}

func (r *repository) teams(orgID uuid.UUID) ([]*Team, error) {
	var teams []*Team
	if err := storage.GetDb().Where("org_id = ?", orgID).Order("lower(name)").Find(&teams).Error; err != nil {
		return nil, err
	}
	return teams, r.fillTeams(teams)
}

// fillTeams loads every team's members and grants in two queries.
func (r *repository) fillTeams(teams []*Team) error {
	if len(teams) == 0 {
		return nil
	}
	byID := make(map[uuid.UUID]*Team, len(teams))
	ids := make([]uuid.UUID, 0, len(teams))
	for _, team := range teams {
		team.Members, team.Grants = []Member{}, []Grant{}
		byID[team.ID] = team
		ids = append(ids, team.ID)
	}
	var members []struct {
		TeamID uuid.UUID `gorm:"column:team_id"`
		Member
	}
	if err := storage.GetDb().Table("team_memberships tm").
		Select("tm.team_id, tm.user_id, u.email, u.name").Joins("JOIN users u ON u.id = tm.user_id").
		Where("tm.team_id IN ?", ids).Order("u.email").Scan(&members).Error; err != nil {
		return err
	}
	for _, member := range members {
		byID[member.TeamID].Members = append(byID[member.TeamID].Members, member.Member)
	}
	var grants []Grant
	if err := storage.GetDb().Where("team_id IN ?", ids).Order("site_id, created_at").Find(&grants).Error; err != nil {
		return err
	}
	for _, grant := range grants {
		byID[grant.TeamID].Grants = append(byID[grant.TeamID].Grants, grant)
	}
	return nil
}

func (r *repository) team(orgID, teamID uuid.UUID) (*Team, error) {
	var team Team
	if err := storage.GetDb().Where("org_id = ? AND id = ?", orgID, teamID).First(&team).Error; err != nil {
		return nil, err
	}
	return &team, r.fillTeam(&team)
}

func (r *repository) fillTeam(team *Team) error {
	return r.fillTeams([]*Team{team})
}

// teamMemberIDs is who team grants reach (for token invalidation).
func (r *repository) teamMemberIDs(teamID uuid.UUID) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := storage.GetDb().Model(&teamMembership{}).Where("team_id = ?", teamID).Pluck("user_id", &ids).Error
	return ids, err
}

func (r *repository) teamSiteIDs(teamID uuid.UUID) ([]string, error) {
	var sites []string
	err := storage.GetDb().Model(&Grant{}).Where("team_id = ? AND site_id <> ''", teamID).
		Distinct("site_id").Pluck("site_id", &sites).Error
	return sites, err
}
