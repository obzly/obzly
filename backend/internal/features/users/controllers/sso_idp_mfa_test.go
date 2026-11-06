package users_controllers

import (
	"net/http"
	"testing"

	users_enums "obzly/internal/features/users/enums"
	mfa_controllers "obzly/internal/features/users/mfa/controllers"
	mfa_dto "obzly/internal/features/users/mfa/dto"
	users_middleware "obzly/internal/features/users/middleware"
	users_repositories "obzly/internal/features/users/repositories"
	users_services "obzly/internal/features/users/services"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createMFAGateRouter mirrors cmd/main.go's ordering: the MFA self-service
// routes sit before RequireMFAIfEnforced, everything else behind it.
func createMFAGateRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	v1 := router.Group("/api/v1")
	protected := v1.Group("")
	protected.Use(users_middleware.AuthMiddleware(users_services.GetUserService()))
	mfa_controllers.GetMFAController().RegisterProtectedRoutes(protected)
	gated := protected.Group("")
	gated.Use(users_middleware.RequireMFAIfEnforced())
	gated.GET("/probe", func(ctx *gin.Context) { ctx.JSON(http.StatusOK, gin.H{"ok": true}) })
	return router
}

// A connector trusting its provider's MFA signs a group-promoted Admin
// whose ID token shows MFA (amr) straight in: no local TOTP enrollment, and
// the session passes the MFA gate. A password-style session for the same
// account is still held to local MFA.
func Test_SSOCallback_TrustedIdPMFA_AdminSignsInWithoutLocalMFA(t *testing.T) {
	router := createSSOTestRouter()
	fake := newFakeOIDCServer(t, "client-1")
	fake.amr = []string{"pwd", "mfa"}
	trust := true
	connector := createCognitoConnectorWithMFATrust(t, router, fake, &trust)
	require.True(t, connector.TrustIdPMFA)
	mapGroupToGlobalAdmin(t, connector.ID, "obzly-admins")
	email := "sso-trusted-" + uuid.New().String()[:8] + "@example.com"

	first := ssoSignIn(t, router, fake, connector.ID, "trusted-subject", email, []string{"obzly-admins"})
	assert.NotEmpty(t, first.Token, "the provider enforced MFA, so the new Admin gets a session")
	assert.False(t, first.MFASetupRequired)
	assert.Empty(t, first.MFAToken)

	user, err := (&users_repositories.UserRepository{}).GetUserByEmail(email)
	require.NoError(t, err)
	assert.Equal(t, users_enums.UserRoleAdmin, user.Role)
	assert.False(t, user.MFAEnabled)

	gate := createMFAGateRouter()
	test_utils.MakeGetRequest(t, gate, "/api/v1/probe", "Bearer "+first.Token, http.StatusOK)
	var status mfa_dto.StatusResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t,
		gate,
		"/api/v1/users/mfa/status",
		"Bearer "+first.Token,
		http.StatusOK,
		&status,
	)
	assert.True(t, status.Required, "the Admin role still requires MFA")
	assert.True(t, status.SatisfiedByIdentityProvider, "but this session already satisfied it")

	// Returning sign-in (existing identity) also skips local MFA.
	second := ssoSignIn(t, router, fake, connector.ID, "trusted-subject", email, []string{"obzly-admins"})
	assert.NotEmpty(t, second.Token)
	test_utils.MakeGetRequest(t, gate, "/api/v1/probe", "Bearer "+second.Token, http.StatusOK)

	// A session not established through the trusted connector is enforced.
	local, err := users_services.GetUserService().GenerateAccessToken(user)
	require.NoError(t, err)
	test_utils.MakeGetRequest(t, gate, "/api/v1/probe", "Bearer "+local.Token, http.StatusForbidden)
	status = mfa_dto.StatusResponseDTO{}
	test_utils.MakeGetRequestAndUnmarshal(
		t,
		gate,
		"/api/v1/users/mfa/status",
		"Bearer "+local.Token,
		http.StatusOK,
		&status,
	)
	assert.True(t, status.Required)
	assert.False(t, status.SatisfiedByIdentityProvider)
}

