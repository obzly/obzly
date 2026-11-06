package organizations_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"obzly/internal/features/organizations"
	projects_controllers "obzly/internal/features/projects/controllers"
	projects_dto "obzly/internal/features/projects/dto"
	projects_testing "obzly/internal/features/projects/testing"
	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"
	users_repositories "obzly/internal/features/users/repositories"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type scimClient struct {
	t      *testing.T
	router *gin.Engine
	token  string
}

// Okta's SCIM 2.0 test sequence: look up by userName, create, read,
// deactivate and reactivate with a pathless PATCH, update by PUT, then
// push a group (create, add, rename, remove member, delete).
func Test_SCIM_OktaSequence_ProvisionsAndDeprovisions(t *testing.T) {
	router, base, admin := scimSetup(t)
	scim := scimClient{t, router, createSCIMToken(t, router, base, admin.Token)}
	email := "okta-" + uuid.NewString()[:8] + "@example.com"

	list := scim.do(
		"GET",
		"/Users?filter="+url.QueryEscape(`userName eq "`+email+`"`)+"&startIndex=1&count=100",
		nil,
		200,
	)
	assert.Equal(t, float64(0), list["totalResults"])

	created := scim.do("POST", "/Users", json.RawMessage(`{
		"schemas": ["urn:ietf:params:scim:schemas:core:2.0:User"],
		"userName": "`+email+`",
		"name": {"givenName": "Ada", "familyName": "Lovelace"},
		"emails": [{"primary": true, "value": "`+email+`", "type": "work"}],
		"displayName": "Ada Lovelace", "locale": "en-US", "externalId": "00u1okta",
		"groups": [], "password": "ignored-Pa55", "active": true}`), 201)
	id := created["id"].(string)
	assert.Equal(t, email, created["userName"])
	assert.Equal(t, true, created["active"])
	assert.Equal(t, "00u1okta", created["externalId"])
	account := getAccount(t, id)
	assert.False(t, account.HasPassword(), "SCIM accounts sign in with SSO")
	assert.Equal(t, users_enums.UserStatusActive, account.Status)
	require.NotNil(t, account.SCIMOrgID)

	assert.Equal(t, email, scim.do("GET", "/Users/"+id, nil, 200)["userName"])
	list = scim.do("GET", "/Users?filter="+url.QueryEscape(`userName eq "`+email+`"`), nil, 200)
	assert.Equal(t, float64(1), list["totalResults"])

	patched := scim.do("PATCH", "/Users/"+id, json.RawMessage(`{
		"schemas": ["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
		"Operations": [{"op": "replace", "value": {"active": false}}]}`), 200)
	assert.Equal(t, false, patched["active"])
	assert.Equal(t, users_enums.UserStatusInactive, getAccount(t, id).Status, "owned account deactivated")
	scim.do(
		"PATCH",
		"/Users/"+id,
		json.RawMessage(`{"Operations": [{"op": "replace", "value": {"active": true}}]}`),
		200,
	)
	assert.Equal(t, users_enums.UserStatusActive, getAccount(t, id).Status)

	replaced := scim.do("PUT", "/Users/"+id, json.RawMessage(`{
		"schemas": ["urn:ietf:params:scim:schemas:core:2.0:User"], "id": "`+id+`",
		"userName": "`+email+`", "name": {"givenName": "Augusta", "familyName": "King"},
		"emails": [{"primary": true, "value": "`+email+`", "type": "work"}], "active": true}`), 200)
	assert.Equal(t, "Augusta King", replaced["displayName"])

	group := scim.do("POST", "/Groups", json.RawMessage(`{
		"schemas": ["urn:ietf:params:scim:schemas:core:2.0:Group"], "displayName": "Okta Engineers", "members": []}`), 201)
	groupID := group["id"].(string)
	group = scim.do("PATCH", "/Groups/"+groupID, json.RawMessage(`{
		"schemas": ["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
		"Operations": [{"op": "add", "path": "members", "value": [{"value": "`+id+`", "display": "`+email+`"}]}]}`), 200)
	assert.Len(t, group["members"], 1)
	group = scim.do("PATCH", "/Groups/"+groupID, json.RawMessage(`{
		"Operations": [{"op": "replace", "value": {"id": "`+groupID+`", "displayName": "Okta Eng"}}]}`), 200)
	assert.Equal(t, "Okta Eng", group["displayName"])
	assert.Len(t, group["members"], 1, "a rename keeps members")
	group = scim.do("PATCH", "/Groups/"+groupID, json.RawMessage(`{
		"Operations": [{"op": "remove", "path": "members[value eq \"`+id+`\"]"}]}`), 200)
	assert.Empty(t, group["members"])
	scim.do("DELETE", "/Groups/"+groupID, nil, 204)
	scim.do("GET", "/Groups/"+groupID, nil, 404)
}

