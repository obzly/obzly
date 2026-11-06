package users_controllers

import (
	"net/http"
	"testing"
	"time"

	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	mfa_dto "obzly/internal/features/users/mfa/dto"
	users_models "obzly/internal/features/users/models"
	users_repositories "obzly/internal/features/users/repositories"
	users_services "obzly/internal/features/users/services"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// createUserWithPassword creates an active user with a known password and, unlike
// CreateTestUser, leaves MFA as a new account has it.
func createUserWithPassword(t *testing.T, role users_enums.UserRole, password string) *users_models.User {
	t.Helper()

	hashedPasswordBytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	require.NoError(t, err)
	hashedPassword := string(hashedPasswordBytes)

	user := &users_models.User{
		ID:                   uuid.New(),
		Email:                "mfa-e2e-" + uuid.New().String()[:8] + "@example.com",
		Name:                 "MFA E2E User",
		HashedPassword:       &hashedPassword,
		PasswordCreationTime: time.Now().UTC(),
		Role:                 role,
		Status:               users_enums.UserStatusActive,
		CreatedAt:            time.Now().UTC(),
	}

	repo := &users_repositories.UserRepository{}
	require.NoError(t, repo.CreateUser(user))
	return user
}

func Test_SignIn_AdminWithoutMFA_ReturnsSetupRequired(t *testing.T) {
	router := createE2ETestRouter()
	user := createUserWithPassword(t, users_enums.UserRoleAdmin, "adminpass123")

	var response users_dto.SignInResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/users/signin", "",
		users_dto.SignInRequestDTO{Email: user.Email, Password: "adminpass123"},
		http.StatusOK, &response,
	)

	assert.True(t, response.MFASetupRequired)
	assert.False(t, response.MFARequired)
	assert.Empty(t, response.Token)
	assert.NotEmpty(t, response.MFAToken)
}

func Test_SignIn_MemberWithoutMFA_ReturnsRealTokenImmediately(t *testing.T) {
	router := createE2ETestRouter()
	user := createUserWithPassword(t, users_enums.UserRoleMember, "memberpass123")

	var response users_dto.SignInResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/users/signin", "",
		users_dto.SignInRequestDTO{Email: user.Email, Password: "memberpass123"},
		http.StatusOK, &response,
	)

	assert.False(t, response.MFASetupRequired)
	assert.False(t, response.MFARequired)
	assert.NotEmpty(t, response.Token)
}

func Test_SignIn_MemberWithoutMFA_WhenMFARequiredForEveryone_ReturnsSetupRequired(t *testing.T) {
	router := createE2ETestRouter()
	user := createUserWithPassword(t, users_enums.UserRoleMember, "memberpass123")
	// The setting is shared by every test package's database, so stub it instead.
	users_models.SetMFAForEveryone(func() bool { return true })
	t.Cleanup(func() {
		users_models.SetMFAForEveryone(users_services.GetSettingsService().MFARequiredForEveryone)
	})

	var response users_dto.SignInResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/users/signin", "",
		users_dto.SignInRequestDTO{Email: user.Email, Password: "memberpass123"},
		http.StatusOK, &response,
	)

	assert.True(t, response.MFASetupRequired)
	assert.Empty(t, response.Token)
}

func Test_Admin_BlockedFromOtherRoutes_UntilMFASetupComplete(t *testing.T) {
	router := createE2ETestRouter()
	user := createUserWithPassword(t, users_enums.UserRoleAdmin, "adminpass123")

	var signIn users_dto.SignInResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/users/signin", "",
		users_dto.SignInRequestDTO{Email: user.Email, Password: "adminpass123"},
		http.StatusOK, &signIn,
	)
	require.True(t, signIn.MFASetupRequired)

	// Protected routes need a real access token, so finish the forced setup flow.
	signIn = completeForcedMFASetup(t, router, signIn.MFAToken)
	require.NotEmpty(t, signIn.Token)

	var listUsersResponse users_dto.ListUsersResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t, router, "/api/v1/users", "Bearer "+signIn.Token,
		http.StatusOK, &listUsersResponse,
	)
}

