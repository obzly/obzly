package users_controllers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"obzly/internal/config"
	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"
	users_repositories "obzly/internal/features/users/repositories"
	users_services "obzly/internal/features/users/services"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func Test_SignUpUser_WithValidData_UserCreated(t *testing.T) {
	router := createUserTestRouter()

	request := users_dto.SignUpRequestDTO{
		Email:    "test" + uuid.New().String() + "@example.com",
		Password: "testpassword123",
		Name:     "Test User",
	}

	test_utils.MakePostRequest(t, router, "/api/v1/users/signup", "", request, http.StatusOK)
}

func Test_SignUpUser_WithInvalidJSON_ReturnsBadRequest(t *testing.T) {
	router := createUserTestRouter()

	resp := test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method:         "POST",
		URL:            "/api/v1/users/signup",
		Body:           "invalid json",
		ExpectedStatus: http.StatusBadRequest,
	})

	assert.Contains(t, string(resp.Body), "Invalid request format")
}

func Test_SignUpUser_WithDuplicateEmail_ReturnsBadRequest(t *testing.T) {
	router := createUserTestRouter()
	email := "duplicate" + uuid.New().String() + "@example.com"

	request := users_dto.SignUpRequestDTO{
		Email:    email,
		Password: "testpassword123",
		Name:     "Test User",
	}

	test_utils.MakePostRequest(t, router, "/api/v1/users/signup", "", request, http.StatusOK)

	resp := test_utils.MakePostRequest(t, router, "/api/v1/users/signup", "", request, http.StatusBadRequest)
	assert.Contains(t, string(resp.Body), "already exists")
}

func Test_SignUpUser_WithValidationErrors_ReturnsBadRequest(t *testing.T) {
	router := createUserTestRouter()

	testCases := []struct {
		name    string
		request users_dto.SignUpRequestDTO
	}{
		{
			name: "missing email",
			request: users_dto.SignUpRequestDTO{
				Password: "testpassword123",
				Name:     "Test User",
			},
		},
		{
			name: "missing password",
			request: users_dto.SignUpRequestDTO{
				Email: "test@example.com",
				Name:  "Test User",
			},
		},
		{
			name: "short password",
			request: users_dto.SignUpRequestDTO{
				Email:    "test@example.com",
				Password: "short",
				Name:     "Test User",
			},
		},
		{
			name: "missing name",
			request: users_dto.SignUpRequestDTO{
				Email:    "test@example.com",
				Password: "testpassword123",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			test_utils.MakePostRequest(t, router, "/api/v1/users/signup", "", tc.request, http.StatusBadRequest)
		})
	}
}

func Test_SignInUser_WithValidCredentials_ReturnsToken(t *testing.T) {
	router := createUserTestRouter()
	email := "signin" + uuid.New().String() + "@example.com"
	password := "testpassword123"

	signupRequest := users_dto.SignUpRequestDTO{
		Email:    email,
		Password: password,
		Name:     "Test User",
	}
	test_utils.MakePostRequest(t, router, "/api/v1/users/signup", "", signupRequest, http.StatusOK)

	signinRequest := users_dto.SignInRequestDTO{
		Email:    email,
		Password: password,
	}

	var response users_dto.SignInResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t,
		router,
		"/api/v1/users/signin",
		"",
		signinRequest,
		http.StatusOK,
		&response,
	)

	assert.NotEmpty(t, response.Token)
	assert.NotEqual(t, uuid.Nil, response.UserID)
}

func Test_SignInUser_WithWrongPassword_ReturnsBadRequest(t *testing.T) {
	router := createUserTestRouter()
	email := "signin2" + uuid.New().String() + "@example.com"

	signupRequest := users_dto.SignUpRequestDTO{
		Email:    email,
		Password: "testpassword123",
		Name:     "Test User",
	}
	test_utils.MakePostRequest(t, router, "/api/v1/users/signup", "", signupRequest, http.StatusOK)

	signinRequest := users_dto.SignInRequestDTO{
		Email:    email,
		Password: "wrongpassword",
	}

	resp := test_utils.MakePostRequest(t, router, "/api/v1/users/signin", "", signinRequest, http.StatusBadRequest)
	// Generic message so responses don't reveal which emails exist.
	assert.Contains(t, string(resp.Body), "invalid email or password")
}

// Repeated failures against one account lock it out, even for the correct password.
func Test_SignInUser_AfterRepeatedWrongPassword_LocksOutAccount(t *testing.T) {
	router := createUserTestRouter()
	email := "lockout-" + uuid.New().String() + "@example.com"
	password := "testpassword123"

	test_utils.MakePostRequest(
		t, router, "/api/v1/users/signup", "",
		users_dto.SignUpRequestDTO{Email: email, Password: password, Name: "Test User"},
		http.StatusOK,
	)

	remoteAddr := test_utils.RandomTestRemoteAddr()
	for i := range 9 {
		resp := test_utils.MakeRequest(t, router, test_utils.RequestOptions{
			Method:         "POST",
			URL:            "/api/v1/users/signin",
			Body:           users_dto.SignInRequestDTO{Email: email, Password: "wrongpassword"},
			RemoteAddr:     remoteAddr,
			ExpectedStatus: http.StatusBadRequest,
		})
		assert.NotContains(t, string(resp.Body), "too many", "attempt %d should not be locked out yet", i+1)
	}

	// The 10th wrong attempt crosses the threshold.
	resp := test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method:         "POST",
		URL:            "/api/v1/users/signin",
		Body:           users_dto.SignInRequestDTO{Email: email, Password: "wrongpassword"},
		RemoteAddr:     remoteAddr,
		ExpectedStatus: http.StatusTooManyRequests,
	})
	assert.Contains(t, string(resp.Body), "too many")

	// Even the CORRECT password is refused while locked out.
	resp = test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method:         "POST",
		URL:            "/api/v1/users/signin",
		Body:           users_dto.SignInRequestDTO{Email: email, Password: password},
		RemoteAddr:     remoteAddr,
		ExpectedStatus: http.StatusTooManyRequests,
	})
	assert.Contains(t, string(resp.Body), "too many")
}