// Microsoft Entra's provisioning validator: existence checks by userName
// and externalId, a duplicate create (409), capitalised ops, string
// booleans, an emails path, groups with excludedAttributes, then delete.
func Test_SCIM_EntraSequence_ProvisionsAndDeprovisions(t *testing.T) {
	router, base, admin := scimSetup(t)
	scim := scimClient{t, router, createSCIMToken(t, router, base, admin.Token)}
	upn := "entra-" + uuid.NewString()[:8] + "@example.com"
	external := uuid.NewString()

	scim.do("GET", "/Users?filter="+url.QueryEscape(`userName eq "missing-`+upn+`"`), nil, 200)
	body := json.RawMessage(`{
		"schemas": ["urn:ietf:params:scim:schemas:core:2.0:User",
			"urn:ietf:params:scim:schemas:extension:enterprise:2.0:User"],
		"externalId": "` + external + `", "userName": "` + upn + `", "active": true,
		"displayName": "Grace Hopper", "emails": [{"primary": true, "type": "work", "value": "` + upn + `"}],
		"meta": {"resourceType": "User"}, "name": {"formatted": "Grace Hopper", "familyName": "Hopper", "givenName": "Grace"},
		"title": "Rear Admiral",
		"urn:ietf:params:scim:schemas:extension:enterprise:2.0:User": {"department": "Navy"}}`)
	id := scim.do("POST", "/Users", body, 201)["id"].(string)
	conflict := scim.do("POST", "/Users", body, 409)
	assert.Equal(t, "uniqueness", conflict["scimType"])

	list := scim.do("GET", "/Users?filter="+url.QueryEscape(`externalId eq "`+external+`"`), nil, 200)
	require.Equal(t, float64(1), list["totalResults"])
	list = scim.do("GET", "/Users?filter="+url.QueryEscape(`emails[type eq "work"].value eq "`+upn+`"`), nil, 200)
	require.Equal(t, float64(1), list["totalResults"])

	patched := scim.do("PATCH", "/Users/"+id, json.RawMessage(`{
		"schemas": ["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
		"Operations": [
			{"op": "Replace", "path": "emails[type eq \"work\"].value", "value": "other@example.com"},
			{"op": "Replace", "path": "name.familyName", "value": "Murray Hopper"},
			{"op": "Add", "path": "title", "value": "Commodore"}]}`), 200)
	assert.Equal(t, "Grace Murray Hopper", patched["displayName"])
	assert.Equal(t, upn, patched["userName"], "the sign-in email follows userName, not emails")
	patched = scim.do("PATCH", "/Users/"+id, json.RawMessage(`{
		"Operations": [{"op": "Replace", "path": "active", "value": "False"}]}`), 200)
	assert.Equal(t, false, patched["active"])

	groupName := "Entra " + uuid.NewString()[:6]
	groupID := scim.do("POST", "/Groups", json.RawMessage(`{
		"schemas": ["urn:ietf:params:scim:schemas:core:2.0:Group"], "externalId": "`+uuid.NewString()+`",
		"displayName": "`+groupName+`", "members": []}`), 201)["id"].(string)
	groups := scim.do("GET", "/Groups?excludedAttributes=members&filter="+
		url.QueryEscape(`displayName eq "`+groupName+`"`), nil, 200)
	require.Equal(t, float64(1), groups["totalResults"])
	_, hasMembers := groups["Resources"].([]any)[0].(map[string]any)["members"]
	assert.False(t, hasMembers)
	group := scim.do("PATCH", "/Groups/"+groupID, json.RawMessage(`{
		"Operations": [{"op": "Add", "path": "members", "value": [{"value": "`+id+`"}]}]}`), 200)
	assert.Len(t, group["members"], 1)
	group = scim.do("PATCH", "/Groups/"+groupID, json.RawMessage(`{
		"Operations": [{"op": "Remove", "path": "members", "value": [{"value": "`+id+`"}]}]}`), 200)
	assert.Empty(t, group["members"])

	scim.do("DELETE", "/Users/"+id, nil, 204)
	scim.do("GET", "/Users/"+id, nil, 404)
	assert.Equal(t, users_enums.UserStatusInactive, getAccount(t, id).Status)
}

