package users_controllers

import (
	"net/http"
	"testing"

	projects_controllers "obzly/internal/features/projects/controllers"
	projects_repositories "obzly/internal/features/projects/repositories"
	projects_testing "obzly/internal/features/projects/testing"
	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"
	users_repositories "obzly/internal/features/users/repositories"
	sso_dto "obzly/internal/features/users/sso/dto"
	sso_models "obzly/internal/features/users/sso/models"
	sso_services "obzly/internal/features/users/sso/services"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_SSOCallback_GroupMapping_GrantsThenRevokesProjectAccessAcrossLogins(t *testing.T) {
	router := createSSOTestRouter()
	admin := users_testing.ReacreateInitAdminAndGetAccess()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	projectRouter := projects_testing.CreateTestRouter(
		projects_controllers.GetProjectController(),
		projects_controllers.GetMembershipController(),
	)
	project := projects_testing.CreateTestProject("Group Synced", owner, projectRouter)
	fake := newFakeOIDCServer(t, "client-1")

	var connector sso_dto.ConnectorResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/sso/connectors", "Bearer "+admin.Token,
		sso_dto.CreateConnectorRequestDTO{
			Name:         "Cognito",
			ProviderType: sso_models.ProviderTypeCognito,
			IssuerURL:    fake.Server.URL,
			ClientID:     fake.ClientID,
			ClientSecret: "shh",
			GroupsClaim:  "cognito:groups",
			ButtonLabel:  "Continue with Cognito",
			Enabled:      true,
		},
		http.StatusOK, &connector,
	)
	assert.Equal(t, "cognito:groups", connector.GroupsClaim)

	var mapping sso_dto.GroupMappingResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/sso/connectors/"+connector.ID.String()+"/group-mappings", "Bearer "+admin.Token,
		sso_dto.CreateGroupMappingRequestDTO{
			GroupName: "obzly-viewers", ProjectID: &project.ID, Role: string(users_enums.ProjectRoleViewer),
		},
		http.StatusOK, &mapping,
	)

	redirectURI := "http://localhost:3000/auth/callback"
	email := "sso-groups-" + uuid.New().String()[:8] + "@example.com"
	signIn := func(groups []string) users_dto.OAuthCallbackResponseDTO {
		code := fake.IssueCode("group-subject", email, "Group User")
		if groups != nil {
			fake.extra[code]["cognito:groups"] = groups
		}
		var response users_dto.OAuthCallbackResponseDTO
		test_utils.MakePostRequestAndUnmarshal(
			t, router, "/api/v1/auth/sso/callback", "",
			sso_dto.SSOCallbackRequestDTO{
				State:       fetchStateForConnector(t, router, connector.ID, redirectURI),
				Code:        code,
				RedirectUri: redirectURI,
			},
			http.StatusOK, &response,
		)
		return response
	}
	roleOf := func(userID uuid.UUID) (users_enums.ProjectRole, bool) {
		m, err := (&projects_repositories.MembershipRepository{}).GetMembershipByUserAndProject(userID, project.ID)
		if err != nil {
			return "", false
		}
		return m.Role, true
	}

	first := signIn([]string{"obzly-viewers", "unmapped-group"})
	require.NotEmpty(t, first.Token)
	role, found := roleOf(first.UserID)
	require.True(t, found, "new user in a mapped group should be added to the project")
	assert.Equal(t, users_enums.ProjectRoleViewer, role)

	second := signIn(nil) // Cognito omits the claim when the user is in no group
	require.Equal(t, first.UserID, second.UserID)
	_, found = roleOf(first.UserID)
	assert.False(t, found, "leaving the group should remove the synced membership on next sign-in")
}

func createCognitoConnectorWithGroups(
	t *testing.T,
	router *gin.Engine,
	fake *fakeOIDCServer,
) sso_dto.ConnectorResponseDTO {
	t.Helper()
	return createCognitoConnectorWithMFATrust(t, router, fake, nil)
}

// createCognitoConnectorWithMFATrust: trustIdPMFA nil leaves the setting at
// its default (not trusted).
func createCognitoConnectorWithMFATrust(
	t *testing.T, router *gin.Engine, fake *fakeOIDCServer, trustIdPMFA *bool,
) sso_dto.ConnectorResponseDTO {
	t.Helper()

	admin := users_testing.ReacreateInitAdminAndGetAccess()
	var connector sso_dto.ConnectorResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/sso/connectors", "Bearer "+admin.Token,
		sso_dto.CreateConnectorRequestDTO{
			Name:         "Cognito",
			ProviderType: sso_models.ProviderTypeCognito,
			IssuerURL:    fake.Server.URL,
			ClientID:     fake.ClientID,
			ClientSecret: "shh",
			GroupsClaim:  "cognito:groups",
			TrustIdPMFA:  trustIdPMFA,
			ButtonLabel:  "Continue with Cognito",
			Enabled:      true,
		},
		http.StatusOK, &connector,
	)
	return connector
}

