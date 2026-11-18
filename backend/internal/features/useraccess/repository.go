package useraccess

import (
	"obzly/internal/storage"

	"github.com/google/uuid"
)

// repository reads one user's sign-in and access rows; every query is by
// user_id on an indexed column, so cost grows with that user's rows only.
type repository struct{}

type ssoIdentityRow struct {
	Name string `gorm:"column:name"`
}

type orgRow struct {
	OrgID     uuid.UUID `gorm:"column:org_id"`
	Name      string    `gorm:"column:name"`
	Role      string    `gorm:"column:role"`
	Suspended bool      `gorm:"column:suspended"`
}

type teamRow struct {
	OrgID uuid.UUID `gorm:"column:org_id"`
	Name  string    `gorm:"column:name"`
}

type siteGrantRow struct {
	SiteID    string     `gorm:"column:site_id"`
	SiteName  string     `gorm:"column:site_name"`
	ProjectID *uuid.UUID `gorm:"column:project_id"`
	Role      string     `gorm:"column:role"`
	ViaSSO    bool       `gorm:"column:via_sso"`
}

type teamGrantRow struct {
	SiteID      string     `gorm:"column:site_id"`
	SiteName    string     `gorm:"column:site_name"`
	ProjectID   *uuid.UUID `gorm:"column:project_id"`
	ProjectName string     `gorm:"column:project_name"`
	Role        string     `gorm:"column:role"`
	Team        string     `gorm:"column:team"`
	Org         string     `gorm:"column:org"`
}

type membershipRow struct {
	ProjectID      uuid.UUID `gorm:"column:project_id"`
	ProjectName    string    `gorm:"column:project_name"`
	Role           string    `gorm:"column:role"`
	ViaSSO         bool      `gorm:"column:via_sso"`
	CentralManaged bool      `gorm:"column:central_managed"`
}

func (r *repository) ssoConnectors(userID uuid.UUID) ([]ssoIdentityRow, error) {
	var rows []ssoIdentityRow
	err := storage.GetDb().Raw(`SELECT c.name FROM user_sso_identities i
		JOIN oauth_connectors c ON c.id = i.connector_id
		WHERE i.user_id = ? ORDER BY lower(c.name)`, userID).Scan(&rows).Error
	return rows, err
}

func (r *repository) orgName(orgID uuid.UUID) (string, error) {
	var names []string
	err := storage.GetDb().Raw("SELECT name FROM organizations WHERE id = ?", orgID).Scan(&names).Error
	if err != nil || len(names) == 0 {
		return "", err
	}
	return names[0], nil
}

func (r *repository) orgs(userID uuid.UUID) ([]orgRow, error) {
	var rows []orgRow
	err := storage.GetDb().Raw(`SELECT m.org_id, o.name, m.role, m.suspended FROM organization_memberships m
		JOIN organizations o ON o.id = m.org_id
		WHERE m.user_id = ? ORDER BY lower(o.name)`, userID).Scan(&rows).Error
	return rows, err
}

func (r *repository) teams(userID uuid.UUID) ([]teamRow, error) {
	var rows []teamRow
	err := storage.GetDb().Raw(`SELECT t.org_id, t.name FROM team_memberships tm
		JOIN teams t ON t.id = tm.team_id
		WHERE tm.user_id = ? ORDER BY lower(t.name)`, userID).Scan(&rows).Error
	return rows, err
}

// siteGrants is the user's own control-plane grants.
func (r *repository) siteGrants(userID uuid.UUID) ([]siteGrantRow, error) {
	var rows []siteGrantRow
	err := storage.GetDb().Raw(`SELECT g.site_id, COALESCE(NULLIF(s.display_name, ''), s.name, g.site_id) AS site_name,
			g.project_id, g.role, g.sso_connector_id IS NOT NULL AS via_sso
		FROM site_grants g LEFT JOIN sites s ON s.id = g.site_id
		WHERE g.user_id = ? ORDER BY g.site_id, g.project_id NULLS FIRST`, userID).Scan(&rows).Error
	return rows, err
}

// teamGrants is what the user's teams give, on Sites and (site ”) this
// instance's projects; a suspended membership gives nothing, as in
// control_plane GrantRepository.TeamGrants.
func (r *repository) teamGrants(userID uuid.UUID) ([]teamGrantRow, error) {
	var rows []teamGrantRow
	err := storage.GetDb().Raw(`SELECT g.site_id, COALESCE(NULLIF(s.display_name, ''), s.name, g.site_id) AS site_name,
			g.project_id, COALESCE(p.name, '') AS project_name, g.role, t.name AS team, o.name AS org
		FROM team_memberships tm
		JOIN team_grants g ON g.team_id = tm.team_id
		JOIN teams t ON t.id = g.team_id
		JOIN organizations o ON o.id = t.org_id
		JOIN organization_memberships om ON om.org_id = t.org_id AND om.user_id = tm.user_id
		LEFT JOIN sites s ON s.id = g.site_id
		LEFT JOIN projects p ON g.site_id = '' AND p.id = g.project_id
		WHERE tm.user_id = ? AND NOT om.suspended
		ORDER BY g.site_id, g.project_id NULLS FIRST, lower(t.name)`, userID).Scan(&rows).Error
	return rows, err
}

// projectMemberships is the user's roles on this instance's own projects.
func (r *repository) projectMemberships(userID uuid.UUID) ([]membershipRow, error) {
	var rows []membershipRow
	err := storage.GetDb().Raw(`SELECT pm.project_id, p.name AS project_name, pm.role,
			pm.sso_connector_id IS NOT NULL AS via_sso, pm.central_managed
		FROM project_memberships pm JOIN projects p ON p.id = pm.project_id
		WHERE pm.user_id = ? ORDER BY lower(p.name)`, userID).Scan(&rows).Error
	return rows, err
}