// An account SCIM didn't create (or a global admin) is never deactivated
// by an organization's IdP: deprovisioning suspends the membership, so
// the organization's teams stop granting access, and the account stays.
// SCIM never joins one by email: an owner adds it, the IdP finds it.
func Test_SCIM_ExistingAccount_SuspendsMembershipNotAccount(t *testing.T) {
	router, base, admin := scimSetup(t)
	scim := scimClient{t, router, createSCIMToken(t, router, base, admin.Token)}
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	engineer := users_testing.CreateTestUser(users_enums.UserRoleMember)
	globalAdmin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	project, _ := projects_testing.CreateTestProjectWithToken("SCIM existing", owner.Token, router)

	refused := scim.do("POST", "/Users", json.RawMessage(`{"userName": "`+engineer.Email+`",
		"name": {"givenName": "Renamed"}, "active": true}`), 409)
	assert.Contains(t, refused["detail"], "owner must add it")
	assert.NotContains(t, fmt.Sprint(refused), engineer.UserID.String())
	assert.Equal(t, float64(0), scim.do("GET", "/Users?filter="+url.QueryEscape(`userName eq "`+engineer.Email+`"`),
		nil, 200)["totalResults"], "not joined")

	test_utils.MakePostRequest(t, router, base+"/members", "Bearer "+admin.Token,
		organizations.MemberRequest{Email: engineer.Email, Role: organizations.RoleMember}, http.StatusOK)
	scim.do("POST", "/Users", json.RawMessage(`{"userName": "`+engineer.Email+`"}`), 409)
	found := scim.do("GET", "/Users?filter="+url.QueryEscape(`userName eq "`+engineer.Email+`"`), nil, 200)
	engineerID := found["Resources"].([]any)[0].(map[string]any)["id"].(string)
	assert.Equal(t, engineer.UserID.String(), engineerID)
	scim.do("PATCH", "/Users/"+engineerID,
		json.RawMessage(`{"Operations": [{"op": "replace", "path": "externalId", "value": "ext-1"}]}`), 200)
	group := scim.do("POST", "/Groups", json.RawMessage(`{"displayName": "Readers `+uuid.NewString()[:6]+`",
		"members": [{"value": "`+engineerID+`"}]}`), 201)
	var team organizations.Team
	teamPath := base + "/teams/" + group["id"].(string)
	test_utils.MakePostRequestAndUnmarshal(t, router, teamPath+"/grants", "Bearer "+admin.Token,
		organizations.GrantRequest{Role: "MEMBER"}, http.StatusOK, &team)
	require.Equal(t, "PROJECT_MEMBER", projectRole(t, router, engineer.Token, project.ID))
	other, _ := projects_testing.CreateTestProjectWithToken("SCIM existing 2", owner.Token, router)
	test_utils.MakePostRequest(t, router, teamPath+"/grants", "Bearer "+admin.Token,
		organizations.GrantRequest{ProjectID: &other.ID, Role: "PROJECT_ADMIN"}, http.StatusOK)
	require.Equal(t, "PROJECT_ADMIN", projectRole(t, router, engineer.Token, other.ID))

	scim.do("PATCH", "/Users/"+engineerID, json.RawMessage(`{"Operations": [
		{"op": "replace", "path": "active", "value": false},
		{"op": "replace", "path": "displayName", "value": "Someone Else"}]}`), 200)
	assert.Equal(t, users_enums.UserStatusActive, getAccount(t, engineerID).Status, "not this organization's account")
	assert.NotEqual(t, "Someone Else", getAccount(t, engineerID).Name)
	assert.Equal(t, "", projectRole(t, router, engineer.Token, project.ID), "suspended: no team access")
	assert.Equal(t, "", projectRole(t, router, engineer.Token, other.ID))
	assert.Equal(t, false, scim.do("GET", "/Users/"+engineerID, nil, 200)["active"])
	scim.do(
		"PATCH",
		"/Users/"+engineerID,
		json.RawMessage(`{"Operations": [{"op": "replace", "path": "active", "value": true}]}`),
		200,
	)
	assert.Equal(t, "PROJECT_MEMBER", projectRole(t, router, engineer.Token, project.ID))

	test_utils.MakePostRequest(t, router, base+"/members", "Bearer "+admin.Token,
		organizations.MemberRequest{Email: globalAdmin.Email, Role: organizations.RoleMember}, http.StatusOK)
	adminID := globalAdmin.UserID.String()
	scim.do("DELETE", "/Users/"+adminID, nil, 204)
	assert.Equal(t, users_enums.UserStatusActive, getAccount(t, adminID).Status, "global admins are never deactivated")

	// Even one SCIM created, once made a global admin.
	promotedID := scim.do("POST", "/Users",
		json.RawMessage(`{"userName": "promoted-`+uuid.NewString()[:8]+`@example.com"}`), 201)["id"].(string)
	require.NoError(t, (&users_repositories.UserRepository{}).UpdateUserRole(uuid.MustParse(promotedID),
		users_enums.UserRoleAdmin))
	scim.do(
		"PATCH",
		"/Users/"+promotedID,
		json.RawMessage(`{"Operations": [{"op": "replace", "path": "active", "value": false}]}`),
		200,
	)
	assert.Equal(t, users_enums.UserStatusActive, getAccount(t, promotedID).Status)
	// Demoted again, then reactivated: the suspension from its admin days lifts.
	require.NoError(t, (&users_repositories.UserRepository{}).UpdateUserRole(uuid.MustParse(promotedID),
		users_enums.UserRoleMember))
	reactivated := scim.do("PATCH", "/Users/"+promotedID,
		json.RawMessage(`{"Operations": [{"op": "replace", "path": "active", "value": true}]}`), 200)
	assert.Equal(t, true, reactivated["active"])

	scim.do("DELETE", "/Users/"+engineerID, nil, 204)
	assert.Equal(t, users_enums.UserStatusActive, getAccount(t, engineerID).Status)
	assert.Equal(t, "", projectRole(t, router, engineer.Token, project.ID))
}

