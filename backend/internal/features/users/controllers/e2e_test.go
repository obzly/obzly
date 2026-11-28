package users_controllers

import (
	"net/http"
	"testing"
	"time"

	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	mfa_controllers "obzly/internal/features/users/mfa/controllers"
	mfa_dto "obzly/internal/features/users/mfa/dto"
	mfa_services "obzly/internal/features/users/mfa/services"
	users_middleware "obzly/internal/features/users/middleware"
	users_services "obzly/internal/features/users/services"
	sso_controllers "obzly/internal/features/users/sso/controllers"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_AdminLifecycleE2E_CompletesSuccessfully(t *testing.T) {
	router := createE2ETestRouter()

	bootstrapToken := users_testing.RecreateInitialAdmin()

	adminPasswordRequest := users_dto.SetAdminPasswordRequestDTO{
		Password:       "adminpassword123",
		BootstrapToken: bootstrapToken,
	}

	test_utils.MakePostRequest(t, router, "/api/v1/users/admin/set-password", "", adminPasswordRequest, http.StatusOK)

	adminSigninRequest := users_dto.SignInRequestDTO{
		Email:    "admin",
		Password: "adminpassword123",
	}

	var adminSigninResponse users_dto.SignInResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t,
		router,
		"/api/v1/users/signin",
		"",
		adminSigninRequest,
		http.StatusOK,
		&adminSigninResponse,
	)

	// MFA is required for the root admin: complete the forced setup with a real TOTP code.
	assert.True(t, adminSigninResponse.MFASetupRequired)
	assert.Empty(t, adminSigninResponse.Token)
	adminSigninResponse = completeForcedMFASetup(t, router, adminSigninResponse.MFAToken)
	assert.NotEmpty(t, adminSigninResponse.Token)

	projectID := uuid.New()
	projectRole := users_enums.ProjectRoleAdmin
	invitedUserEmail := "invited" + uuid.New().String() + "@example.com"
	inviteRequest := users_dto.InviteUserRequestDTO{
		Email:               invitedUserEmail,
		IntendedProjectID:   &projectID,
		IntendedProjectRole: &projectRole,
	}
	var inviteResponse users_dto.InviteUserResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t,
		router,
		"/api/v1/users/invite",
		"Bearer "+adminSigninResponse.Token,
		inviteRequest,
		http.StatusOK,
		&inviteResponse,
	)
	assert.NotEmpty(t, inviteResponse.InviteToken)

	// Sign up with the relayed invite token; the email alone can't claim the account.
	userSignupRequest := users_dto.SignUpRequestDTO{
		Email:       invitedUserEmail,
		Password:    "userpassword123",
		Name:        "Invited User",
		InviteToken: inviteResponse.InviteToken,
	}
	test_utils.MakePostRequest(t, router, "/api/v1/users/signup", "", userSignupRequest, http.StatusOK)

	userSigninRequest := users_dto.SignInRequestDTO{
		Email:    invitedUserEmail,
		Password: "userpassword123",
	}

	var userSigninResponse users_dto.SignInResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t,
		router,
		"/api/v1/users/signin",
		"",
		userSigninRequest,
		http.StatusOK,
		&userSigninResponse,
	)

	var listUsersResponse users_dto.ListUsersResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t,
		router,
		"/api/v1/users",
		"Bearer "+adminSigninResponse.Token,
		http.StatusOK,
		&listUsersResponse,
	)
	assert.GreaterOrEqual(t, len(listUsersResponse.Users), 2) // Admin + new user
}

