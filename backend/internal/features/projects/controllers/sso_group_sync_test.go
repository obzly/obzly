package projects_controllers

import (
	"testing"
	"time"

	projects_repositories "obzly/internal/features/projects/repositories"
	projects_services "obzly/internal/features/projects/services"
	projects_testing "obzly/internal/features/projects/testing"
	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"
	sso_dto "obzly/internal/features/users/sso/dto"
	sso_models "obzly/internal/features/users/sso/models"
	sso_repositories "obzly/internal/features/users/sso/repositories"
	sso_services "obzly/internal/features/users/sso/services"
	users_testing "obzly/internal/features/users/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newGroupSyncConnector(t *testing.T) *sso_models.Connector {
	t.Helper()

	connector := &sso_models.Connector{
		ID:                    uuid.New(),
		Name:                  "Group Sync Test",
		ProviderType:          sso_models.ProviderTypeCognito,
		IssuerURL:             "https://example.invalid",
		ClientID:              "client",
		ClientSecretEncrypted: "x",
		Scopes:                "openid email profile",
		GroupsClaim:           "cognito:groups",
		ButtonLabel:           "Sign in",
		Enabled:               true,
		CreatedAt:             time.Now().UTC(),
		UpdatedAt:             time.Now().UTC(),
	}
	require.NoError(t, (&sso_repositories.ConnectorRepository{}).Create(connector))
	return connector
}

func mapGroup(t *testing.T, connectorID uuid.UUID, group string, projectID uuid.UUID, role users_enums.ProjectRole) {
	t.Helper()

	admin := &users_models.User{ID: uuid.New(), Role: users_enums.UserRoleAdmin}
	_, err := sso_services.GetConnectorService().CreateGroupMapping(
		connectorID,
		&sso_dto.CreateGroupMappingRequestDTO{GroupName: group, ProjectID: &projectID, Role: string(role)},
		admin,
	)
	require.NoError(t, err)
}

// syncGroups runs the same steps as a login: resolve mappings for the user's
// groups, then sync memberships.
func syncGroups(t *testing.T, userID, connectorID uuid.UUID, groups ...string) {
	t.Helper()

	grants, err := sso_services.GetConnectorService().ResolveGroupGrants(connectorID, groups)
	require.NoError(t, err)
	require.NoError(t, projects_services.GetMembershipService().
		SyncSSOGroupMemberships(userID, connectorID, grants.ProjectRoles))
}

func membershipOf(userID, projectID uuid.UUID) (role users_enums.ProjectRole, managedBy *uuid.UUID, found bool) {
	m, err := (&projects_repositories.MembershipRepository{}).GetMembershipByUserAndProject(userID, projectID)
	if err != nil {
		return "", nil, false
	}
	return m.Role, m.SSOConnectorID, true
}

func Test_SSOGroupSync_UserInMappedGroup_GetsTaggedMembership(t *testing.T) {
	users_testing.CleanupPlans()
	router := projects_testing.CreateTestRouter(GetProjectController(), GetMembershipController())
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project := projects_testing.CreateTestProject("P", owner, router)
	connector := newGroupSyncConnector(t)
	mapGroup(t, connector.ID, "viewers", project.ID, users_enums.ProjectRoleViewer)

	syncGroups(t, user.UserID, connector.ID, "viewers")

	role, managedBy, found := membershipOf(user.UserID, project.ID)
	require.True(t, found)
	assert.Equal(t, users_enums.ProjectRoleViewer, role)
	require.NotNil(t, managedBy)
	assert.Equal(t, connector.ID, *managedBy)
}

func Test_SSOGroupSync_SeveralGroupsSameProject_HighestRoleWins(t *testing.T) {
	users_testing.CleanupPlans()
	router := projects_testing.CreateTestRouter(GetProjectController(), GetMembershipController())
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project := projects_testing.CreateTestProject("P", owner, router)
	connector := newGroupSyncConnector(t)
	mapGroup(t, connector.ID, "viewers", project.ID, users_enums.ProjectRoleViewer)
	mapGroup(t, connector.ID, "editors", project.ID, users_enums.ProjectRoleMember)

	syncGroups(t, user.UserID, connector.ID, "viewers", "editors")

	role, _, found := membershipOf(user.UserID, project.ID)
	require.True(t, found)
	assert.Equal(t, users_enums.ProjectRoleMember, role)
}

func Test_SSOGroupSync_GroupChanges_RoleUpdatedThenMembershipRemoved(t *testing.T) {
	users_testing.CleanupPlans()
	router := projects_testing.CreateTestRouter(GetProjectController(), GetMembershipController())
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project := projects_testing.CreateTestProject("P", owner, router)
	connector := newGroupSyncConnector(t)
	mapGroup(t, connector.ID, "viewers", project.ID, users_enums.ProjectRoleViewer)
	mapGroup(t, connector.ID, "admins", project.ID, users_enums.ProjectRoleAdmin)

	syncGroups(t, user.UserID, connector.ID, "viewers")
	syncGroups(t, user.UserID, connector.ID, "admins")
	role, managedBy, found := membershipOf(user.UserID, project.ID)
	require.True(t, found)
	assert.Equal(t, users_enums.ProjectRoleAdmin, role)
	assert.NotNil(t, managedBy)

	syncGroups(t, user.UserID, connector.ID)
	_, _, found = membershipOf(user.UserID, project.ID)
	assert.False(t, found)
}