// A deprovisioned owner keeps their account but not the organization: no
// managing it, and no minting a SCIM token to reactivate themselves.
func Test_SCIM_SuspendedOwner_CannotManageOrganization(t *testing.T) {
	router, base, admin := scimSetup(t)
	scim := scimClient{t, router, createSCIMToken(t, router, base, admin.Token)}
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	test_utils.MakePostRequest(t, router, base+"/members", "Bearer "+admin.Token,
		organizations.MemberRequest{Email: owner.Email, Role: organizations.RoleOwner}, http.StatusOK)
	test_utils.MakeGetRequest(t, router, base+"/scim-tokens", "Bearer "+owner.Token, http.StatusOK)

	scim.do("PATCH", "/Users/"+owner.UserID.String(),
		json.RawMessage(`{"Operations": [{"op": "replace", "path": "active", "value": false}]}`), 200)
	test_utils.MakePostRequest(t, router, base+"/scim-tokens", "Bearer "+owner.Token,
		organizations.SCIMTokenRequest{Name: "comeback"}, http.StatusForbidden)
	test_utils.MakePostRequest(t, router, base+"/members", "Bearer "+owner.Token,
		organizations.MemberRequest{Email: admin.Email, Role: organizations.RoleMember}, http.StatusForbidden)
	test_utils.MakeGetRequest(t, router, base+"/members", "Bearer "+owner.Token, http.StatusForbidden)

	scim.do("PATCH", "/Users/"+owner.UserID.String(),
		json.RawMessage(`{"Operations": [{"op": "replace", "path": "active", "value": true}]}`), 200)
	test_utils.MakeGetRequest(t, router, base+"/scim-tokens", "Bearer "+owner.Token, http.StatusOK)
}