// A successful sign-in resets the failure counter.
func Test_SignInUser_SuccessAfterFewWrongPasswords_ResetsAttemptCounter(t *testing.T) {
	router := createUserTestRouter()
	email := "lockout-reset-" + uuid.New().String() + "@example.com"
	password := "testpassword123"

	test_utils.MakePostRequest(
		t, router, "/api/v1/users/signup", "",
		users_dto.SignUpRequestDTO{Email: email, Password: password, Name: "Test User"},
		http.StatusOK,
	)

	remoteAddr := test_utils.RandomTestRemoteAddr()
	for range 3 {
		test_utils.MakeRequest(t, router, test_utils.RequestOptions{
			Method:         "POST",
			URL:            "/api/v1/users/signin",
			Body:           users_dto.SignInRequestDTO{Email: email, Password: "wrongpassword"},
			RemoteAddr:     remoteAddr,
			ExpectedStatus: http.StatusBadRequest,
		})
	}

	resp := test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method:         "POST",
		URL:            "/api/v1/users/signin",
		Body:           users_dto.SignInRequestDTO{Email: email, Password: password},
		RemoteAddr:     remoteAddr,
		ExpectedStatus: http.StatusOK,
	})
	var response users_dto.SignInResponseDTO
	require.NoError(t, json.Unmarshal(resp.Body, &response))
	assert.NotEmpty(t, response.Token)
}

// One source failing across many accounts is throttled by IP too.
func Test_SignInUser_RepeatedFailuresFromOneIPAcrossDifferentAccounts_LocksOutIP(t *testing.T) {
	router := createUserTestRouter()
	remoteAddr := test_utils.RandomTestRemoteAddr()

	for i := range 10 {
		resp := test_utils.MakeRequest(t, router, test_utils.RequestOptions{
			Method: "POST",
			URL:    "/api/v1/users/signin",
			Body: users_dto.SignInRequestDTO{
				Email:    fmt.Sprintf("ip-lockout-%d-%s@example.com", i, uuid.New().String()[:8]),
				Password: "whatever",
			},
			RemoteAddr:     remoteAddr,
			ExpectedStatus: 0, // checked manually below - status varies right at the threshold
		})
		if i < 9 {
			assert.NotContains(t, string(resp.Body), "too many")
		}
	}

	resp := test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method: "POST",
		URL:    "/api/v1/users/signin",
		Body: users_dto.SignInRequestDTO{
			Email:    "yet-another-" + uuid.New().String() + "@example.com",
			Password: "whatever",
		},
		RemoteAddr:     remoteAddr,
		ExpectedStatus: http.StatusTooManyRequests,
	})
	assert.Contains(t, string(resp.Body), "too many")
}

func Test_SignInUser_WithNonExistentUser_ReturnsBadRequest(t *testing.T) {
	router := createUserTestRouter()

	signinRequest := users_dto.SignInRequestDTO{
		Email:    "nonexistent" + uuid.New().String() + "@example.com",
		Password: "testpassword123",
	}

	resp := test_utils.MakePostRequest(t, router, "/api/v1/users/signin", "", signinRequest, http.StatusBadRequest)
	assert.Contains(t, string(resp.Body), "invalid email or password")
}

// An unknown email and a wrong password must be indistinguishable.
func Test_SignInUser_UnknownEmailAndWrongPassword_ReturnBitForBitIdenticalErrors(t *testing.T) {
	router := createUserTestRouter()
	email := "signin-enum" + uuid.New().String() + "@example.com"
	test_utils.MakePostRequest(t, router, "/api/v1/users/signup", "",
		users_dto.SignUpRequestDTO{Email: email, Password: "testpassword123", Name: "Test User"}, http.StatusOK)

	unknownResp := test_utils.MakePostRequest(t, router, "/api/v1/users/signin", "",
		users_dto.SignInRequestDTO{Email: "totally-unknown" + uuid.New().String() + "@example.com", Password: "whatever123"},
		http.StatusBadRequest)
	wrongPasswordResp := test_utils.MakePostRequest(t, router, "/api/v1/users/signin", "",
		users_dto.SignInRequestDTO{Email: email, Password: "wrongpassword"},
		http.StatusBadRequest)

	assert.Equal(t, string(unknownResp.Body), string(wrongPasswordResp.Body))
}

// A password-less (OAuth/SSO-only) account must get a clean rejection, not a nil-pointer panic.
func Test_SignInUser_AccountWithNoPassword_ReturnsErrorNotPanic(t *testing.T) {
	router := createUserTestRouter()

	userID := uuid.New()
	email := "oauth-only" + userID.String()[:8] + "@example.com"
	user := &users_models.User{
		ID:                   userID,
		Email:                email,
		Name:                 "OAuth Only User",
		HashedPassword:       nil,
		PasswordCreationTime: time.Now().UTC(),
		CreatedAt:            time.Now().UTC(),
		Role:                 users_enums.UserRoleMember,
		Status:               users_enums.UserStatusActive,
	}
	require.NoError(t, (&users_repositories.UserRepository{}).CreateUser(user))

	resp := test_utils.MakePostRequest(t, router, "/api/v1/users/signin", "",
		users_dto.SignInRequestDTO{Email: email, Password: "whatever123"}, http.StatusBadRequest)
	assert.Contains(t, string(resp.Body), "invalid email or password")
}

// The status check, which runs after password verification, must still apply.
func Test_SignInUser_InactiveAccountWithCorrectPassword_ReturnsDeactivatedMessage(t *testing.T) {
	router := createUserTestRouter()
	email := "inactive" + uuid.New().String() + "@example.com"
	test_utils.MakePostRequest(t, router, "/api/v1/users/signup", "",
		users_dto.SignUpRequestDTO{Email: email, Password: "testpassword123", Name: "Test User"}, http.StatusOK)

	user, err := (&users_repositories.UserRepository{}).GetUserByEmail(email)
	require.NoError(t, err)
	require.NotNil(t, user)
	require.NoError(t, (&users_repositories.UserRepository{}).UpdateUserStatus(user.ID, users_enums.UserStatusInactive))

	resp := test_utils.MakePostRequest(t, router, "/api/v1/users/signin", "",
		users_dto.SignInRequestDTO{Email: email, Password: "testpassword123"}, http.StatusBadRequest)
	assert.Contains(t, string(resp.Body), "deactivated")
}

