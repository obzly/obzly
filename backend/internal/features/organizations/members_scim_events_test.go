package organizations_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"obzly/internal/features/organizations"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_ListMembers_ReturnsWhenEachJoinedAndTheirTeams(t *testing.T) {
	router, base, admin := scimSetup(t)
	member := users_testing.CreateTestUser(users_enums.UserRoleMember)
	loner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	outsider := users_testing.CreateTestUser(users_enums.UserRoleMember)
	for _, id := range []uuid.UUID{member.UserID, loner.UserID} {
		test_utils.MakePostRequest(t, router, base+"/members", "Bearer "+admin.Token,
			organizations.MemberRequest{UserID: &id, Role: organizations.RoleMember}, http.StatusOK)
	}
	for _, name := range []string{"SRE", "Platform"} {
		var team organizations.Team
		test_utils.MakePostRequestAndUnmarshal(t, router, base+"/teams", "Bearer "+admin.Token,
			organizations.TeamRequest{Name: name}, http.StatusOK, &team)
		test_utils.MakePostRequest(t, router, base+"/teams/"+team.ID.String()+"/members", "Bearer "+admin.Token,
			organizations.MemberRequest{Email: member.Email}, http.StatusOK)
	}

	// Members read the list, with teams sorted by name.
	var members []organizations.Membership
	test_utils.MakeGetRequestAndUnmarshal(t, router, base+"/members", "Bearer "+member.Token, http.StatusOK, &members)
	byUser := map[uuid.UUID]organizations.Membership{}
	for _, m := range members {
		byUser[m.UserID] = m
	}
	require.Contains(t, byUser, member.UserID)
	assert.Equal(t, []string{"Platform", "SRE"}, byUser[member.UserID].Teams)
	assert.False(t, byUser[member.UserID].CreatedAt.IsZero(), "joined")
	assert.Equal(t, []string{}, byUser[loner.UserID].Teams)

	test_utils.MakeGetRequest(t, router, base+"/members", "Bearer "+outsider.Token, http.StatusForbidden)
}

func Test_ListSCIMEvents_ListsThisOrganizationsProvisioningForOwnersOnly(t *testing.T) {
	router, base, admin := scimSetup(t)
	scim := scimClient{t, router, createSCIMToken(t, router, base, admin.Token)}
	email := "events-" + uuid.NewString()[:8] + "@example.com"

	created := scim.do("POST", "/Users", json.RawMessage(`{"userName": "`+email+`",
		"name": {"givenName": "Ada", "familyName": "Lovelace"}, "active": true}`), 201)
	id := created["id"].(string)
	scim.do("PATCH", "/Users/"+id, json.RawMessage(`{"Operations": [{"op": "replace", "path": "displayName",
		"value": "Ada King"}]}`), 200)
	scim.do("PATCH", "/Users/"+id, json.RawMessage(`{"Operations": [{"op": "replace", "value": {"active": false}}]}`),
		200)

	// Another organization's provisioning stays out of this one's list.
	otherRouter, otherBase, otherAdmin := scimSetup(t)
	other := scimClient{t, otherRouter, createSCIMToken(t, otherRouter, otherBase, otherAdmin.Token)}
	otherEmail := "other-" + uuid.NewString()[:8] + "@example.com"
	other.do("POST", "/Users", json.RawMessage(`{"userName": "`+otherEmail+`", "active": true}`), 201)

	var events []organizations.SCIMEvent
	test_utils.MakeGetRequestAndUnmarshal(t, router, base+"/scim-events", "Bearer "+admin.Token, http.StatusOK,
		&events)
	messages := make([]string, 0, len(events))
	for _, event := range events {
		messages = append(messages, event.Message)
	}
	assert.Equal(t, []string{
		"SCIM (test): user " + email + " deactivated",
		"SCIM (test): user " + email + " updated (name)",
		"SCIM (test): user " + email + " created",
	}, messages, "newest first")

	test_utils.MakeGetRequestAndUnmarshal(t, router, base+"/scim-events?limit=1", "Bearer "+admin.Token,
		http.StatusOK, &events)
	assert.Len(t, events, 1)

	// Owners and global admins only, like the tokens.
	orgAdmin := users_testing.CreateTestUser(users_enums.UserRoleMember)
	member := users_testing.CreateTestUser(users_enums.UserRoleMember)
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	for user, role := range map[uuid.UUID]string{
		orgAdmin.UserID: organizations.RoleAdmin, member.UserID: organizations.RoleMember,
		owner.UserID: organizations.RoleOwner,
	} {
		test_utils.MakePostRequest(t, router, base+"/members", "Bearer "+admin.Token,
			organizations.MemberRequest{UserID: &user, Role: role}, http.StatusOK)
	}
	test_utils.MakeGetRequest(t, router, base+"/scim-events", "Bearer "+orgAdmin.Token, http.StatusForbidden)
	test_utils.MakeGetRequest(t, router, base+"/scim-events", "Bearer "+member.Token, http.StatusForbidden)
	test_utils.MakeGetRequest(t, router, base+"/scim-events", "Bearer "+owner.Token, http.StatusOK)
	test_utils.MakeGetRequest(t, router, otherBase+"/scim-events", "Bearer "+owner.Token, http.StatusForbidden)
	test_utils.MakeGetRequest(t, router, "/api/v1/orgs/"+uuid.NewString()+"/scim-events", "Bearer "+admin.Token,
		http.StatusNotFound)
}
