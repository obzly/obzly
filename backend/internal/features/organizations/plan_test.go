package organizations_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"obzly/internal/config"
	control_plane_controllers "obzly/internal/features/control_plane/controllers"
	control_plane_models "obzly/internal/features/control_plane/models"
	control_plane_repositories "obzly/internal/features/control_plane/repositories"
	control_plane_services "obzly/internal/features/control_plane/services"
	"obzly/internal/features/organizations"
	"obzly/internal/features/orgplan"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	"obzly/internal/storage"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Global admins set a plan, members read it, and the organization's Site
// fetches it as a document signed for it alone.
func Test_OrgPlan_AdminSets_SiteFetchesSignedDocument(t *testing.T) {
	router, base, admin := scimSetup(t)
	control_plane_controllers.GetJWKSController().RegisterRoutes(router)
	organizations.GetController().RegisterSitePlanRoutes(router)
	require.NoError(t, control_plane_services.EnsureSigningKey())
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	t.Cleanup(resetSitePlan)

	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	test_utils.MakePostRequest(t, router, base+"/members", "Bearer "+admin.Token,
		organizations.MemberRequest{Email: owner.Email, Role: organizations.RoleOwner}, http.StatusOK)
	limits := orgplan.Limits{LogsPerSecondLimit: 50, MaxLogsLifeDays: 7, MaxLogSizeKB: 8, MaxProjects: 3}
	test_utils.MakePutRequest(t, router, base+"/plan", "Bearer "+owner.Token, limits, http.StatusForbidden)
	test_utils.MakePutRequest(t, router, base+"/plan", "Bearer "+admin.Token,
		orgplan.Limits{MaxLogsLifeDays: 4000}, http.StatusBadRequest)
	test_utils.MakePutRequest(t, router, base+"/plan", "Bearer "+admin.Token, limits, http.StatusOK)
	var plan orgplan.OrgPlan
	test_utils.MakeGetRequestAndUnmarshal(t, router, base+"/plan", "Bearer "+owner.Token, http.StatusOK, &plan)
	assert.Equal(t, limits, plan.Limits)

	siteID := createSite(t)
	test_utils.MakePutRequest(t, router, base+"/sites/"+siteID, "Bearer "+admin.Token, nil, http.StatusOK)
	env := config.EnvVariables{
		SiteID:              siteID,
		ControlPlaneIssuer:  config.GetEnv().PublicURL,
		ControlPlaneJWKSURL: server.URL + "/.well-known/jwks.json",
	}
	require.NoError(t, orgplan.RefreshFromControlPlane(context.Background(), env, time.Now().UTC()))
	assert.Equal(t, limits, orgplan.Current())

	// Signed for siteID only: another Site refuses it.
	other := createSite(t)
	raw := fetchDocument(t, server.URL, siteID)
	otherEnv := env
	otherEnv.SiteID = other
	_, err := orgplan.VerifyDocument(context.Background(), otherEnv, raw, time.Now().UTC())
	require.Error(t, err)
	// And a Site the control plane doesn't know gets nothing.
	unknown := env
	unknown.SiteID = "no-such-site-" + uuid.NewString()[:6]
	require.Error(t, orgplan.RefreshFromControlPlane(context.Background(), unknown, time.Now().UTC()))
	assert.Equal(t, limits, orgplan.Current(), "the held plan stays")
}

// The control plane enforces seats - members added directly, through a
// team, or by SCIM - and Sites.
func Test_OrgPlan_MemberAndSiteQuotas(t *testing.T) {
	router, base, admin := scimSetup(t)
	scim := scimClient{t, router, createSCIMToken(t, router, base, admin.Token)}
	test_utils.MakePutRequest(t, router, base+"/plan", "Bearer "+admin.Token,
		orgplan.Limits{MaxMembers: 2, MaxSites: 1}, http.StatusOK)

	second := users_testing.CreateTestUser(users_enums.UserRoleMember)
	test_utils.MakePostRequest(t, router, base+"/members", "Bearer "+admin.Token,
		organizations.MemberRequest{Email: second.Email, Role: organizations.RoleMember}, http.StatusOK)
	test_utils.MakePostRequest(t, router, base+"/members", "Bearer "+admin.Token,
		organizations.MemberRequest{Email: second.Email, Role: organizations.RoleAdmin}, http.StatusOK)

	third := users_testing.CreateTestUser(users_enums.UserRoleMember)
	refused := test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method: "POST", URL: base + "/members", AuthToken: "Bearer " + admin.Token,
		Body:           organizations.MemberRequest{Email: third.Email, Role: organizations.RoleMember},
		ExpectedStatus: http.StatusForbidden,
	})
	assert.Contains(t, string(refused.Body), "allows 2 members")
	var team organizations.Team
	test_utils.MakePostRequestAndUnmarshal(t, router, base+"/teams", "Bearer "+admin.Token,
		organizations.TeamRequest{Name: "Quota " + uuid.NewString()[:6]}, http.StatusOK, &team)
	test_utils.MakePostRequest(t, router, base+"/teams/"+team.ID.String()+"/members", "Bearer "+admin.Token,
		map[string]string{"userId": third.UserID.String()}, http.StatusForbidden)
	scim.do("POST", "/Users", map[string]any{"userName": "quota-" + uuid.NewString()[:8] + "@example.com"}, 403)

	first, second2 := createSite(t), createSite(t)
	test_utils.MakePutRequest(t, router, base+"/sites/"+first, "Bearer "+admin.Token, nil, http.StatusOK)
	test_utils.MakePutRequest(t, router, base+"/sites/"+first, "Bearer "+admin.Token, nil, http.StatusOK)
	test_utils.MakePutRequest(t, router, base+"/sites/"+second2, "Bearer "+admin.Token, nil, http.StatusForbidden)
}

func createSite(t *testing.T) string {
	t.Helper()
	now := time.Now().UTC()
	site := &control_plane_models.Site{ID: "plan-" + uuid.NewString()[:8], Name: "Plan test",
		ApiURL: "http://unused.invalid", Enabled: true, CreatedAt: now, UpdatedAt: now}
	repository := &control_plane_repositories.SiteRepository{}
	require.NoError(t, repository.Create(site))
	t.Cleanup(func() { _ = repository.Delete(site.ID) })
	return site.ID
}

func fetchDocument(t *testing.T, serverURL, siteID string) string {
	t.Helper()
	response, err := http.Get(serverURL + "/api/v1/site-plans/" + siteID)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	require.Equal(t, http.StatusOK, response.StatusCode)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return string(body)
}

func resetSitePlan() {
	orgplan.Set(orgplan.Limits{})
	storage.GetDb().Exec("DELETE FROM site_org_plan")
}
