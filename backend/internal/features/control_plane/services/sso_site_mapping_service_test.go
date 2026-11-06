package control_plane_services

import (
	"testing"
	"time"

	control_plane_models "obzly/internal/features/control_plane/models"
	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"
	sso_models "obzly/internal/features/users/sso/models"
	sso_repositories "obzly/internal/features/users/sso/repositories"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTestConnector(t *testing.T) *sso_models.Connector {
	t.Helper()
	connector := &sso_models.Connector{
		ID: uuid.New(), Name: "Cognito test", ProviderType: sso_models.ProviderTypeCognito,
		IssuerURL: "https://example.invalid", ClientID: "client", ClientSecretEncrypted: "x",
		Scopes: "openid email", GroupsClaim: "cognito:groups", ButtonLabel: "Sign in",
		Enabled: true, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, (&sso_repositories.ConnectorRepository{}).Create(connector))
	return connector
}

func adminActor() *users_models.User {
	return &users_models.User{ID: uuid.New(), Role: users_enums.UserRoleAdmin, IsRootAdmin: true}
}

func Test_SSOSiteMapping_Create_Validates(t *testing.T) {
	connector := createTestConnector(t)
	site := createTestSiteRow(t, "https://site.example.com")
	projectID := uuid.New()
	svc := ssoSiteMappingService

	_, err := svc.Create(adminActor(), connector.ID, "g", site.ID, &projectID, "MEMBER")
	assert.ErrorIs(t, err, ErrSSOSiteMappingRole, "a Site-wide role takes no project")
	_, err = svc.Create(adminActor(), connector.ID, "g", site.ID, nil, "PROJECT_VIEWER")
	assert.ErrorIs(t, err, ErrSSOSiteMappingRole, "a project role needs a project")
	_, err = svc.Create(adminActor(), connector.ID, "g", "no-such-site", nil, "VIEWER")
	assert.ErrorIs(t, err, ErrSSOSiteMappingSite)
	_, err = svc.Create(&users_models.User{ID: uuid.New(), Role: users_enums.UserRoleMember},
		connector.ID, "g", site.ID, nil, "VIEWER")
	assert.ErrorIs(t, err, ErrGrantForbidden)

	_, err = svc.Create(adminActor(), connector.ID, "g", site.ID, nil, "VIEWER")
	require.NoError(t, err)
	_, err = svc.Create(adminActor(), connector.ID, "g", site.ID, nil, "ADMIN")
	assert.ErrorIs(t, err, ErrSSOSiteMappingExists, "one Site-wide mapping per group and Site")
}

// Highest role per Site/project wins; group changes re-role and remove only SSO-owned
// grants; hand-made grants are untouched; cached refusals are cleared.
func Test_SSOSiteMapping_Sync(t *testing.T) {
	connector := createTestConnector(t)
	site := createTestSiteRow(t, "https://site.example.com")
	user := createTestUserRow(t)
	projectP, projectQ := uuid.New(), uuid.New()
	svc := ssoSiteMappingService

	for _, m := range []struct {
		group   string
		project *uuid.UUID
		role    string
	}{
		{"viewers", nil, "VIEWER"},
		{"members", nil, "MEMBER"},
		{"p-admins", &projectP, "PROJECT_ADMIN"},
		{"q-viewers", &projectQ, "PROJECT_VIEWER"},
	} {
		_, err := svc.Create(adminActor(), connector.ID, m.group, site.ID, m.project, m.role)
		require.NoError(t, err)
	}
	// A hand-made grant on project Q, by an admin.
	manual := &control_plane_models.SiteGrant{
		UserID: user.ID, SiteID: site.ID, ProjectID: &projectQ, Role: "PROJECT_MEMBER", CreatedBy: user.ID,
	}
	require.NoError(t, grantRepository.Create(manual))

	grantsByScope := func() map[grantScope]*control_plane_models.SiteGrant {
		t.Helper()
		grants, err := grantRepository.ListForUserAndSite(user.ID, site.ID)
		require.NoError(t, err)
		out := map[grantScope]*control_plane_models.SiteGrant{}
		for _, g := range grants {
			out[scopeOf(g.SiteID, g.ProjectID)] = g
		}
		return out
	}
	siteWide, onP, onQ := scopeOf(site.ID, nil), scopeOf(site.ID, &projectP), scopeOf(site.ID, &projectQ)

	// In every group.
	siteTokenService.rememberDenied(user.ID, site.ID, &SiteRejectedError{Status: 403, Message: "no access"})
	require.NoError(
		t,
		svc.SyncSSOSiteGrants(user.ID, connector.ID, []string{"viewers", "members", "p-admins", "q-viewers"}),
	)
	g := grantsByScope()
	require.Contains(t, g, siteWide)
	assert.Equal(t, "MEMBER", g[siteWide].Role, "highest Site-wide role across groups")
	assert.NotNil(t, g[siteWide].SSOConnectorID)
	require.Contains(t, g, onP)
	assert.Equal(t, "PROJECT_ADMIN", g[onP].Role)
	assert.Equal(t, manual.ID, g[onQ].ID, "the hand-made grant keeps its slot")
	assert.Equal(t, "PROJECT_MEMBER", g[onQ].Role)
	assert.Nil(t, g[onQ].SSOConnectorID)
	assert.Nil(t, siteTokenService.readDenied(user.ID, site.ID), "access changed: cached refusal cleared")

	// Left "members" and "p-admins": Site-wide drops to VIEWER, P goes.
	require.NoError(t, svc.SyncSSOSiteGrants(user.ID, connector.ID, []string{"viewers", "q-viewers"}))
	g = grantsByScope()
	assert.Equal(t, "VIEWER", g[siteWide].Role)
	assert.NotContains(t, g, onP)
	assert.Equal(t, "PROJECT_MEMBER", g[onQ].Role)

	// Left everything: only the hand-made grant remains.
	require.NoError(t, svc.SyncSSOSiteGrants(user.ID, connector.ID, nil))
	g = grantsByScope()
	assert.Len(t, g, 1)
	assert.Equal(t, manual.ID, g[onQ].ID)

	// The Site sees it through the normal path: effective grant + assertion.
	require.NoError(t, svc.SyncSSOSiteGrants(user.ID, connector.ID, []string{"viewers"}))
	effective, err := grantService.ResolveEffectiveGrant(user.ID, site.ID)
	require.NoError(t, err)
	assert.Equal(t, "VIEWER", effective.SiteRole)
}

// A group mapped to Site access counts as "has access" for creating the
// account at first SSO sign-in while open registration is off.
func Test_SSOSiteMapping_GroupsGrantSiteAccess(t *testing.T) {
	connector := createTestConnector(t)
	site := createTestSiteRow(t, "https://site.example.com")
	_, err := ssoSiteMappingService.Create(adminActor(), connector.ID, "site-viewers", site.ID, nil, "VIEWER")
	require.NoError(t, err)

	mapped, err := ssoSiteMappingService.GroupsGrantSiteAccess(connector.ID, []string{"x", "site-viewers"})
	require.NoError(t, err)
	assert.True(t, mapped)

	mapped, err = ssoSiteMappingService.GroupsGrantSiteAccess(connector.ID, []string{"x"})
	require.NoError(t, err)
	assert.False(t, mapped)

	mapped, err = ssoSiteMappingService.GroupsGrantSiteAccess(connector.ID, nil)
	require.NoError(t, err)
	assert.False(t, mapped)
}
