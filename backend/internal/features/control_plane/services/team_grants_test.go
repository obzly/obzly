package control_plane_services

import (
	"testing"

	control_plane_models "obzly/internal/features/control_plane/models"
	"obzly/internal/storage"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// addTeamGrant makes a team in a fresh organization with members and gives it role on
// the Site (projectID nil: Site-wide). Returns the organization ID.
func addTeamGrant(t *testing.T, siteID string, projectID *uuid.UUID, role string, members ...uuid.UUID) uuid.UUID {
	t.Helper()
	db := storage.GetDb()
	orgID, teamID := uuid.New(), uuid.New()
	require.NoError(t, db.Exec("INSERT INTO organizations (id, name, slug) VALUES (?, ?, ?)",
		orgID, "org", "org-"+orgID.String()[:8]).Error)
	t.Cleanup(func() { db.Exec("DELETE FROM organizations WHERE id = ?", orgID) })
	require.NoError(t, db.Exec("INSERT INTO teams (id, org_id, name) VALUES (?, ?, 'team')", teamID, orgID).Error)
	for _, member := range members {
		require.NoError(t, db.Exec("INSERT INTO organization_memberships (org_id, user_id, role) VALUES (?, ?, ?)",
			orgID, member, "ORG_MEMBER").Error)
		require.NoError(t, db.Exec("INSERT INTO team_memberships (team_id, user_id) VALUES (?, ?)",
			teamID, member).Error)
	}
	require.NoError(t, db.Exec("INSERT INTO team_grants (team_id, site_id, project_id, role) VALUES (?, ?, ?, ?)",
		teamID, siteID, projectID, role).Error)
	return orgID
}

// The effective grant is the strongest of a user's own and their teams', per Site-wide
// role and per project.
func Test_ResolveEffectiveGrant_TeamGrants_StrongestWins(t *testing.T) {
	site := createTestSiteRow(t, "https://teams.example.com")
	other := createTestSiteRow(t, "https://other.example.com")
	user := createTestUserRow(t)
	outsider := createTestUserRow(t)
	projectA, projectB := uuid.New(), uuid.New()
	require.NoError(t, grantRepository.Create(&control_plane_models.SiteGrant{
		UserID: user.ID, SiteID: site.ID, Role: "VIEWER", CreatedBy: user.ID,
	}))
	require.NoError(t, grantRepository.Create(&control_plane_models.SiteGrant{
		UserID: user.ID, SiteID: site.ID, ProjectID: &projectA, Role: "PROJECT_ADMIN", CreatedBy: user.ID,
	}))
	addTeamGrant(t, site.ID, nil, "MEMBER", user.ID)
	addTeamGrant(t, site.ID, &projectA, "PROJECT_VIEWER", user.ID) // weaker than the own grant
	addTeamGrant(t, site.ID, &projectB, "PROJECT_MEMBER", user.ID)
	otherOrg := addTeamGrant(t, other.ID, nil, "ADMIN", user.ID) // another Site

	effective, err := grantService.ResolveEffectiveGrant(user.ID, site.ID)
	require.NoError(t, err)
	assert.Equal(t, "MEMBER", effective.SiteRole, "team MEMBER beats own VIEWER")
	assert.Equal(t, "PROJECT_ADMIN", effective.Projects[projectA], "a team never lowers an own grant")
	assert.Equal(t, "PROJECT_MEMBER", effective.Projects[projectB], "a team-only project")

	onOther, err := grantService.ResolveEffectiveGrant(user.ID, other.ID)
	require.NoError(t, err)
	assert.True(t, onOther.IsAdmin())

	// SCIM-suspended in that organization: its teams grant nothing.
	setSuspended := func(on bool) {
		require.NoError(t, storage.GetDb().Exec(
			"UPDATE organization_memberships SET suspended = ? WHERE org_id = ? AND user_id = ?", on, otherOrg, user.ID,
		).Error)
	}
	setSuspended(true)
	onOther, err = grantService.ResolveEffectiveGrant(user.ID, other.ID)
	require.NoError(t, err)
	assert.False(t, onOther.HasAnyAccess())
	setSuspended(false)

	none, err := grantService.ResolveEffectiveGrant(outsider.ID, site.ID)
	require.NoError(t, err)
	assert.False(t, none.HasAnyAccess())

	all, err := grantService.ListForUser(user.ID)
	require.NoError(t, err)
	sites := map[string]bool{}
	for _, grant := range all {
		sites[grant.SiteID] = true
	}
	assert.True(t, sites[other.ID], "revocation reaches Sites granted only through a team")
}

// SSO mappings resolve to the same role with teams present: teams only add, so
// SSO-synced roles stand.
func Test_SSOMappings_ResolveToTheSameRoleWithTeams(t *testing.T) {
	connector := createTestConnector(t)
	site := createTestSiteRow(t, "https://sso-teams.example.com")
	user := createTestUserRow(t)
	project := uuid.New()
	for _, m := range []struct {
		group   string
		project *uuid.UUID
		role    string
	}{
		{"admins", nil, "ADMIN"},
		{"p-members", &project, "PROJECT_MEMBER"},
	} {
		_, err := ssoSiteMappingService.Create(adminActor(), connector.ID, m.group, site.ID, m.project, m.role)
		require.NoError(t, err)
	}
	require.NoError(t, ssoSiteMappingService.SyncSSOSiteGrants(user.ID, connector.ID, []string{"admins", "p-members"}))
	before, err := grantService.ResolveEffectiveGrant(user.ID, site.ID)
	require.NoError(t, err)
	require.Equal(t, "ADMIN", before.SiteRole)
	require.Equal(t, "PROJECT_MEMBER", before.Projects[project])

	addTeamGrant(t, site.ID, nil, "VIEWER", user.ID)
	addTeamGrant(t, site.ID, &project, "PROJECT_VIEWER", user.ID)
	after, err := grantService.ResolveEffectiveGrant(user.ID, site.ID)
	require.NoError(t, err)
	assert.Equal(t, before, after)

	var mappings int64
	require.NoError(t, storage.GetDb().Raw("SELECT count(*) FROM sso_site_group_mappings WHERE connector_id = ?",
		connector.ID).Scan(&mappings).Error)
	assert.Equal(t, int64(2), mappings)
}