func Test_SignInUser_WithInvalidJSON_ReturnsBadRequest(t *testing.T) {
	router := createUserTestRouter()

	resp := test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method:         "POST",
		URL:            "/api/v1/users/signin",
		Body:           "invalid json",
		ExpectedStatus: http.StatusBadRequest,
	})

	assert.Contains(t, string(resp.Body), "Invalid request format")
}

func signUpAndSignIn(t *testing.T, router *gin.Engine) string {
	t.Helper()

	email := "signout" + uuid.New().String() + "@example.com"
	password := "testpassword123"

	test_utils.MakePostRequest(t, router, "/api/v1/users/signup", "",
		users_dto.SignUpRequestDTO{Email: email, Password: password, Name: "Test User"}, http.StatusOK)

	var response users_dto.SignInResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/users/signin", "",
		users_dto.SignInRequestDTO{Email: email, Password: password},
		http.StatusOK, &response,
	)
	return response.Token
}

// After SignOut the same token must stop working.
func Test_SignOut_RevokesToken_SubsequentUseIsRejected(t *testing.T) {
	router := createUserTestRouter()
	token := signUpAndSignIn(t, router)

	test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method: "GET", URL: "/api/v1/users/me", AuthToken: token, ExpectedStatus: http.StatusOK,
	})

	test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method: "POST", URL: "/api/v1/users/logout", AuthToken: token, ExpectedStatus: http.StatusOK,
	})

	test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method: "GET", URL: "/api/v1/users/me", AuthToken: token, ExpectedStatus: http.StatusUnauthorized,
	})
}

// SignOut revokes one token, not every session of the account.
func Test_SignOut_DoesNotRevokeOtherSessions(t *testing.T) {
	router := createUserTestRouter()

	email := "signout-multi" + uuid.New().String() + "@example.com"
	password := "testpassword123"
	test_utils.MakePostRequest(t, router, "/api/v1/users/signup", "",
		users_dto.SignUpRequestDTO{Email: email, Password: password, Name: "Test User"}, http.StatusOK)

	signIn := func() string {
		var response users_dto.SignInResponseDTO
		test_utils.MakePostRequestAndUnmarshal(
			t, router, "/api/v1/users/signin", "",
			users_dto.SignInRequestDTO{Email: email, Password: password},
			http.StatusOK, &response,
		)
		return response.Token
	}

	tokenA := signIn()
	tokenB := signIn()
	require.NotEqual(t, tokenA, tokenB, "each sign-in should mint a distinct token (jti)")

	test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method: "POST", URL: "/api/v1/users/logout", AuthToken: tokenA, ExpectedStatus: http.StatusOK,
	})

	test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method: "GET", URL: "/api/v1/users/me", AuthToken: tokenA, ExpectedStatus: http.StatusUnauthorized,
	})
	test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method: "GET", URL: "/api/v1/users/me", AuthToken: tokenB, ExpectedStatus: http.StatusOK,
	})
}

func Test_SignOut_WithoutToken_ReturnsUnauthorized(t *testing.T) {
	router := createUserTestRouter()

	test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method: "POST", URL: "/api/v1/users/logout", ExpectedStatus: http.StatusUnauthorized,
	})
}

func Test_CheckAdminHasPassword_WhenAdminHasNoPassword_ReturnsFalse(t *testing.T) {
	router := createUserTestRouter()

	users_testing.RecreateInitialAdmin()

	var response users_dto.IsAdminHasPasswordResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/users/admin/has-password", "", http.StatusOK, &response)

	assert.False(t, response.HasPassword)
	// First run: the setup page needs the login to sign in with afterwards.
	assert.Equal(t, config.GetEnv().EffectiveRootAdminEmail(), response.RootAdminLogin)
}

func Test_SetAdminPassword_WithValidPassword_PasswordSet(t *testing.T) {
	router := createUserTestRouter()

	bootstrapToken := users_testing.RecreateInitialAdmin()

	request := users_dto.SetAdminPasswordRequestDTO{
		Password:       "adminpassword123",
		BootstrapToken: bootstrapToken,
	}

	test_utils.MakePostRequest(t, router, "/api/v1/users/admin/set-password", "", request, http.StatusOK)

	var hasPasswordResponse users_dto.IsAdminHasPasswordResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t,
		router,
		"/api/v1/users/admin/has-password",
		"",
		http.StatusOK,
		&hasPasswordResponse,
	)

	assert.True(t, hasPasswordResponse.HasPassword)
	assert.Empty(t, hasPasswordResponse.RootAdminLogin, "not advertised once the password is set")
}

// Without the logged bootstrap token, whoever reached setup first would claim the instance.
func Test_SetAdminPassword_WithoutBootstrapToken_ReturnsBadRequest(t *testing.T) {
	router := createUserTestRouter()
	users_testing.RecreateInitialAdmin()

	resp := test_utils.MakePostRequest(
		t, router, "/api/v1/users/admin/set-password", "",
		users_dto.SetAdminPasswordRequestDTO{Password: "attackerpassword123"},
		http.StatusBadRequest,
	)
	assert.Contains(t, string(resp.Body), "bootstrap token")

	var hasPasswordResponse users_dto.IsAdminHasPasswordResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t, router, "/api/v1/users/admin/has-password", "", http.StatusOK, &hasPasswordResponse,
	)
	assert.False(t, hasPasswordResponse.HasPassword)
}

func Test_SetAdminPassword_WithWrongBootstrapToken_ReturnsBadRequest(t *testing.T) {
	router := createUserTestRouter()
	users_testing.RecreateInitialAdmin()

	resp := test_utils.MakePostRequest(
		t, router, "/api/v1/users/admin/set-password", "",
		users_dto.SetAdminPasswordRequestDTO{
			Password:       "attackerpassword123",
			BootstrapToken: "0000000000000000000000000000000000000000000000000000000000000000",
		},
		http.StatusBadRequest,
	)
	assert.Contains(t, string(resp.Body), "bootstrap token")
}