func mapGroupToGlobalAdmin(t *testing.T, connectorID uuid.UUID, group string) {
	t.Helper()

	root := &users_models.User{ID: uuid.New(), Role: users_enums.UserRoleAdmin, IsRootAdmin: true}
	_, err := sso_services.GetConnectorService().CreateGroupMapping(
		connectorID,
		&sso_dto.CreateGroupMappingRequestDTO{GroupName: group, Role: string(users_enums.UserRoleAdmin)},
		root,
	)
	require.NoError(t, err)
}

func ssoSignIn(
	t *testing.T, router *gin.Engine, fake *fakeOIDCServer, connectorID uuid.UUID,
	subject, email string, groups []string,
) users_dto.OAuthCallbackResponseDTO {
	t.Helper()

	redirectURI := "http://localhost:3000/auth/callback"
	code := fake.IssueCode(subject, email, "Group User")
	if groups != nil {
		fake.extra[code]["cognito:groups"] = groups
	}
	var response users_dto.OAuthCallbackResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/auth/sso/callback", "",
		sso_dto.SSOCallbackRequestDTO{
			State:       fetchStateForConnector(t, router, connectorID, redirectURI),
			Code:        code,
			RedirectUri: redirectURI,
		},
		http.StatusOK, &response,
	)
	return response
}

func Test_SSOCallback_AdminGroup_PromotesWithMFAThenDemotesWhenGroupLeft(t *testing.T) {
	router := createSSOTestRouter()
	fake := newFakeOIDCServer(t, "client-1")
	trust := false
	connector := createCognitoConnectorWithMFATrust(t, router, fake, &trust)
	require.False(t, connector.TrustIdPMFA)
	mapGroupToGlobalAdmin(t, connector.ID, "obzly-admins")
	email := "sso-admin-" + uuid.New().String()[:8] + "@example.com"
	users := &users_repositories.UserRepository{}

	first := ssoSignIn(t, router, fake, connector.ID, "admin-subject", email, []string{"obzly-admins"})
	assert.Empty(t, first.Token, "a group-promoted admin must complete MFA setup before getting a token")
	assert.True(t, first.MFASetupRequired)
	user, err := users.GetUserByEmail(email)
	require.NoError(t, err)
	require.NotNil(t, user)
	assert.Equal(t, users_enums.UserRoleAdmin, user.Role)
	require.NotNil(t, user.SSOAdminConnectorID)
	assert.Equal(t, connector.ID, *user.SSOAdminConnectorID)

	second := ssoSignIn(t, router, fake, connector.ID, "admin-subject", email, nil)
	assert.NotEmpty(t, second.Token, "after demotion no MFA is required, so sign-in completes")
	user, err = users.GetUserByEmail(email)
	require.NoError(t, err)
	assert.Equal(t, users_enums.UserRoleMember, user.Role)
	assert.Nil(t, user.SSOAdminConnectorID)
}

// A manually promoted admin can't be linked by email at all (ErrOAuthAccountNotLinkable),
// so the sign-in never touches their role.
func Test_SSOCallback_AdminGroup_ManuallyPromotedAdminNeverDemoted(t *testing.T) {
	router := createSSOTestRouter()
	fake := newFakeOIDCServer(t, "client-1")
	connector := createCognitoConnectorWithGroups(t, router, fake)
	mapGroupToGlobalAdmin(t, connector.ID, "obzly-admins")
	manualAdmin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	redirectURI := "http://localhost:3000/auth/callback"
	code := fake.IssueCode("manual-admin-subject", manualAdmin.Email, "Manual Admin")
	resp := test_utils.MakePostRequest(
		t, router, "/api/v1/auth/sso/callback", "",
		sso_dto.SSOCallbackRequestDTO{
			State:       fetchStateForConnector(t, router, connector.ID, redirectURI),
			Code:        code,
			RedirectUri: redirectURI,
		},
		http.StatusBadRequest,
	)
	assert.Contains(t, string(resp.Body), "already exists")

	user, err := (&users_repositories.UserRepository{}).GetUserByEmail(manualAdmin.Email)
	require.NoError(t, err)
	assert.Equal(t, users_enums.UserRoleAdmin, user.Role)
	assert.Nil(t, user.SSOAdminConnectorID)
}