func Test_Admin_WithRealAccessTokenButNoMFA_BlockedFromOtherRoutes(t *testing.T) {
	// A valid token for an Admin without MFA (promoted, or predating enforcement)
	// is refused everywhere except the MFA routes.
	router := createE2ETestRouter()
	adminUser := createUserWithPassword(t, users_enums.UserRoleAdmin, "adminpass123")

	repo := &users_repositories.UserRepository{}
	reloaded, err := repo.GetUserByID(adminUser.ID)
	require.NoError(t, err)

	token, err := users_services.GetUserService().GenerateAccessToken(reloaded)
	require.NoError(t, err)

	resp := test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method:         "GET",
		URL:            "/api/v1/users",
		AuthToken:      "Bearer " + token.Token,
		ExpectedStatus: http.StatusForbidden,
	})
	assert.Contains(t, string(resp.Body), "mfaSetupRequired")

	test_utils.MakeGetRequest(t, router, "/api/v1/users/mfa/status", "Bearer "+token.Token, http.StatusOK)
}

func Test_ChallengeToken_CannotBeUsedAsRealAccessToken(t *testing.T) {
	router := createE2ETestRouter()
	user := createUserWithPassword(t, users_enums.UserRoleAdmin, "adminpass123")

	var signIn users_dto.SignInResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/users/signin", "",
		users_dto.SignInRequestDTO{Email: user.Email, Password: "adminpass123"},
		http.StatusOK, &signIn,
	)
	require.NotEmpty(t, signIn.MFAToken)

	// The challenge token must be refused outright, not accepted then blocked.
	test_utils.MakeGetRequest(t, router, "/api/v1/users", "Bearer "+signIn.MFAToken, http.StatusUnauthorized)
}

func Test_AdminResetsAnotherUsersMFA_ThenTheyCanSignInWithoutACode(t *testing.T) {
	router := createE2ETestRouter()
	adminAccess := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	member := createUserWithPassword(t, users_enums.UserRoleMember, "memberpass123")

	repo := &users_repositories.UserRepository{}
	reloaded, err := repo.GetUserByID(member.ID)
	require.NoError(t, err)
	memberToken, err := users_services.GetUserService().GenerateAccessToken(reloaded)
	require.NoError(t, err)

	var setup mfa_dto.SetupResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/users/mfa/setup", "Bearer "+memberToken.Token, nil, http.StatusOK, &setup,
	)
	code, err := totp.GenerateCode(setup.Secret, time.Now())
	require.NoError(t, err)
	test_utils.MakePostRequest(
		t, router, "/api/v1/users/mfa/enable", "Bearer "+memberToken.Token,
		mfa_dto.EnableRequestDTO{Code: code}, http.StatusOK,
	)

	test_utils.MakePostRequest(
		t, router, "/api/v1/users/"+member.ID.String()+"/reset-mfa",
		"Bearer "+adminAccess.Token, nil, http.StatusOK,
	)

	var signIn users_dto.SignInResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/users/signin", "",
		users_dto.SignInRequestDTO{Email: member.Email, Password: "memberpass123"},
		http.StatusOK, &signIn,
	)
	assert.False(t, signIn.MFARequired)
	assert.NotEmpty(t, signIn.Token)
}

func Test_ResetUserMFA_WhenNotAdmin_Forbidden(t *testing.T) {
	router := createE2ETestRouter()
	memberAccess := users_testing.CreateTestUser(users_enums.UserRoleMember)
	otherUser := createUserWithPassword(t, users_enums.UserRoleMember, "memberpass123")

	test_utils.MakePostRequest(
		t, router, "/api/v1/users/"+otherUser.ID.String()+"/reset-mfa",
		"Bearer "+memberAccess.Token, nil, http.StatusForbidden,
	)
}