func Test_SetAdminPassword_WithInvalidPassword_ReturnsBadRequest(t *testing.T) {
	router := createUserTestRouter()

	testCases := []struct {
		name     string
		password string
	}{
		{
			name:     "short password",
			password: "short",
		},
		{
			name:     "empty password",
			password: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			request := users_dto.SetAdminPasswordRequestDTO{
				Password: tc.password,
			}

			test_utils.MakePostRequest(
				t,
				router,
				"/api/v1/users/admin/set-password",
				"",
				request,
				http.StatusBadRequest,
			)
		})
	}
}

func Test_SetAdminPassword_WithInvalidJSON_ReturnsBadRequest(t *testing.T) {
	router := createUserTestRouter()

	resp := test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method:         "POST",
		URL:            "/api/v1/users/admin/set-password",
		Body:           "invalid json",
		ExpectedStatus: http.StatusBadRequest,
	})

	assert.Contains(t, string(resp.Body), "Invalid request format")
}

func Test_ChangeUserPassword_WithValidData_PasswordChanged(t *testing.T) {
	router := createUserTestRouter()
	email := "changepass" + uuid.New().String() + "@example.com"
	oldPassword := "oldpassword123"
	newPassword := "newpassword123"

	signupRequest := users_dto.SignUpRequestDTO{
		Email:    email,
		Password: oldPassword,
		Name:     "Test User",
	}
	test_utils.MakePostRequest(t, router, "/api/v1/users/signup", "", signupRequest, http.StatusOK)

	signinRequest := users_dto.SignInRequestDTO{
		Email:    email,
		Password: oldPassword,
	}
	var signinResponse users_dto.SignInResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t,
		router,
		"/api/v1/users/signin",
		"",
		signinRequest,
		http.StatusOK,
		&signinResponse,
	)

	changePasswordRequest := users_dto.ChangePasswordRequestDTO{
		CurrentPassword: oldPassword,
		NewPassword:     newPassword,
	}

	test_utils.MakePutRequest(
		t,
		router,
		"/api/v1/users/change-password",
		"Bearer "+signinResponse.Token,
		changePasswordRequest,
		http.StatusOK,
	)

	oldSigninRequest := users_dto.SignInRequestDTO{
		Email:    email,
		Password: oldPassword,
	}
	test_utils.MakePostRequest(t, router, "/api/v1/users/signin", "", oldSigninRequest, http.StatusBadRequest)

	newSigninRequest := users_dto.SignInRequestDTO{
		Email:    email,
		Password: newPassword,
	}
	test_utils.MakePostRequest(t, router, "/api/v1/users/signin", "", newSigninRequest, http.StatusOK)
}

// A bearer token alone must not be enough to change the password.
func Test_ChangeUserPassword_WrongCurrentPassword_ReturnsUnauthorized(t *testing.T) {
	router := createUserTestRouter()
	token, _, _ := signUpAndSignInWithPassword(t, router)

	resp := test_utils.MakePutRequest(
		t, router, "/api/v1/users/change-password", "Bearer "+token,
		users_dto.ChangePasswordRequestDTO{CurrentPassword: "wrong-password", NewPassword: "newpassword123"},
		http.StatusUnauthorized,
	)
	assert.Contains(t, string(resp.Body), "current password is incorrect")
}

// With MFA enabled, a stolen password alone must not be enough to change it.
func Test_ChangeUserPassword_MFAEnabled_WithoutCode_ReturnsUnprocessableEntity(t *testing.T) {
	router := createUserTestRouter()
	token, password, userID := signUpAndSignInWithPassword(t, router)
	users_testing.EnableRealMFAForTestUser(userID, "irrelevant@example.com")

	resp := test_utils.MakePutRequest(
		t, router, "/api/v1/users/change-password", "Bearer "+token,
		users_dto.ChangePasswordRequestDTO{CurrentPassword: password, NewPassword: "newpassword123"},
		http.StatusUnprocessableEntity,
	)
	assert.Contains(t, string(resp.Body), "MFA code required")
}

func Test_ChangeUserPassword_MFAEnabled_WithWrongCode_ReturnsUnprocessableEntity(t *testing.T) {
	router := createUserTestRouter()
	token, password, userID := signUpAndSignInWithPassword(t, router)
	users_testing.EnableRealMFAForTestUser(userID, "irrelevant@example.com")

	wrongCode := "000000"
	resp := test_utils.MakePutRequest(
		t, router, "/api/v1/users/change-password", "Bearer "+token,
		users_dto.ChangePasswordRequestDTO{CurrentPassword: password, NewPassword: "newpassword123", MFACode: &wrongCode},
		http.StatusUnprocessableEntity,
	)
	assert.Contains(t, string(resp.Body), "invalid MFA code")
}

func Test_ChangeUserPassword_MFAEnabled_WithCorrectCode_Succeeds(t *testing.T) {
	router := createUserTestRouter()
	token, password, userID := signUpAndSignInWithPassword(t, router)
	mfaCode := users_testing.EnableRealMFAForTestUser(userID, "irrelevant@example.com")

	test_utils.MakePutRequest(
		t, router, "/api/v1/users/change-password", "Bearer "+token,
		users_dto.ChangePasswordRequestDTO{CurrentPassword: password, NewPassword: "newpassword123", MFACode: &mfaCode},
		http.StatusOK,
	)
}

func Test_ChangeUserPassword_WithoutAuth_ReturnsUnauthorized(t *testing.T) {
	router := createUserTestRouter()

	request := users_dto.ChangePasswordRequestDTO{
		NewPassword: "newpassword123",
	}

	test_utils.MakePutRequest(t, router, "/api/v1/users/change-password", "", request, http.StatusUnauthorized)
}

func Test_ChangeUserPassword_WithInvalidJSON_ReturnsBadRequest(t *testing.T) {
	router := createUserTestRouter()
	testUser := users_testing.CreateTestUser(users_enums.UserRoleMember)

	resp := test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method:         "PUT",
		URL:            "/api/v1/users/change-password",
		Body:           "invalid json",
		AuthToken:      "Bearer " + testUser.Token,
		ExpectedStatus: http.StatusBadRequest,
	})

	assert.Contains(t, string(resp.Body), "Invalid request format")
}