func Test_SSOGlobalAdminMapping_OnlyRootAdminMayManageIt(t *testing.T) {
	router := createSSOTestRouter()
	fake := newFakeOIDCServer(t, "client-1")
	connector := createCognitoConnectorWithGroups(t, router, fake)
	service := sso_services.GetConnectorService()
	nonRootAdmin := &users_models.User{ID: uuid.New(), Role: users_enums.UserRoleAdmin}
	root := &users_models.User{ID: uuid.New(), Role: users_enums.UserRoleAdmin, IsRootAdmin: true}
	adminRole := string(users_enums.UserRoleAdmin)

	_, err := service.CreateGroupMapping(
		connector.ID, &sso_dto.CreateGroupMappingRequestDTO{GroupName: "g", Role: adminRole}, nonRootAdmin,
	)
	assert.ErrorIs(t, err, sso_services.ErrGlobalAdminRootOnly)

	_, err = service.CreateGroupMapping(
		connector.ID,
		&sso_dto.CreateGroupMappingRequestDTO{GroupName: "g", Role: string(users_enums.ProjectRoleViewer)},
		root,
	)
	assert.ErrorIs(t, err, sso_services.ErrInvalidGroupMappingRole, "a global mapping can only grant ADMIN")

	created, err := service.CreateGroupMapping(
		connector.ID, &sso_dto.CreateGroupMappingRequestDTO{GroupName: "g", Role: adminRole}, root,
	)
	require.NoError(t, err)
	assert.Nil(t, created.ProjectID)

	_, err = service.CreateGroupMapping(
		connector.ID, &sso_dto.CreateGroupMappingRequestDTO{GroupName: "g", Role: adminRole}, root,
	)
	assert.ErrorIs(t, err, sso_services.ErrGroupMappingExists)

	assert.ErrorIs(
		t,
		service.DeleteGroupMapping(connector.ID, created.ID, nonRootAdmin),
		sso_services.ErrGlobalAdminRootOnly,
	)
	assert.NoError(t, service.DeleteGroupMapping(connector.ID, created.ID, root))
}

// With registration closed, a user in a mapped group still gets an account on
// first sign-in; one in no mapped group (or without the claim) is refused.
func Test_SSOCallback_RegistrationClosed_OnlyMappedGroupsGetAnAccount(t *testing.T) {
	router := createSSOTestRouter()
	fake := newFakeOIDCServer(t, "client-1")
	connector := createCognitoConnectorWithGroups(t, router, fake)
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	projectRouter := projects_testing.CreateTestRouter(
		projects_controllers.GetProjectController(),
		projects_controllers.GetMembershipController(),
	)
	project := projects_testing.CreateTestProject("Closed Registration", owner, projectRouter)
	_, err := sso_services.GetConnectorService().CreateGroupMapping(
		connector.ID,
		&sso_dto.CreateGroupMappingRequestDTO{
			GroupName: "obzly-viewers", ProjectID: &project.ID, Role: string(users_enums.ProjectRoleViewer),
		},
		&users_models.User{ID: uuid.New(), Role: users_enums.UserRoleAdmin, IsRootAdmin: true},
	)
	require.NoError(t, err)

	// Closed only now: the setup above recreates the initial admin, which resets settings.
	settingsRepository := &users_repositories.UsersSettingsRepository{}
	settings, err := settingsRepository.GetSettings()
	require.NoError(t, err)
	previous := settings.IsAllowExternalRegistrations
	settings.IsAllowExternalRegistrations = false
	require.NoError(t, settingsRepository.UpdateSettings(settings))
	t.Cleanup(func() {
		settings.IsAllowExternalRegistrations = previous
		_ = settingsRepository.UpdateSettings(settings)
	})

	redirectURI := "http://localhost:3000/auth/callback"
	callback := func(subject string, groups []string, expectedStatus int) *test_utils.TestResponse {
		code := fake.IssueCode(subject, subject+"@example.com", "Closed Reg User")
		if groups != nil {
			fake.extra[code]["cognito:groups"] = groups
		}
		return test_utils.MakePostRequest(t, router, "/api/v1/auth/sso/callback", "",
			sso_dto.SSOCallbackRequestDTO{
				State:       fetchStateForConnector(t, router, connector.ID, redirectURI),
				Code:        code,
				RedirectUri: redirectURI,
			},
			expectedStatus,
		)
	}

	suffix := uuid.NewString()[:8]
	callback("mapped-"+suffix, []string{"obzly-viewers", "other"}, http.StatusOK)

	refused := callback("unmapped-"+suffix, []string{"some-other-group"}, http.StatusBadRequest)
	assert.Contains(t, string(refused.Body), "isn't in a group that has access")

	callback("no-groups-"+suffix, nil, http.StatusBadRequest)
}