func Test_SCIM_TokensAndIsolation(t *testing.T) {
	router, base, admin := scimSetup(t)
	orgAdmin := users_testing.CreateTestUser(users_enums.UserRoleMember)
	test_utils.MakePostRequest(t, router, base+"/members", "Bearer "+admin.Token,
		organizations.MemberRequest{UserID: &orgAdmin.UserID, Role: organizations.RoleAdmin}, http.StatusOK)
	test_utils.MakePostRequest(t, router, base+"/scim-tokens", "Bearer "+orgAdmin.Token,
		organizations.SCIMTokenRequest{Name: "okta"}, http.StatusForbidden) // owners only
	test_utils.MakeGetRequest(t, router, base+"/scim-tokens", "Bearer "+orgAdmin.Token, http.StatusForbidden)

	var created organizations.NewSCIMToken
	test_utils.MakePostRequestAndUnmarshal(t, router, base+"/scim-tokens", "Bearer "+admin.Token,
		organizations.SCIMTokenRequest{Name: "okta"}, http.StatusOK, &created)
	require.NotEmpty(t, created.Token)
	resp := test_utils.MakeGetRequest(t, router, base+"/scim-tokens", "Bearer "+admin.Token, http.StatusOK)
	assert.NotContains(t, string(resp.Body), created.Token, "the secret is shown once")
	hash := sha256.Sum256([]byte(created.Token))
	assert.NotContains(t, string(resp.Body), hex.EncodeToString(hash[:]))

	scim := scimClient{t, router, created.Token}
	scim.do("GET", "/Users", nil, 200)
	for _, bad := range []string{"", "Bearer nope", "Bearer " + created.Token + "x", created.Token} {
		test_utils.MakeRequest(t, router, test_utils.RequestOptions{
			Method: "GET", URL: "/scim/v2/Users", AuthToken: bad, ExpectedStatus: http.StatusUnauthorized,
		})
	}

	// Another organization's token sees none of this one's users or teams.
	otherAdmin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	var other organizations.Organization
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/orgs", "Bearer "+otherAdmin.Token,
		organizations.OrgRequest{Name: "Other", Slug: "other-" + uuid.NewString()[:8]}, http.StatusOK, &other)
	otherBase := "/api/v1/orgs/" + other.ID.String()
	t.Cleanup(func() { test_utils.MakeDeleteRequest(t, router, otherBase, "Bearer "+otherAdmin.Token, http.StatusOK) })
	foreign := scimClient{t, router, createSCIMToken(t, router, otherBase, otherAdmin.Token)}
	userID := scim.do("POST", "/Users", json.RawMessage(`{"userName": "iso-`+uuid.NewString()[:8]+`@example.com"}`), 201)["id"].(string)
	groupID := scim.do("POST", "/Groups", json.RawMessage(`{"displayName": "Iso"}`), 201)["id"].(string)
	foreign.do("GET", "/Users/"+userID, nil, 404)
	foreign.do(
		"PATCH",
		"/Users/"+userID,
		json.RawMessage(`{"Operations": [{"op": "replace", "path": "active", "value": false}]}`),
		404,
	)
	foreign.do("DELETE", "/Groups/"+groupID, nil, 404)
	foreign.do(
		"POST",
		"/Groups",
		json.RawMessage(`{"displayName": "Steal", "members": [{"value": "`+userID+`"}]}`),
		400,
	)
	assert.Equal(t, users_enums.UserStatusActive, getAccount(t, userID).Status)

	// Revoked tokens stop working.
	test_utils.MakeDeleteRequest(
		t,
		router,
		base+"/scim-tokens/"+created.ID.String(),
		"Bearer "+admin.Token,
		http.StatusOK,
	)
	scim.do("GET", "/Users", nil, 401)
}