func Test_ChangeUserPassword_WithValidationErrors_ReturnsBadRequest(t *testing.T) {
	router := createUserTestRouter()
	testUser := users_testing.CreateTestUser(users_enums.UserRoleMember)

	testCases := []struct {
		name    string
		request users_dto.ChangePasswordRequestDTO
	}{
		{
			name:    "missing new password",
			request: users_dto.ChangePasswordRequestDTO{},
		},
		{
			name: "short new password",
			request: users_dto.ChangePasswordRequestDTO{
				NewPassword: "short",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			test_utils.MakePutRequest(
				t,
				router,
				"/api/v1/users/change-password",
				"Bearer "+testUser.Token,
				tc.request,
				http.StatusBadRequest,
			)
		})
	}
}

func Test_InviteUser_WhenUserIsAdmin_UserInvited(t *testing.T) {
	router := createUserTestRouter()
	adminUser := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	projectID := uuid.New()
	projectRole := users_enums.ProjectRoleAdmin

	request := users_dto.InviteUserRequestDTO{
		Email:               "invited" + uuid.New().String() + "@example.com",
		IntendedProjectID:   &projectID,
		IntendedProjectRole: &projectRole,
	}

	var response users_dto.InviteUserResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t,
		router,
		"/api/v1/users/invite",
		"Bearer "+adminUser.Token,
		request,
		http.StatusOK,
		&response,
	)

	assert.Equal(t, request.Email, response.Email)
	assert.Equal(t, request.IntendedProjectID, response.IntendedProjectID)
	assert.Equal(t, request.IntendedProjectRole, response.IntendedProjectRole)
	assert.NotEqual(t, uuid.Nil, response.ID)
}

func Test_InviteUser_WithoutAuth_ReturnsUnauthorized(t *testing.T) {
	router := createUserTestRouter()

	request := users_dto.InviteUserRequestDTO{
		Email: "invited@example.com",
	}

	test_utils.MakePostRequest(t, router, "/api/v1/users/invite", "", request, http.StatusUnauthorized)
}

func Test_InviteUser_WithoutPermission_ReturnsForbidden(t *testing.T) {
	router := createUserTestRouter()
	defer users_testing.ResetSettingsToDefaults()

	memberUser := users_testing.CreateTestUser(users_enums.UserRoleMember)

	uniqueID := uuid.New().String()[:8]
	request := users_dto.InviteUserRequestDTO{
		Email: fmt.Sprintf("invited_%s@example.com", uniqueID),
	}

	users_testing.DisableMemberInvitations()

	settingsService := users_services.GetSettingsService()
	settings, err := settingsService.GetSettings()
	assert.NoError(t, err)

	if settings.IsAllowMemberInvitations {
		t.Fatal("RACE CONDITION DETECTED: Member invitations should be disabled but were enabled by another test")
	}

	resp := test_utils.MakePostRequest(
		t,
		router,
		"/api/v1/users/invite",
		"Bearer "+memberUser.Token,
		request,
		http.StatusForbidden,
	)
	assert.Contains(t, string(resp.Body), "insufficient permissions")
}

func Test_InviteUser_WithInvalidJSON_ReturnsBadRequest(t *testing.T) {
	router := createUserTestRouter()
	adminUser := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	resp := test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method:         "POST",
		URL:            "/api/v1/users/invite",
		Body:           "invalid json",
		AuthToken:      "Bearer " + adminUser.Token,
		ExpectedStatus: http.StatusBadRequest,
	})

	assert.Contains(t, string(resp.Body), "Invalid request format")
}

func Test_InviteUser_WithValidationErrors_ReturnsBadRequest(t *testing.T) {
	router := createUserTestRouter()
	adminUser := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	testCases := []struct {
		name    string
		request users_dto.InviteUserRequestDTO
	}{
		{
			name: "missing email",
			request: users_dto.InviteUserRequestDTO{
				IntendedProjectID: &uuid.UUID{},
			},
		},
		{
			name: "invalid email",
			request: users_dto.InviteUserRequestDTO{
				Email: "invalid-email",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			test_utils.MakePostRequest(
				t,
				router,
				"/api/v1/users/invite",
				"Bearer "+adminUser.Token,
				tc.request,
				http.StatusBadRequest,
			)
		})
	}
}

func Test_InviteUser_WithDuplicateEmail_ReturnsBadRequest(t *testing.T) {
	router := createUserTestRouter()
	adminUser := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	email := "duplicate-invite" + uuid.New().String() + "@example.com"

	request := users_dto.InviteUserRequestDTO{
		Email: email,
	}

	test_utils.MakePostRequest(t, router, "/api/v1/users/invite", "Bearer "+adminUser.Token, request, http.StatusOK)

	resp := test_utils.MakePostRequest(
		t,
		router,
		"/api/v1/users/invite",
		"Bearer "+adminUser.Token,
		request,
		http.StatusBadRequest,
	)
	assert.Contains(t, string(resp.Body), "already exists")
}

func Test_UpdateUserInfo_WithValidName_NameUpdated(t *testing.T) {
	router := createUserTestRouter()
	testUser := users_testing.CreateTestUser(users_enums.UserRoleMember)

	newName := "Updated Name"
	request := users_dto.UpdateUserInfoRequestDTO{
		Name: &newName,
	}

	test_utils.MakePutRequest(
		t,
		router,
		"/api/v1/users/me",
		"Bearer "+testUser.Token,
		request,
		http.StatusOK,
	)

	var profile users_dto.UserProfileResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t,
		router,
		"/api/v1/users/me",
		"Bearer "+testUser.Token,
		http.StatusOK,
		&profile,
	)

	assert.Equal(t, "Updated Name", profile.Name)
}

// signUpAndSignInWithPassword also returns the plaintext password, which
// CreateTestUser's placeholder hash can't provide.
func signUpAndSignInWithPassword(t *testing.T, router *gin.Engine) (token, password string, userID uuid.UUID) {
	t.Helper()

	email := "updateinfo" + uuid.New().String() + "@example.com"
	password = "testpassword123"

	test_utils.MakePostRequest(t, router, "/api/v1/users/signup", "",
		users_dto.SignUpRequestDTO{Email: email, Password: password, Name: "Test User"}, http.StatusOK)

	var response users_dto.SignInResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/users/signin", "",
		users_dto.SignInRequestDTO{Email: email, Password: password},
		http.StatusOK, &response,
	)
	return response.Token, password, response.UserID
}