// A non-root admin resetting an admin's MFA could enroll their own authenticator
// and sign in as them.
func Test_ResetUserMFA_WhenNonRootAdminTargetsAdmin_Forbidden(t *testing.T) {
	router := createE2ETestRouter()
	nonRootAdmin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	targetAdmin := createUserWithPassword(t, users_enums.UserRoleAdmin, "adminpass123")

	resp := test_utils.MakePostRequest(
		t, router, "/api/v1/users/"+targetAdmin.ID.String()+"/reset-mfa",
		"Bearer "+nonRootAdmin.Token, nil, http.StatusBadRequest,
	)
	assert.Contains(t, string(resp.Body), "root admin")
}

func Test_ResetUserMFA_WhenRootAdminTargetsAdmin_Succeeds(t *testing.T) {
	router := createE2ETestRouter()
	rootAdmin := users_testing.ReacreateInitAdminAndGetAccess()
	targetAdmin := createUserWithPassword(t, users_enums.UserRoleAdmin, "adminpass123")
	require.NoError(t, (&users_repositories.UserRepository{}).EnableMFA(targetAdmin.ID, 0))

	test_utils.MakePostRequest(
		t, router, "/api/v1/users/"+targetAdmin.ID.String()+"/reset-mfa",
		"Bearer "+rootAdmin.Token, nil, http.StatusOK,
	)
}

func Test_Member_CanOptInToMFA_ThenMustVerifyOnNextSignIn(t *testing.T) {
	router := createE2ETestRouter()
	user := createUserWithPassword(t, users_enums.UserRoleMember, "memberpass123")

	repo := &users_repositories.UserRepository{}
	reloaded, err := repo.GetUserByID(user.ID)
	require.NoError(t, err)
	token, err := users_services.GetUserService().GenerateAccessToken(reloaded)
	require.NoError(t, err)

	var setup mfa_dto.SetupResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/users/mfa/setup", "Bearer "+token.Token, nil, http.StatusOK, &setup,
	)
	code, err := totp.GenerateCode(setup.Secret, time.Now())
	require.NoError(t, err)
	test_utils.MakePostRequest(
		t, router, "/api/v1/users/mfa/enable", "Bearer "+token.Token,
		mfa_dto.EnableRequestDTO{Code: code}, http.StatusOK,
	)

	// Signing in again must now require a code, not return a token directly.
	var signIn users_dto.SignInResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/users/signin", "",
		users_dto.SignInRequestDTO{Email: user.Email, Password: "memberpass123"},
		http.StatusOK, &signIn,
	)
	assert.True(t, signIn.MFARequired)
	assert.Empty(t, signIn.Token)

	loginCode, err := totp.GenerateCodeCustom(
		setup.Secret, time.Now().Add(30*time.Second),
		totp.ValidateOpts{Period: 30, Skew: 1, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1},
	)
	require.NoError(t, err)

	var verifyResponse users_dto.SignInResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/users/mfa/login-verify", "",
		mfa_dto.LoginVerifyRequestDTO{MFAToken: signIn.MFAToken, Code: loginCode},
		http.StatusOK, &verifyResponse,
	)
	assert.NotEmpty(t, verifyResponse.Token)
}