// New connectors don't trust the provider's MFA unless asked to.
func Test_CreateConnector_WithoutTrustIdPMFA_DefaultsToUntrusted(t *testing.T) {
	router := createSSOTestRouter()
	fake := newFakeOIDCServer(t, "client-1")
	connector := createCognitoConnectorWithMFATrust(t, router, fake, nil)
	assert.False(t, connector.TrustIdPMFA)
}

// Trusting the provider is not enough on its own: an ID token whose amr
// shows no second factor (or has no amr) gets the local MFA requirement.
func Test_SSOCallback_TrustedIdPMFA_WithoutMFAInAMR_AdminMustEnrollLocally(t *testing.T) {
	for name, amr := range map[string][]string{"password only": {"pwd"}, "no amr": nil} {
		t.Run(name, func(t *testing.T) {
			router := createSSOTestRouter()
			fake := newFakeOIDCServer(t, "client-1")
			fake.amr = amr
			trust := true
			connector := createCognitoConnectorWithMFATrust(t, router, fake, &trust)
			mapGroupToGlobalAdmin(t, connector.ID, "obzly-admins")
			email := "sso-no-amr-" + uuid.New().String()[:8] + "@example.com"

			response := ssoSignIn(t, router, fake, connector.ID, "no-amr-"+uuid.NewString()[:8], email,
				[]string{"obzly-admins"})
			assert.Empty(t, response.Token)
			assert.True(t, response.MFASetupRequired)
		})
	}
}

// Turning the setting off brings back the local enrollment requirement.
func Test_SSOCallback_UntrustedIdPMFA_AdminMustEnrollLocally(t *testing.T) {
	router := createSSOTestRouter()
	fake := newFakeOIDCServer(t, "client-1")
	trust := false
	connector := createCognitoConnectorWithMFATrust(t, router, fake, &trust)
	mapGroupToGlobalAdmin(t, connector.ID, "obzly-admins")
	email := "sso-untrusted-" + uuid.New().String()[:8] + "@example.com"

	response := ssoSignIn(t, router, fake, connector.ID, "untrusted-subject", email, []string{"obzly-admins"})
	assert.Empty(t, response.Token)
	assert.True(t, response.MFASetupRequired)
	assert.NotEmpty(t, response.MFAToken)
}

func Test_ResetMFAByEmail_ClearsMFAAndReportsPriorState(t *testing.T) {
	users_services.GetUserService().SetAuditLogWriter(&AuditLogWriterStub{})
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	repo := &users_repositories.UserRepository{}

	wasEnabled, err := users_services.GetUserService().ResetMFAByEmail(admin.Email)
	require.NoError(t, err)
	assert.True(t, wasEnabled)

	user, err := repo.GetUserByEmail(admin.Email)
	require.NoError(t, err)
	assert.False(t, user.MFAEnabled)
	assert.Nil(t, user.MFASecretEncrypted)

	wasEnabled, err = users_services.GetUserService().ResetMFAByEmail(admin.Email)
	require.NoError(t, err)
	assert.False(t, wasEnabled, "a second reset finds MFA already off")

	_, err = users_services.GetUserService().ResetMFAByEmail("nobody-" + uuid.New().String()[:8] + "@example.com")
	assert.ErrorContains(t, err, "no user found")
}

// A control-plane session satisfies the Site's MFA rule only when the
// assertion said the user passed MFA there (amr contains "mfa").
func Test_RequireMFA_ControlPlaneSession_HonoursAMR(t *testing.T) {
	gate := createMFAGateRouter()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	repo := &users_repositories.UserRepository{}
	require.NoError(t, repo.ClearMFA(admin.UserID))
	user, err := repo.GetUserByID(admin.UserID)
	require.NoError(t, err)

	sessionWith := func(amr []string) string {
		response, err := users_services.GetUserService().GenerateAccessTokenWithOptions(user,
			users_services.AccessTokenOptions{ExtraClaims: map[string]any{
				users_services.AuthOriginClaim: users_services.AuthOriginControlPlane, "amr": amr,
			}})
		require.NoError(t, err)
		return response.Token
	}

	test_utils.MakeGetRequest(t, gate, "/api/v1/probe", "Bearer "+sessionWith([]string{"pwd"}), http.StatusForbidden)
	test_utils.MakeGetRequest(t, gate, "/api/v1/probe", "Bearer "+sessionWith([]string{"mfa"}), http.StatusOK)
}