func Test_SCIM_FiltersPagingAndErrors(t *testing.T) {
	router, base, admin := scimSetup(t)
	scim := scimClient{t, router, createSCIMToken(t, router, base, admin.Token)}
	tag := uuid.NewString()[:6]
	for i := range 3 {
		scim.do("POST", "/Users", json.RawMessage(fmt.Sprintf(`{"userName": "page-%s-%d@example.com"}`, tag, i)), 201)
	}
	all := scim.do("GET", "/Users", nil, 200)
	total := all["totalResults"].(float64)
	assert.GreaterOrEqual(t, total, float64(4)) // the creator too
	page := scim.do("GET", "/Users?startIndex=2&count=2", nil, 200)
	assert.Equal(t, total, page["totalResults"])
	assert.Equal(t, float64(2), page["itemsPerPage"])
	assert.Equal(t, float64(2), page["startIndex"])
	assert.Equal(t, float64(0), scim.do("GET", "/Users?count=0", nil, 200)["itemsPerPage"])

	upper := scim.do("GET", "/Users?filter="+url.QueryEscape(`USERNAME Eq "PAGE-`+tag+`-1@EXAMPLE.COM"`), nil, 200)
	assert.Equal(t, float64(1), upper["totalResults"], "attribute names and emails are case-insensitive")
	for _, bad := range []string{`userName co "x"`, `title eq "x"`, `userName eq x`, `userName eq "a" and active eq true`} {
		failed := scim.do("GET", "/Users?filter="+url.QueryEscape(bad), nil, 400)
		assert.Equal(t, "invalidFilter", failed["scimType"], bad)
	}
	assert.Equal(
		t,
		"invalidSyntax",
		scim.do("PATCH", "/Users/"+uuid.NewString(), json.RawMessage(`{"Operations": [{"op": "move"}]}`), 400)["scimType"],
	)
	scim.do("GET", "/Users/not-a-uuid", nil, 404)
	scim.do("POST", "/Users", json.RawMessage(`{"userName": "no-at-sign"}`), 400)
	failed := scim.do(
		"POST",
		"/Groups",
		json.RawMessage(`{"displayName": "x", "members": [{"value": "`+uuid.NewString()+`"}]}`),
		400,
	)
	assert.Equal(t, "invalidValue", failed["scimType"])

	config := scim.do("GET", "/ServiceProviderConfig", nil, 200)
	assert.Equal(t, true, config["patch"].(map[string]any)["supported"])
	assert.Equal(t, float64(2), scim.do("GET", "/ResourceTypes", nil, 200)["totalResults"])
	assert.Equal(t, float64(2), scim.do("GET", "/Schemas", nil, 200)["totalResults"])
}

func (c scimClient) do(method, path string, body any, status int) map[string]any {
	c.t.Helper()
	resp := test_utils.MakeRequest(c.t, c.router, test_utils.RequestOptions{
		Method: method, URL: "/scim/v2" + path, Body: body, AuthToken: "Bearer " + c.token, ExpectedStatus: status,
	})
	out := map[string]any{}
	if len(resp.Body) > 0 {
		require.NoError(c.t, json.Unmarshal(resp.Body, &out), string(resp.Body))
		if status != 204 {
			assert.Contains(c.t, resp.Headers.Get("Content-Type"), "application/scim+json")
		}
	}
	return out
}

func scimSetup(t *testing.T) (*gin.Engine, string, *users_dto.SignInResponseDTO) {
	router := projects_testing.CreateTestRouter(projects_controllers.GetProjectController(),
		projects_controllers.GetMembershipController(), organizations.GetController())
	organizations.GetController().RegisterSCIMRoutes(router.Group("/scim/v2"))
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	var org organizations.Organization
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/orgs", "Bearer "+admin.Token,
		organizations.OrgRequest{Name: "SCIM", Slug: "scim-" + uuid.NewString()[:8]}, http.StatusOK, &org)
	base := "/api/v1/orgs/" + org.ID.String()
	t.Cleanup(func() { test_utils.MakeDeleteRequest(t, router, base, "Bearer "+admin.Token, http.StatusOK) })
	return router, base, admin
}

func createSCIMToken(t *testing.T, router *gin.Engine, base, adminToken string) string {
	var created organizations.NewSCIMToken
	test_utils.MakePostRequestAndUnmarshal(t, router, base+"/scim-tokens", "Bearer "+adminToken,
		organizations.SCIMTokenRequest{Name: "test"}, http.StatusOK, &created)
	return created.Token
}

func getAccount(t *testing.T, id string) *users_models.User {
	user, err := (&users_repositories.UserRepository{}).GetUserByID(uuid.MustParse(id))
	require.NoError(t, err)
	return user
}

func projectRole(t *testing.T, router *gin.Engine, token string, projectID uuid.UUID) string {
	var listed projects_dto.ListProjectsResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/projects", "Bearer "+token, http.StatusOK, &listed)
	for _, project := range listed.Projects {
		if project.ID == projectID && project.UserRole != nil {
			return string(*project.UserRole)
		}
	}
	return ""
}