func Test_UserLifecycleE2E_CompletesSuccessfully(t *testing.T) {
	router := createE2ETestRouter()
	users_testing.ResetSettingsToDefaults()

	userEmail := "testuser" + uuid.New().String() + "@example.com"
	userSignupRequest := users_dto.SignUpRequestDTO{
		Email:    userEmail,
		Password: "userpassword123",
		Name:     "Test User",
	}

	test_utils.MakePostRequest(t, router, "/api/v1/users/signup", "", userSignupRequest, http.StatusOK)

	// The first sign-in goes through MFA setup, as for every role.
	signinResponse, _ := signInAndSetUpMFA(t, router, userEmail, "userpassword123")
	assert.NotEmpty(t, signinResponse.Token)
	assert.NotEqual(t, uuid.Nil, signinResponse.UserID)

	var profileResponse users_dto.UserProfileResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t,
		router,
		"/api/v1/users/"+signinResponse.UserID.String(),
		"Bearer "+signinResponse.Token,
		http.StatusOK,
		&profileResponse,
	)
	assert.Equal(t, signinResponse.UserID, profileResponse.ID)
	assert.Equal(t, userEmail, profileResponse.Email)
	assert.Equal(t, users_enums.UserRoleMember, profileResponse.Role)
	assert.True(t, profileResponse.IsActive)
}

func createUserTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	v1 := router.Group("/api/v1")

	GetUserController().RegisterRoutes(v1)

	protected := v1.Group("").Use(users_middleware.AuthMiddleware(users_services.GetUserService()))
	GetUserController().RegisterProtectedRoutes(protected.(*gin.RouterGroup))

	users_services.GetUserService().SetAuditLogWriter(&AuditLogWriterStub{})

	return router
}

func createSettingsTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	v1 := router.Group("/api/v1")

	protected := v1.Group("").Use(users_middleware.AuthMiddleware(users_services.GetUserService()))
	GetSettingsController().RegisterRoutes(protected.(*gin.RouterGroup))

	users_services.GetUserService().SetAuditLogWriter(&AuditLogWriterStub{})
	users_services.GetSettingsService().SetAuditLogWriter(&AuditLogWriterStub{})
	users_services.GetManagementService().SetAuditLogWriter(&AuditLogWriterStub{})

	return router
}

func createManagementTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	v1 := router.Group("/api/v1")

	protected := v1.Group("").Use(users_middleware.AuthMiddleware(users_services.GetUserService()))
	GetManagementController().RegisterRoutes(protected.(*gin.RouterGroup))

	users_services.GetUserService().SetAuditLogWriter(&AuditLogWriterStub{})
	users_services.GetSettingsService().SetAuditLogWriter(&AuditLogWriterStub{})
	users_services.GetManagementService().SetAuditLogWriter(&AuditLogWriterStub{})

	return router
}

func createSSOTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	v1 := router.Group("/api/v1")

	// Public routes: signup/signin + /auth/sso/connectors + /auth/sso/callback
	GetUserController().RegisterRoutes(v1)

	// Protected routes: admin-gated /sso/connectors CRUD
	protected := v1.Group("").Use(users_middleware.AuthMiddleware(users_services.GetUserService()))
	sso_controllers.GetConnectorController().RegisterRoutes(protected.(*gin.RouterGroup))

	users_services.GetUserService().SetAuditLogWriter(&AuditLogWriterStub{})

	return router
}

func createE2ETestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	v1 := router.Group("/api/v1")

	// Register all routes
	GetUserController().RegisterRoutes(v1)
	mfa_controllers.GetMFAController().RegisterPublicRoutes(v1)

	protected := v1.Group("").Use(users_middleware.AuthMiddleware(users_services.GetUserService()))
	protectedGroup := protected.(*gin.RouterGroup)

	// MFA routes and the enforcement middleware go first, as in cmd/main.go.
	mfa_controllers.GetMFAController().RegisterProtectedRoutes(protectedGroup)
	protectedGroup.Use(users_middleware.RequireMFAIfEnforced())

	GetUserController().RegisterProtectedRoutes(protectedGroup)
	GetSettingsController().RegisterRoutes(protectedGroup)
	GetManagementController().RegisterRoutes(protectedGroup)

	users_services.GetUserService().SetAuditLogWriter(&AuditLogWriterStub{})
	users_services.GetSettingsService().SetAuditLogWriter(&AuditLogWriterStub{})
	users_services.GetManagementService().SetAuditLogWriter(&AuditLogWriterStub{})

	return router
}

