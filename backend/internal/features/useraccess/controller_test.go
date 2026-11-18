package useraccess

import (
	"net/http"
	"testing"

	users_enums "obzly/internal/features/users/enums"
	users_middleware "obzly/internal/features/users/middleware"
	users_services "obzly/internal/features/users/services"
	users_testing "obzly/internal/features/users/testing"
	"obzly/internal/storage"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_GetUserDetail_WhenRequesterIsNotAdmin_ReturnsForbidden(t *testing.T) {
	router := createTestRouter()
	member := users_testing.CreateTestUser(users_enums.UserRoleMember)
	other := users_testing.CreateTestUser(users_enums.UserRoleMember)

	test_utils.MakeGetRequest(t, router, "/api/v1/users/"+other.UserID.String()+"/detail",
		"Bearer "+member.Token, http.StatusForbidden)
	// Not even their own: the panel is for admins.
	test_utils.MakeGetRequest(t, router, "/api/v1/users/"+member.UserID.String()+"/detail",
		"Bearer "+member.Token, http.StatusForbidden)
}

func Test_GetUserDetail_WhenUserIsUnknownOrIDInvalid_ReturnsNotFoundOrBadRequest(t *testing.T) {
	router := createTestRouter()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	test_utils.MakeGetRequest(t, router, "/api/v1/users/"+uuid.NewString()+"/detail",
		"Bearer "+admin.Token, http.StatusNotFound)
	test_utils.MakeGetRequest(t, router, "/api/v1/users/not-a-uuid/detail",
		"Bearer "+admin.Token, http.StatusBadRequest)
}

func Test_GetUserDetail_WhenUserHasSignInsOrgsAndGrants_ReturnsThemAll(t *testing.T) {
	router := createTestRouter()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	target := users_testing.CreateTestUser(users_enums.UserRoleMember)
	db := storage.GetDb()
	suffix := uuid.NewString()[:8]

	// An SSO identity (disabled connector: other packages list enabled ones).
	connectorID := uuid.New()
	require.NoError(t, db.Exec(`INSERT INTO oauth_connectors
		(id, name, provider_type, issuer_url, client_id, client_secret_encrypted, button_label, enabled)
		VALUES (?, ?, 'okta', 'https://idp.example.com', 'client', 'x', 'Okta', false)`,
		connectorID, "Okta "+suffix).Error)
	t.Cleanup(func() { db.Exec("DELETE FROM oauth_connectors WHERE id = ?", connectorID) })
	require.NoError(t, db.Exec("INSERT INTO user_sso_identities (user_id, connector_id, subject) VALUES (?, ?, ?)",
		target.UserID, connectorID, "sub-"+suffix).Error)

	// An organization with a team that has a Site-wide role, and a direct project grant.
	siteID := "useraccess-" + suffix
	require.NoError(t, db.Exec("INSERT INTO sites (id, name, api_url) VALUES (?, ?, ?)",
		siteID, "Site "+suffix, "https://"+siteID+".example.com").Error)
	t.Cleanup(func() { db.Exec("DELETE FROM sites WHERE id = ?", siteID) })
	orgID, teamID := uuid.New(), uuid.New()
	require.NoError(t, db.Exec("INSERT INTO organizations (id, name, slug) VALUES (?, ?, ?)",
		orgID, "Contoso "+suffix, "contoso-"+suffix).Error)
	t.Cleanup(func() { db.Exec("DELETE FROM organizations WHERE id = ?", orgID) })
	require.NoError(t, db.Exec("INSERT INTO organization_memberships (org_id, user_id, role) VALUES (?, ?, 'ORG_ADMIN')",
		orgID, target.UserID).Error)
	require.NoError(t, db.Exec("INSERT INTO teams (id, org_id, name) VALUES (?, ?, 'Platform')", teamID, orgID).Error)
	require.NoError(t, db.Exec("INSERT INTO team_memberships (team_id, user_id) VALUES (?, ?)",
		teamID, target.UserID).Error)
	require.NoError(t, db.Exec("INSERT INTO organization_sites (org_id, site_id) VALUES (?, ?)", orgID, siteID).Error)
	require.NoError(t, db.Exec("INSERT INTO team_grants (team_id, site_id, role) VALUES (?, ?, 'VIEWER')",
		teamID, siteID).Error)
	grantProject := uuid.New()
	require.NoError(t, db.Exec(`INSERT INTO site_grants (user_id, site_id, project_id, role, created_by)
		VALUES (?, ?, ?, 'PROJECT_MEMBER', ?)`, target.UserID, siteID, grantProject, admin.UserID).Error)

	// A role on one of this instance's own projects.
	projectID := uuid.New()
	require.NoError(t, db.Exec("INSERT INTO projects (id, name) VALUES (?, ?)", projectID, "Shop "+suffix).Error)
	t.Cleanup(func() { db.Exec("DELETE FROM projects WHERE id = ?", projectID) })
	require.NoError(t, db.Exec("INSERT INTO project_memberships (user_id, project_id, role) VALUES (?, ?, 'PROJECT_MEMBER')",
		target.UserID, projectID).Error)

	var detail UserDetail
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/users/"+target.UserID.String()+"/detail",
		"Bearer "+admin.Token, http.StatusOK, &detail)

	assert.Equal(t, target.Email, detail.Email)
	assert.True(t, detail.IsActive)
	assert.False(t, detail.ProjectRolesOnSites, "tests run as a standalone install")
	assert.Equal(t, []SignInMethod{{Kind: "password"}, {Kind: "sso", Name: "Okta " + suffix}}, detail.SignInMethods)
	assert.Nil(t, detail.ProvisionedBy)

	require.Len(t, detail.Organizations, 1)
	assert.Equal(t, "Contoso "+suffix, detail.Organizations[0].Name)
	assert.Equal(t, "ORG_ADMIN", detail.Organizations[0].Role)
	assert.Equal(t, []string{"Platform"}, detail.Organizations[0].Teams)

	require.Len(t, detail.SiteAccess, 2)
	direct, team := detail.SiteAccess[0], detail.SiteAccess[1]
	if direct.Via != "direct" {
		direct, team = team, direct
	}
	assert.Equal(t, SiteAccess{SiteID: siteID, SiteName: "Site " + suffix, ProjectID: &grantProject,
		Role: "PROJECT_MEMBER", Via: "direct"}, direct)
	assert.Equal(t, SiteAccess{SiteID: siteID, SiteName: "Site " + suffix, Role: "VIEWER", Via: "team",
		Team: "Platform", Org: "Contoso " + suffix}, team)

	require.Len(t, detail.ProjectRoles, 1)
	assert.Equal(t, "Shop "+suffix, detail.ProjectRoles[0].ProjectName)
	assert.Equal(t, "PROJECT_MEMBER", detail.ProjectRoles[0].Role)
	assert.Equal(t, "direct", detail.ProjectRoles[0].Via)

	// A suspended membership (SCIM deprovisioning) gives no team access.
	require.NoError(t, db.Exec("UPDATE organization_memberships SET suspended = true WHERE org_id = ? AND user_id = ?",
		orgID, target.UserID).Error)
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/users/"+target.UserID.String()+"/detail",
		"Bearer "+admin.Token, http.StatusOK, &detail)
	require.Len(t, detail.SiteAccess, 1)
	assert.Equal(t, "direct", detail.SiteAccess[0].Via)
	assert.True(t, detail.Organizations[0].Suspended)
}

func createTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	protected := router.Group("/api/v1")
	protected.Use(users_middleware.AuthMiddleware(users_services.GetUserService()))
	GetController().RegisterRoutes(protected)
	return router
}
