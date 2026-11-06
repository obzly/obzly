package organizations_test

import (
	"net/http"
	"testing"

	"obzly/internal/features/organizations"
	projects_controllers "obzly/internal/features/projects/controllers"
	projects_dto "obzly/internal/features/projects/dto"
	projects_testing "obzly/internal/features/projects/testing"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_Organizations_OwnersAdminsAndMembers(t *testing.T) {
	router := projects_testing.CreateTestRouter(projects_controllers.GetProjectController(),
		projects_controllers.GetMembershipController(), organizations.GetController())
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	orgAdmin := users_testing.CreateTestUser(users_enums.UserRoleMember)
	member := users_testing.CreateTestUser(users_enums.UserRoleMember)
	newcomer := users_testing.CreateTestUser(users_enums.UserRoleMember)

	test_utils.MakePostRequest(t, router, "/api/v1/orgs", "Bearer "+member.Token,
		organizations.OrgRequest{Name: "Acme", Slug: "acme-x"}, http.StatusForbidden)
	slug := "acme-" + uuid.NewString()[:8]
	var org organizations.Organization
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/orgs", "Bearer "+admin.Token,
		organizations.OrgRequest{Name: "Acme", Slug: slug}, http.StatusOK, &org)
	assert.Equal(t, organizations.RoleOwner, org.MyRole)
	test_utils.MakePostRequest(t, router, "/api/v1/orgs", "Bearer "+admin.Token,
		organizations.OrgRequest{Name: "Again", Slug: slug}, http.StatusBadRequest)
	base := "/api/v1/orgs/" + org.ID.String()

	set := func(token string, userID uuid.UUID, role string, status int) {
		test_utils.MakePostRequest(t, router, base+"/members", "Bearer "+token,
			organizations.MemberRequest{UserID: &userID, Role: role}, status)
	}
	set(admin.Token, orgAdmin.UserID, organizations.RoleAdmin, http.StatusOK)
	set(admin.Token, member.UserID, organizations.RoleMember, http.StatusOK)
	set(member.Token, newcomer.UserID, organizations.RoleMember, http.StatusForbidden)
	set(orgAdmin.Token, newcomer.UserID, organizations.RoleOwner, http.StatusBadRequest) // only owners make owners
	set(orgAdmin.Token, newcomer.UserID, organizations.RoleMember, http.StatusOK)
	set(admin.Token, admin.UserID, organizations.RoleMember, http.StatusBadRequest) // the last owner stays

	// Teams: org admins manage; members read.
	test_utils.MakePostRequest(t, router, base+"/teams", "Bearer "+member.Token,
		organizations.TeamRequest{Name: "sre"}, http.StatusForbidden)
	var team organizations.Team
	test_utils.MakePostRequestAndUnmarshal(t, router, base+"/teams", "Bearer "+orgAdmin.Token,
		organizations.TeamRequest{Name: "SRE"}, http.StatusOK, &team)
	test_utils.MakePostRequest(t, router, base+"/teams", "Bearer "+orgAdmin.Token,
		organizations.TeamRequest{Name: "sre"}, http.StatusBadRequest)
	test_utils.MakePostRequest(t, router, base+"/teams/"+team.ID.String()+"/members", "Bearer "+orgAdmin.Token,
		organizations.MemberRequest{Email: member.Email}, http.StatusOK)
	var teams []organizations.Team
	test_utils.MakeGetRequestAndUnmarshal(t, router, base+"/teams", "Bearer "+member.Token, http.StatusOK, &teams)
	require.Len(t, teams, 1)
	require.Len(t, teams[0].Members, 1)

	// Org admins pick team members from the organization only, and an
	// outside user looks the same as an unknown email.
	outsider := users_testing.CreateTestUser(users_enums.UserRoleMember)
	outside := test_utils.MakePostRequest(t, router, base+"/teams/"+team.ID.String()+"/members",
		"Bearer "+orgAdmin.Token, organizations.MemberRequest{Email: outsider.Email}, http.StatusBadRequest)
	unknown := test_utils.MakePostRequest(t, router, base+"/teams/"+team.ID.String()+"/members",
		"Bearer "+orgAdmin.Token, organizations.MemberRequest{Email: "nobody-" + uuid.NewString() + "@example.com"},
		http.StatusBadRequest)
	assert.Equal(t, string(unknown.Body), string(outside.Body))
	test_utils.MakePostRequest(t, router, base+"/teams/"+team.ID.String()+"/members", "Bearer "+orgAdmin.Token,
		organizations.MemberRequest{UserID: &outsider.UserID}, http.StatusBadRequest)
	test_utils.MakeGetRequest(t, router, base, "Bearer "+outsider.Token, http.StatusForbidden)

	// Removing a member takes them off the organization's teams.
	test_utils.MakeDeleteRequest(t, router, base+"/members/"+member.UserID.String(), "Bearer "+orgAdmin.Token,
		http.StatusOK)
	test_utils.MakeGetRequestAndUnmarshal(t, router, base+"/teams", "Bearer "+orgAdmin.Token, http.StatusOK, &teams)
	assert.Empty(t, teams[0].Members)
	test_utils.MakeGetRequest(t, router, base, "Bearer "+member.Token, http.StatusForbidden)

	// Listing: members see their organizations, admins all.
	var mine []organizations.Organization
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/orgs", "Bearer "+orgAdmin.Token, http.StatusOK, &mine)
	found := false
	for _, o := range mine {
		found = found || o.ID == org.ID
	}
	assert.True(t, found)

	test_utils.MakeDeleteRequest(t, router, base, "Bearer "+orgAdmin.Token, http.StatusForbidden)
	test_utils.MakeDeleteRequest(t, router, base, "Bearer "+admin.Token, http.StatusOK)
}