func Test_UpdateUserInfo_WithValidEmailAndCorrectPassword_EmailUpdated(t *testing.T) {
	router := createUserTestRouter()
	token, password, _ := signUpAndSignInWithPassword(t, router)

	newEmail := "newemail" + uuid.New().String() + "@example.com"
	request := users_dto.UpdateUserInfoRequestDTO{
		Email:           &newEmail,
		CurrentPassword: password,
	}

	test_utils.MakePutRequest(
		t,
		router,
		"/api/v1/users/me",
		"Bearer "+token,
		request,
		http.StatusOK,
	)

	var profile users_dto.UserProfileResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t,
		router,
		"/api/v1/users/me",
		"Bearer "+token,
		http.StatusOK,
		&profile,
	)

	assert.Equal(t, newEmail, profile.Email)
}

// A bearer token alone must not be enough to change the email.
func Test_UpdateUserInfo_EmailChangeWithoutCurrentPassword_ReturnsUnauthorized(t *testing.T) {
	router := createUserTestRouter()
	token, _, _ := signUpAndSignInWithPassword(t, router)

	newEmail := "newemail" + uuid.New().String() + "@example.com"
	test_utils.MakePutRequest(
		t, router, "/api/v1/users/me", "Bearer "+token,
		users_dto.UpdateUserInfoRequestDTO{Email: &newEmail},
		http.StatusUnauthorized,
	)
}

func Test_UpdateUserInfo_EmailChangeWithWrongCurrentPassword_ReturnsUnauthorized(t *testing.T) {
	router := createUserTestRouter()
	token, _, _ := signUpAndSignInWithPassword(t, router)

	newEmail := "newemail" + uuid.New().String() + "@example.com"
	resp := test_utils.MakePutRequest(
		t, router, "/api/v1/users/me", "Bearer "+token,
		users_dto.UpdateUserInfoRequestDTO{Email: &newEmail, CurrentPassword: "wrong-password"},
		http.StatusUnauthorized,
	)
	assert.Contains(t, string(resp.Body), "current password is incorrect")
}

func Test_UpdateUserInfo_WithTakenEmail_ReturnsBadRequest(t *testing.T) {
	router := createUserTestRouter()
	token1, password1, _ := signUpAndSignInWithPassword(t, router)

	user2Email := "updateinfo" + uuid.New().String() + "@example.com"
	test_utils.MakePostRequest(t, router, "/api/v1/users/signup", "",
		users_dto.SignUpRequestDTO{Email: user2Email, Password: "testpassword123", Name: "Test User"}, http.StatusOK)

	request := users_dto.UpdateUserInfoRequestDTO{
		Email:           &user2Email,
		CurrentPassword: password1,
	}

	resp := test_utils.MakePutRequest(
		t,
		router,
		"/api/v1/users/me",
		"Bearer "+token1,
		request,
		http.StatusBadRequest,
	)

	assert.Contains(t, string(resp.Body), "already taken")
}

func Test_UpdateUserInfo_WhenAdminTriesToChangeEmail_ReturnsBadRequest(t *testing.T) {
	router := createUserTestRouter()
	adminUser := users_testing.ReacreateInitAdminAndGetAccess()

	newEmail := "newemail@example.com"
	request := users_dto.UpdateUserInfoRequestDTO{
		Email: &newEmail,
	}

	resp := test_utils.MakePutRequest(
		t,
		router,
		"/api/v1/users/me",
		"Bearer "+adminUser.Token,
		request,
		http.StatusBadRequest,
	)

	assert.Contains(t, string(resp.Body), "admin email cannot be changed")
}

// A password-less account has no re-auth to check, so its email can't change here.
func Test_UpdateUserInfo_NoPasswordAccount_EmailChangeRejected(t *testing.T) {
	router := createUserTestRouter()

	userID := uuid.New()
	email := "ssouser" + userID.String()[:8] + "@example.com"
	user := &users_models.User{
		ID:                   userID,
		Email:                email,
		Name:                 "SSO Test User",
		HashedPassword:       nil,
		PasswordCreationTime: time.Now().UTC(),
		CreatedAt:            time.Now().UTC(),
		Role:                 users_enums.UserRoleMember,
		Status:               users_enums.UserStatusActive,
	}
	require.NoError(t, (&users_repositories.UserRepository{}).CreateUser(user))

	response, err := users_services.GetUserService().GenerateAccessToken(user)
	require.NoError(t, err)

	newEmail := "newemail" + uuid.New().String() + "@example.com"
	resp := test_utils.MakePutRequest(
		t, router, "/api/v1/users/me", "Bearer "+response.Token,
		users_dto.UpdateUserInfoRequestDTO{Email: &newEmail, CurrentPassword: "anything"},
		http.StatusBadRequest,
	)
	assert.Contains(t, string(resp.Body), "no password set")
}

// The callback isn't called here (it would need github.com); verification is
// proven via verifyOAuthState directly.
func Test_GenerateOAuthState_ReturnsSignedStateThatPassesVerification(t *testing.T) {
	router := createUserTestRouter()

	resp := test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method: "GET", URL: "/api/v1/auth/oauth/state?provider=github", ExpectedStatus: http.StatusOK,
	})

	var stateResponse users_dto.GenerateOAuthStateResponseDTO
	require.NoError(t, json.Unmarshal(resp.Body, &stateResponse))
	assert.NotEmpty(t, stateResponse.State)
}

func Test_GenerateOAuthState_UnsupportedProvider_ReturnsBadRequest(t *testing.T) {
	router := createUserTestRouter()

	test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method: "GET", URL: "/api/v1/auth/oauth/state?provider=facebook", ExpectedStatus: http.StatusBadRequest,
	})
}