// completeForcedMFASetup fetches a secret and confirms it with a real TOTP code,
// returning the access token that completes login.
func completeForcedMFASetup(t *testing.T, router *gin.Engine, mfaToken string) users_dto.SignInResponseDTO {
	t.Helper()

	var setupResponse mfa_dto.SetupResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/users/mfa/login-setup", "",
		mfa_dto.LoginSetupRequestDTO{MFAToken: mfaToken},
		http.StatusOK, &setupResponse,
	)
	require.NotEmpty(t, setupResponse.Secret)

	code, err := totp.GenerateCode(setupResponse.Secret, time.Now())
	require.NoError(t, err)

	var signInResponse users_dto.SignInResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/users/mfa/login-enable", "",
		mfa_dto.LoginEnableRequestDTO{MFAToken: mfaToken, Code: code},
		http.StatusOK, &signInResponse,
	)

	return signInResponse
}

// signInAndSetUpMFA signs in with a password and completes the MFA setup every
// account goes through at its first sign-in. The secret is returned for later codes.
func signInAndSetUpMFA(
	t *testing.T, router *gin.Engine, email, password string,
) (users_dto.SignInResponseDTO, string) {
	t.Helper()

	var signIn users_dto.SignInResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/users/signin", "",
		users_dto.SignInRequestDTO{Email: email, Password: password},
		http.StatusOK, &signIn,
	)
	require.True(t, signIn.MFASetupRequired, "a first sign-in goes through MFA setup")
	require.Empty(t, signIn.Token)
	return finishMFASetup(t, signIn.MFAToken)
}

// signInWithMFACode signs in an account that has MFA with a password and a code.
func signInWithMFACode(t *testing.T, router *gin.Engine, email, password, code string) users_dto.SignInResponseDTO {
	t.Helper()

	var signIn users_dto.SignInResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/users/signin", "",
		users_dto.SignInRequestDTO{Email: email, Password: password},
		http.StatusOK, &signIn,
	)
	require.True(t, signIn.MFARequired)
	require.Empty(t, signIn.Token)
	return finishMFALogin(t, signIn.MFAToken, code)
}

// finishMFASetup completes a setup challenge (password, OAuth or SSO sign-in)
// through the service, so it works on routers without the MFA routes.
func finishMFASetup(t *testing.T, mfaToken string) (users_dto.SignInResponseDTO, string) {
	t.Helper()

	setup, err := mfa_services.GetMFAService().SetupWithToken(mfaToken)
	require.NoError(t, err)
	code, err := totp.GenerateCode(setup.Secret, time.Now())
	require.NoError(t, err)
	user, err := mfa_services.GetMFAService().EnableWithToken(mfaToken, code)
	require.NoError(t, err)

	session, err := users_services.GetUserService().GenerateAccessToken(user)
	require.NoError(t, err)
	session.Email = user.Email
	return *session, setup.Secret
}

// finishMFALogin completes a login challenge with code, like finishMFASetup.
func finishMFALogin(t *testing.T, mfaToken, code string) users_dto.SignInResponseDTO {
	t.Helper()

	user, err := mfa_services.GetMFAService().VerifyLoginToken(mfaToken, code)
	require.NoError(t, err)
	session, err := users_services.GetUserService().GenerateAccessToken(user)
	require.NoError(t, err)
	session.Email = user.Email
	return *session
}

// nextTOTPCode is the next 30 s step's code: a step already used (by setup,
// say) is never accepted again.
func nextTOTPCode(t *testing.T, secret string) string {
	t.Helper()
	code, err := totp.GenerateCodeCustom(secret, time.Now().Add(30*time.Second),
		totp.ValidateOpts{Period: 30, Skew: 1, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	require.NoError(t, err)
	return code
}

type AuditLogWriterStub struct{}

func (a *AuditLogWriterStub) WriteAuditLog(message string, userID *uuid.UUID, projectID *uuid.UUID) {
	// do nothing
}