// On a standalone install a team's grants on its own projects (site "")
// give access: the strongest role wins, a site-wide grant covers every
// project, and leaving the team takes it away.
func Test_TeamGrants_GiveLocalProjectAccess(t *testing.T) {
	router := projects_testing.CreateTestRouter(projects_controllers.GetProjectController(),
		projects_controllers.GetMembershipController(), organizations.GetController())
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	engineer := users_testing.CreateTestUser(users_enums.UserRoleMember)
	projectA, _ := projects_testing.CreateTestProjectWithToken("Teams A", owner.Token, router)
	projectB, _ := projects_testing.CreateTestProjectWithToken("Teams B", owner.Token, router)
	projects_testing.AddMemberToProject(projectA, engineer, users_enums.ProjectRoleViewer, owner.Token, router)

	var org organizations.Organization
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/orgs", "Bearer "+admin.Token,
		organizations.OrgRequest{Name: "Local", Slug: "local-" + uuid.NewString()[:8]}, http.StatusOK, &org)
	base := "/api/v1/orgs/" + org.ID.String()
	t.Cleanup(func() { test_utils.MakeDeleteRequest(t, router, base, "Bearer "+admin.Token, http.StatusOK) })
	var team organizations.Team
	test_utils.MakePostRequestAndUnmarshal(t, router, base+"/teams", "Bearer "+admin.Token,
		organizations.TeamRequest{Name: "Backend"}, http.StatusOK, &team)
	teamBase := base + "/teams/" + team.ID.String()
	test_utils.MakePostRequest(t, router, teamBase+"/members", "Bearer "+admin.Token,
		organizations.MemberRequest{UserID: &engineer.UserID}, http.StatusOK)

	roleOn := func(projectID uuid.UUID) string {
		var listed projects_dto.ListProjectsResponseDTO
		test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/projects", "Bearer "+engineer.Token,
			http.StatusOK, &listed)
		for _, project := range listed.Projects {
			if project.ID == projectID && project.UserRole != nil {
				return string(*project.UserRole)
			}
		}
		return ""
	}
	require.Equal(t, "PROJECT_VIEWER", roleOn(projectA.ID))
	require.Equal(t, "", roleOn(projectB.ID))

	var grant organizations.Grant
	test_utils.MakePostRequestAndUnmarshal(t, router, teamBase+"/grants", "Bearer "+admin.Token,
		organizations.GrantRequest{ProjectID: &projectA.ID, Role: "PROJECT_ADMIN"}, http.StatusOK, &grant)
	assert.Equal(t, "PROJECT_ADMIN", roleOn(projectA.ID), "the team's stronger role wins")
	test_utils.MakePostRequest(t, router, teamBase+"/grants", "Bearer "+admin.Token,
		organizations.GrantRequest{Role: "MEMBER"}, http.StatusOK)
	assert.Equal(t, "PROJECT_MEMBER", roleOn(projectB.ID), "site-wide: every project")
	test_utils.MakeGetRequest(t, router, "/api/v1/projects/"+projectB.ID.String(), "Bearer "+engineer.Token,
		http.StatusOK)
	test_utils.MakePostRequest(t, router, teamBase+"/grants", "Bearer "+admin.Token,
		organizations.GrantRequest{Role: "ADMIN"}, http.StatusBadRequest) // no instance-wide admin by team
	test_utils.MakePostRequest(t, router, teamBase+"/grants", "Bearer "+admin.Token,
		organizations.GrantRequest{SiteID: "elsewhere", Role: "MEMBER"}, http.StatusBadRequest)

	// The owner stays owner whatever a team says.
	test_utils.MakePostRequest(t, router, teamBase+"/members", "Bearer "+admin.Token,
		organizations.MemberRequest{UserID: &owner.UserID}, http.StatusOK)
	var ownerList projects_dto.ListProjectsResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t,
		router,
		"/api/v1/projects",
		"Bearer "+owner.Token,
		http.StatusOK,
		&ownerList,
	)
	for _, project := range ownerList.Projects {
		if project.ID == projectA.ID {
			require.NotNil(t, project.UserRole)
			assert.Equal(t, users_enums.ProjectRoleOwner, *project.UserRole)
		}
	}

	test_utils.MakeDeleteRequest(t, router, teamBase+"/members/"+engineer.UserID.String(), "Bearer "+admin.Token,
		http.StatusOK)
	assert.Equal(t, "PROJECT_VIEWER", roleOn(projectA.ID), "back to the own membership")
	assert.Equal(t, "", roleOn(projectB.ID))
	test_utils.MakeGetRequest(t, router, "/api/v1/projects/"+projectB.ID.String(), "Bearer "+engineer.Token,
		http.StatusForbidden)
}