func Test_SSOGroupSync_ManuallyAddedMember_NeverChangedOrRemoved(t *testing.T) {
	users_testing.CleanupPlans()
	router := projects_testing.CreateTestRouter(GetProjectController(), GetMembershipController())
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project := projects_testing.CreateTestProject("P", owner, router)
	projects_testing.AddMemberToProjectViaOwner(project, user, users_enums.ProjectRoleMember, router)
	connector := newGroupSyncConnector(t)
	mapGroup(t, connector.ID, "viewers", project.ID, users_enums.ProjectRoleViewer)

	syncGroups(t, user.UserID, connector.ID, "viewers")
	role, managedBy, found := membershipOf(user.UserID, project.ID)
	require.True(t, found)
	assert.Equal(t, users_enums.ProjectRoleMember, role)
	assert.Nil(t, managedBy)

	syncGroups(t, user.UserID, connector.ID)
	_, _, found = membershipOf(user.UserID, project.ID)
	assert.True(t, found)
}

func Test_SSOGroupSync_ProjectOwner_NeverChangedOrRemoved(t *testing.T) {
	users_testing.CleanupPlans()
	router := projects_testing.CreateTestRouter(GetProjectController(), GetMembershipController())
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project := projects_testing.CreateTestProject("P", owner, router)
	connector := newGroupSyncConnector(t)
	mapGroup(t, connector.ID, "viewers", project.ID, users_enums.ProjectRoleViewer)

	syncGroups(t, owner.UserID, connector.ID, "viewers")
	syncGroups(t, owner.UserID, connector.ID)

	role, _, found := membershipOf(owner.UserID, project.ID)
	require.True(t, found)
	assert.Equal(t, users_enums.ProjectRoleOwner, role)
}

func Test_SSOGroupSync_AdminChangesSyncedRole_DetachesFromSync(t *testing.T) {
	users_testing.CleanupPlans()
	router := projects_testing.CreateTestRouter(GetProjectController(), GetMembershipController())
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project := projects_testing.CreateTestProject("P", owner, router)
	connector := newGroupSyncConnector(t)
	mapGroup(t, connector.ID, "viewers", project.ID, users_enums.ProjectRoleViewer)
	syncGroups(t, user.UserID, connector.ID, "viewers")

	require.NoError(t, (&projects_repositories.MembershipRepository{}).
		UpdateMemberRole(user.UserID, project.ID, users_enums.ProjectRoleMember))

	syncGroups(t, user.UserID, connector.ID)
	role, managedBy, found := membershipOf(user.UserID, project.ID)
	require.True(t, found)
	assert.Equal(t, users_enums.ProjectRoleMember, role)
	assert.Nil(t, managedBy)
}

func Test_SSOGroupMapping_Validation(t *testing.T) {
	users_testing.CleanupPlans()
	router := projects_testing.CreateTestRouter(GetProjectController(), GetMembershipController())
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project := projects_testing.CreateTestProject("P", owner, router)
	connector := newGroupSyncConnector(t)
	service := sso_services.GetConnectorService()
	admin := &users_models.User{ID: uuid.New(), Role: users_enums.UserRoleAdmin}
	member := &users_models.User{ID: uuid.New(), Role: users_enums.UserRoleMember}

	request := func(role users_enums.ProjectRole, projectID uuid.UUID) *sso_dto.CreateGroupMappingRequestDTO {
		return &sso_dto.CreateGroupMappingRequestDTO{GroupName: "g", ProjectID: &projectID, Role: string(role)}
	}

	_, err := service.CreateGroupMapping(connector.ID, request(users_enums.ProjectRoleOwner, project.ID), admin)
	assert.ErrorIs(t, err, sso_services.ErrInvalidGroupMappingRole)

	_, err = service.CreateGroupMapping(connector.ID, request(users_enums.ProjectRoleViewer, uuid.New()), admin)
	assert.ErrorIs(t, err, sso_services.ErrGroupMappingProject)

	_, err = service.CreateGroupMapping(connector.ID, request(users_enums.ProjectRoleViewer, project.ID), member)
	assert.ErrorIs(t, err, sso_services.ErrInsufficientPermissions)

	_, err = service.CreateGroupMapping(connector.ID, request(users_enums.ProjectRoleViewer, project.ID), admin)
	require.NoError(t, err)
	_, err = service.CreateGroupMapping(connector.ID, request(users_enums.ProjectRoleMember, project.ID), admin)
	assert.ErrorIs(t, err, sso_services.ErrGroupMappingExists)
}