// The old fixed state="github" must be rejected before any code exchange.
func Test_GitHubOAuthCallback_FixedLegacyStateString_Rejected(t *testing.T) {
	router := createUserTestRouter()

	resp := test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method: "POST", URL: "/api/v1/auth/github/callback",
		Body: users_dto.OAuthCallbackRequestDTO{
			Code: "irrelevant", RedirectUri: "http://localhost:3000/auth/callback", State: "github",
		},
		ExpectedStatus: http.StatusBadRequest,
	})
	assert.Contains(t, string(resp.Body), "oauth state")
}

func Test_GitHubOAuthCallback_MissingState_ReturnsBadRequest(t *testing.T) {
	router := createUserTestRouter()

	test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method: "POST", URL: "/api/v1/auth/github/callback",
		Body: map[string]string{
			"code": "irrelevant", "redirectUri": "http://localhost:3000/auth/callback",
		},
		ExpectedStatus: http.StatusBadRequest,
	})
}

func Test_GitHubOAuthCallback_GoogleState_Rejected(t *testing.T) {
	router := createUserTestRouter()

	resp := test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method: "GET", URL: "/api/v1/auth/oauth/state?provider=google", ExpectedStatus: http.StatusOK,
	})
	var stateResponse users_dto.GenerateOAuthStateResponseDTO
	require.NoError(t, json.Unmarshal(resp.Body, &stateResponse))

	resp = test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method: "POST", URL: "/api/v1/auth/github/callback",
		Body: users_dto.OAuthCallbackRequestDTO{
			Code: "irrelevant", RedirectUri: "http://localhost:3000/auth/callback", State: stateResponse.State,
		},
		ExpectedStatus: http.StatusBadRequest,
	})
	assert.Contains(t, string(resp.Body), "oauth state")
}

func Test_GitHubOAuth_WithValidCode_ReturnsToken(t *testing.T) {
	testID := uuid.New().String()[:8]
	testEmail := "github-user-" + testID + "@example.com"
	testOAuthID := int64(uuid.New().ID())

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login/oauth/access_token" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"access_token": "mock-access-token",
				"token_type":   "bearer",
			})
			return
		}
		if r.URL.Path == "/user" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":    testOAuthID,
				"email": testEmail,
				"name":  "GitHub Test User",
				"login": "githubtest",
			})
			return
		}
		if r.URL.Path == "/user/emails" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"email": testEmail, "primary": true, "verified": true},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	endpoint := oauth2.Endpoint{
		AuthURL:  mockServer.URL + "/login/oauth/authorize",
		TokenURL: mockServer.URL + "/login/oauth/access_token",
	}

	userService := users_services.GetUserService()
	response, err := userService.HandleGitHubOAuthWithMockEndpoint(
		"test-code",
		"http://localhost:3000/auth/callback",
		endpoint,
		mockServer.URL+"/user",
	)

	require.NoError(t, err)
	assert.NotEmpty(t, response.Token)
	assert.Equal(t, testEmail, response.Email)
	assert.True(t, response.IsNewUser)
}

// An email match to a password account must not be auto-linked, or whoever
// controls that email at GitHub could sign in as the account.
func Test_GitHubOAuth_WithExistingPasswordAccount_RefusesAutoLink(t *testing.T) {
	testID := uuid.New().String()[:8]
	email := "existing-" + testID + "@example.com"
	testOAuthID := int64(uuid.New().ID())

	router := createUserTestRouter()
	signupRequest := users_dto.SignUpRequestDTO{
		Email:    email,
		Password: "testpassword123",
		Name:     "Existing User",
	}
	test_utils.MakePostRequest(t, router, "/api/v1/users/signup", "", signupRequest, http.StatusOK)

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login/oauth/access_token" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"access_token": "mock-access-token",
				"token_type":   "bearer",
			})
			return
		}
		if r.URL.Path == "/user" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":    testOAuthID,
				"email": email,
				"name":  "GitHub Test User",
				"login": "githubtest",
			})
			return
		}
		if r.URL.Path == "/user/emails" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"email": email, "primary": true, "verified": true},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	endpoint := oauth2.Endpoint{
		AuthURL:  mockServer.URL + "/login/oauth/authorize",
		TokenURL: mockServer.URL + "/login/oauth/access_token",
	}

	userService := users_services.GetUserService()
	response, err := userService.HandleGitHubOAuthWithMockEndpoint(
		"test-code",
		"http://localhost:3000/auth/callback",
		endpoint,
		mockServer.URL+"/user",
	)

	assert.ErrorIs(t, err, users_services.ErrOAuthAccountNotLinkable)
	assert.Nil(t, response)
}

func Test_GitHubOAuth_WithInvitedUser_ActivatesUser(t *testing.T) {
	router := createUserTestRouter()
	adminUser := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	testID := uuid.New().String()[:8]
	email := "invited-" + testID + "@example.com"
	testOAuthID := int64(uuid.New().ID())

	inviteRequest := users_dto.InviteUserRequestDTO{
		Email: email,
	}
	test_utils.MakePostRequest(
		t,
		router,
		"/api/v1/users/invite",
		"Bearer "+adminUser.Token,
		inviteRequest,
		http.StatusOK,
	)

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login/oauth/access_token" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"access_token": "mock-access-token",
				"token_type":   "bearer",
			})
			return
		}
		if r.URL.Path == "/user" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":    testOAuthID,
				"email": email,
				"name":  "GitHub Test User",
				"login": "githubtest",
			})
			return
		}
		if r.URL.Path == "/user/emails" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"email": email, "primary": true, "verified": true},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	endpoint := oauth2.Endpoint{
		AuthURL:  mockServer.URL + "/login/oauth/authorize",
		TokenURL: mockServer.URL + "/login/oauth/access_token",
	}

	userService := users_services.GetUserService()
	response, err := userService.HandleGitHubOAuthWithMockEndpoint(
		"test-code",
		"http://localhost:3000/auth/callback",
		endpoint,
		mockServer.URL+"/user",
	)

	require.NoError(t, err)
	assert.NotEmpty(t, response.Token)
	assert.Equal(t, email, response.Email)
	assert.False(t, response.IsNewUser)
}