// Repeated wrong codes lock the account out, so a known password can't be
// paired with brute-forcing the 6-digit code.
func Test_LoginVerify_AfterRepeatedWrongCodes_LocksOutAccount(t *testing.T) {
	router := createE2ETestRouter()
	user := createUserWithPassword(t, users_enums.UserRoleMember, "memberpass123")

	repo := &users_repositories.UserRepository{}
	reloaded, err := repo.GetUserByID(user.ID)
	require.NoError(t, err)
	token, err := users_services.GetUserService().GenerateAccessToken(reloaded)
	require.NoError(t, err)

	var setup mfa_dto.SetupResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/users/mfa/setup", "Bearer "+token.Token, nil, http.StatusOK, &setup,
	)
	code, err := totp.GenerateCode(setup.Secret, time.Now())
	require.NoError(t, err)
	test_utils.MakePostRequest(
		t, router, "/api/v1/users/mfa/enable", "Bearer "+token.Token,
		mfa_dto.EnableRequestDTO{Code: code}, http.StatusOK,
	)

	var signIn users_dto.SignInResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/users/signin", "",
		users_dto.SignInRequestDTO{Email: user.Email, Password: "memberpass123"},
		http.StatusOK, &signIn,
	)
	require.True(t, signIn.MFARequired)

	// 4 wrong codes in a row - each still reports "invalid code", not lockout.
	for i := range 4 {
		resp := test_utils.MakePostRequest(
			t, router, "/api/v1/users/mfa/login-verify", "",
			mfa_dto.LoginVerifyRequestDTO{MFAToken: signIn.MFAToken, Code: "000000"},
			http.StatusBadRequest,
		)
		assert.NotContains(t, string(resp.Body), "too many", "attempt %d should not be locked out yet", i+1)
	}

	// The 5th wrong code is itself reported as locked out.
	resp := test_utils.MakePostRequest(
		t, router, "/api/v1/users/mfa/login-verify", "",
		mfa_dto.LoginVerifyRequestDTO{MFAToken: signIn.MFAToken, Code: "000000"},
		http.StatusTooManyRequests,
	)
	assert.Contains(t, string(resp.Body), "too many")

	// Even the now-CORRECT code is refused while locked out.
	loginCode, err := totp.GenerateCodeCustom(
		setup.Secret, time.Now().Add(30*time.Second),
		totp.ValidateOpts{Period: 30, Skew: 1, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1},
	)
	require.NoError(t, err)
	resp = test_utils.MakePostRequest(
		t, router, "/api/v1/users/mfa/login-verify", "",
		mfa_dto.LoginVerifyRequestDTO{MFAToken: signIn.MFAToken, Code: loginCode},
		http.StatusTooManyRequests,
	)
	assert.Contains(t, string(resp.Body), "too many")
}

func Test_LoginVerify_SuccessAfterFewWrongCodes_ResetsAttemptCounter(t *testing.T) {
	router := createE2ETestRouter()
	user := createUserWithPassword(t, users_enums.UserRoleMember, "memberpass123")

	repo := &users_repositories.UserRepository{}
	reloaded, err := repo.GetUserByID(user.ID)
	require.NoError(t, err)
	token, err := users_services.GetUserService().GenerateAccessToken(reloaded)
	require.NoError(t, err)

	var setup mfa_dto.SetupResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/users/mfa/setup", "Bearer "+token.Token, nil, http.StatusOK, &setup,
	)
	code, err := totp.GenerateCode(setup.Secret, time.Now())
	require.NoError(t, err)
	test_utils.MakePostRequest(
		t, router, "/api/v1/users/mfa/enable", "Bearer "+token.Token,
		mfa_dto.EnableRequestDTO{Code: code}, http.StatusOK,
	)

	var signIn users_dto.SignInResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/users/signin", "",
		users_dto.SignInRequestDTO{Email: user.Email, Password: "memberpass123"},
		http.StatusOK, &signIn,
	)

	for range 3 {
		test_utils.MakePostRequest(
			t, router, "/api/v1/users/mfa/login-verify", "",
			mfa_dto.LoginVerifyRequestDTO{MFAToken: signIn.MFAToken, Code: "000000"},
			http.StatusBadRequest,
		)
	}

	loginCode, err := totp.GenerateCodeCustom(
		setup.Secret, time.Now().Add(30*time.Second),
		totp.ValidateOpts{Period: 30, Skew: 1, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1},
	)
	require.NoError(t, err)
	var verifyResponse users_dto.SignInResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/users/mfa/login-verify", "",
		mfa_dto.LoginVerifyRequestDTO{MFAToken: signIn.MFAToken, Code: loginCode},
		http.StatusOK, &verifyResponse,
	)
	assert.NotEmpty(t, verifyResponse.Token)
}