// A Site-wide ADMIN team grant lets the team reset local passwords and MFA
// on that Site, so only an organization owner may add one - not an
// organization admin, nor a global admin who isn't an owner.
func Test_AddGrant_SiteWideAdmin_OnlyOrganizationOwner(t *testing.T) {
	router := projects_testing.CreateTestRouter(projects_controllers.GetProjectController(),
		projects_controllers.GetMembershipController(), organizations.GetController())
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	otherAdmin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	orgAdmin := users_testing.CreateTestUser(users_enums.UserRoleMember)
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)

	var org organizations.Organization
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/orgs", "Bearer "+admin.Token,
		organizations.OrgRequest{Name: "Fabrikam", Slug: "fabrikam-" + uuid.NewString()[:8]}, http.StatusOK, &org)
	base := "/api/v1/orgs/" + org.ID.String()
	test_utils.MakePostRequest(t, router, base+"/members", "Bearer "+admin.Token,
		organizations.MemberRequest{UserID: &orgAdmin.UserID, Role: organizations.RoleAdmin}, http.StatusOK)
	test_utils.MakePostRequest(t, router, base+"/members", "Bearer "+admin.Token,
		organizations.MemberRequest{UserID: &owner.UserID, Role: organizations.RoleOwner}, http.StatusOK)
	var team organizations.Team
	test_utils.MakePostRequestAndUnmarshal(t, router, base+"/teams", "Bearer "+orgAdmin.Token,
		organizations.TeamRequest{Name: "Platform"}, http.StatusOK, &team)

	grant := organizations.GrantRequest{SiteID: "eu-" + uuid.NewString()[:8], Role: "ADMIN"}
	url := base + "/teams/" + team.ID.String() + "/grants"
	test_utils.MakePostRequest(t, router, url, "Bearer "+orgAdmin.Token, grant, http.StatusForbidden)
	test_utils.MakePostRequest(t, router, url, "Bearer "+otherAdmin.Token, grant, http.StatusForbidden)
	// Past the permission check (the Site itself isn't this organization's).
	resp := test_utils.MakePostRequest(t, router, url, "Bearer "+owner.Token, grant, http.StatusBadRequest)
	assert.NotContains(t, string(resp.Body), "insufficient permissions")
}