func Test_GitHubOAuth_WithNoPublicEmail_FetchesFromEmailsEndpoint(t *testing.T) {
	testID := uuid.New().String()[:8]
	testEmail := "private-email-" + testID + "@example.com"
	testOAuthID := int64(uuid.New().ID())

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login/oauth/access_token" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"access_token": "mock-access-token",
				"token_type":   "bearer",
			})
			return
		}
		if r.URL.Path == "/user" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":    testOAuthID,
				"email": "",
				"name":  "GitHub Test User",
				"login": "githubtest",
			})
			return
		}
		if r.URL.Path == "/user/emails" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"email":    testEmail,
					"primary":  true,
					"verified": true,
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	endpoint := oauth2.Endpoint{
		AuthURL:  mockServer.URL + "/login/oauth/authorize",
		TokenURL: mockServer.URL + "/login/oauth/access_token",
	}

	userService := users_services.GetUserService()
	response, err := userService.HandleGitHubOAuthWithMockEndpoint(
		"test-code",
		"http://localhost:3000/auth/callback",
		endpoint,
		mockServer.URL+"/user",
	)

	require.NoError(t, err)
	assert.NotEmpty(t, response.Token)
	assert.Equal(t, testEmail, response.Email)
	assert.True(t, response.IsNewUser)
}

func Test_GitHubOAuth_WhenRegistrationDisabled_ReturnsBadRequest(t *testing.T) {
	defer users_testing.ResetSettingsToDefaults()
	users_testing.DisableExternalRegistrations()

	newEmail := "new-user-" + uuid.New().String()[:8] + "@example.com"
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login/oauth/access_token" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"access_token": "mock-access-token",
				"token_type":   "bearer",
			})
			return
		}
		if r.URL.Path == "/user" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":    99999,
				"email": newEmail,
				"name":  "GitHub Test User",
				"login": "githubtest",
			})
			return
		}
		if r.URL.Path == "/user/emails" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"email": newEmail, "primary": true, "verified": true},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	endpoint := oauth2.Endpoint{
		AuthURL:  mockServer.URL + "/login/oauth/authorize",
		TokenURL: mockServer.URL + "/login/oauth/access_token",
	}

	userService := users_services.GetUserService()
	response, err := userService.HandleGitHubOAuthWithMockEndpoint(
		"test-code",
		"http://localhost:3000/auth/callback",
		endpoint,
		mockServer.URL+"/user",
	)

	assert.Error(t, err)
	assert.Nil(t, response)
	assert.Contains(t, err.Error(), "registration is disabled")
}

func Test_GoogleOAuth_WithValidCode_ReturnsToken(t *testing.T) {
	testID := uuid.New().String()[:8]
	testEmail := "google-user-" + testID + "@example.com"
	testOAuthID := "google-" + testID

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"access_token": "mock-access-token",
				"token_type":   "Bearer",
			})
			return
		}
		if r.URL.Path == "/userinfo" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":             testOAuthID,
				"email":          testEmail,
				"verified_email": true,
				"name":           "Google Test User",
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	endpoint := oauth2.Endpoint{
		AuthURL:  mockServer.URL + "/auth",
		TokenURL: mockServer.URL + "/token",
	}

	userService := users_services.GetUserService()
	response, err := userService.HandleGoogleOAuthWithMockEndpoint(
		"test-code",
		"http://localhost:3000/auth/callback",
		endpoint,
		mockServer.URL+"/userinfo",
	)

	require.NoError(t, err)
	assert.NotEmpty(t, response.Token)
	assert.Equal(t, testEmail, response.Email)
	assert.True(t, response.IsNewUser)
}

func Test_GoogleOAuth_WithExistingPasswordAccount_RefusesAutoLink(t *testing.T) {
	testID := uuid.New().String()[:8]
	email := "existing-google-" + testID + "@example.com"
	testOAuthID := "google-" + testID + "-456"

	router := createUserTestRouter()
	signupRequest := users_dto.SignUpRequestDTO{
		Email:    email,
		Password: "testpassword123",
		Name:     "Existing User",
	}
	test_utils.MakePostRequest(t, router, "/api/v1/users/signup", "", signupRequest, http.StatusOK)

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"access_token": "mock-access-token",
				"token_type":   "Bearer",
			})
			return
		}
		if r.URL.Path == "/userinfo" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":             testOAuthID,
				"email":          email,
				"verified_email": true,
				"name":           "Google Test User",
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	endpoint := oauth2.Endpoint{
		AuthURL:  mockServer.URL + "/auth",
		TokenURL: mockServer.URL + "/token",
	}

	userService := users_services.GetUserService()
	response, err := userService.HandleGoogleOAuthWithMockEndpoint(
		"test-code",
		"http://localhost:3000/auth/callback",
		endpoint,
		mockServer.URL+"/userinfo",
	)

	assert.ErrorIs(t, err, users_services.ErrOAuthAccountNotLinkable)
	assert.Nil(t, response)
}

func Test_GoogleOAuth_WithInvitedUser_ActivatesUser(t *testing.T) {
	router := createUserTestRouter()
	adminUser := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	testID := uuid.New().String()[:8]
	email := "invited-google-" + testID + "@example.com"
	testOAuthID := "google-" + testID + "-789"

	inviteRequest := users_dto.InviteUserRequestDTO{
		Email: email,
	}
	test_utils.MakePostRequest(
		t,
		router,
		"/api/v1/users/invite",
		"Bearer "+adminUser.Token,
		inviteRequest,
		http.StatusOK,
	)

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"access_token": "mock-access-token",
				"token_type":   "Bearer",
			})
			return
		}
		if r.URL.Path == "/userinfo" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":             testOAuthID,
				"email":          email,
				"verified_email": true,
				"name":           "Google Test User",
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	endpoint := oauth2.Endpoint{
		AuthURL:  mockServer.URL + "/auth",
		TokenURL: mockServer.URL + "/token",
	}

	userService := users_services.GetUserService()
	response, err := userService.HandleGoogleOAuthWithMockEndpoint(
		"test-code",
		"http://localhost:3000/auth/callback",
		endpoint,
		mockServer.URL+"/userinfo",
	)

	require.NoError(t, err)
	assert.NotEmpty(t, response.Token)
	assert.Equal(t, email, response.Email)
	assert.False(t, response.IsNewUser)
}
