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

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The email of an account an admin invited is vouched for, and stays so once
// the invitee signs up with the invite token: this is what lets a control
// plane tell a Site to match a local account by that email.
func Test_InviteUser_ByAdmin_EmailStaysVerifiedThroughSignUp(t *testing.T) {
	router := createUserTestRouter()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	email := "contoso-invitee-" + uuid.NewString() + "@example.com"

	invite := inviteUserViaAPI(t, router, admin.Token, email)
	assert.True(t, userByEmail(t, email).EmailVerified)

	test_utils.MakePostRequest(t, router, "/api/v1/users/signup", "", users_dto.SignUpRequestDTO{
		Email: email, Password: "testpassword123", Name: "Contoso Invitee", InviteToken: invite.InviteToken,
	}, http.StatusOK)
	assert.True(t, userByEmail(t, email).EmailVerified)
}

// A member who may invite gets the invite token back and could finish the
// sign-up themselves, so their invite vouches for nothing.
func Test_InviteUser_ByMember_EmailNotVerified(t *testing.T) {
	router := createUserTestRouter()
	users_testing.EnableMemberInvitations()
	defer users_testing.ResetSettingsToDefaults()
	member := users_testing.CreateTestUser(users_enums.UserRoleMember)
	email := "fabrikam-invitee-" + uuid.NewString() + "@example.com"

	inviteUserViaAPI(t, router, member.Token, email)

	assert.False(t, userByEmail(t, email).EmailVerified)
}

func Test_SignUp_OpenRegistration_EmailNotVerified(t *testing.T) {
	router := createUserTestRouter()
	users_testing.EnableExternalRegistrations()
	defer users_testing.ResetSettingsToDefaults()
	email := "fabrikam-signup-" + uuid.NewString() + "@example.com"

	test_utils.MakePostRequest(t, router, "/api/v1/users/signup", "",
		users_dto.SignUpRequestDTO{Email: email, Password: "testpassword123", Name: "Fabrikam"}, http.StatusOK)

	assert.False(t, userByEmail(t, email).EmailVerified)
}

// Regression: a self-service email change makes the email unverified, so a
// control plane stops vouching for it; changing only the name doesn't.
func Test_UpdateUserInfo_SelfServiceEmailChange_ClearsEmailVerified(t *testing.T) {
	router := createUserTestRouter()
	token, password, userID := signUpAndSignInWithPassword(t, router)
	repo := &users_repositories.UserRepository{}
	require.NoError(t, repo.SetEmailVerified(userID, true))
	before, err := repo.GetUserByID(userID)
	require.NoError(t, err)

	name := "Renamed"
	test_utils.MakePutRequest(t, router, "/api/v1/users/me", "Bearer "+token,
		users_dto.UpdateUserInfoRequestDTO{Name: &name, Email: &before.Email}, http.StatusOK)
	afterRename, err := repo.GetUserByID(userID)
	require.NoError(t, err)
	assert.True(t, afterRename.EmailVerified, "an unchanged email keeps its flag")

	code := users_testing.EnableRealMFAForTestUser(userID, "irrelevant@example.com")
	newEmail := "contoso-victim-" + uuid.NewString() + "@example.com"
	test_utils.MakePutRequest(t, router, "/api/v1/users/me", "Bearer "+token,
		users_dto.UpdateUserInfoRequestDTO{Email: &newEmail, CurrentPassword: password, MFACode: &code},
		http.StatusOK)

	afterChange, err := repo.GetUserByID(userID)
	require.NoError(t, err)
	assert.Equal(t, newEmail, afterChange.Email)
	assert.False(t, afterChange.EmailVerified)
}

// Regression: a password sign-in gets its session from the MFA step, which
// wrote no audit entry, so no local sign-in was audited at all.
func Test_SignIn_CompletedThroughMFA_WritesSignInAudit(t *testing.T) {
	router := createE2ETestRouter()
	user := createUserWithPassword(t, users_enums.UserRoleMember, "memberpass123")

	audit := &recordingAuditWriter{}
	users_services.GetUserService().SetAuditLogWriter(audit)
	defer users_services.GetUserService().SetAuditLogWriter(&AuditLogWriterStub{})

	var signIn users_dto.SignInResponseDTO
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/users/signin", "",
		users_dto.SignInRequestDTO{Email: user.Email, Password: "memberpass123"}, http.StatusOK, &signIn)
	require.True(t, signIn.MFASetupRequired)
	assert.False(t, audit.contains("User signed in"), "no session yet, so nothing to audit")

	var setup mfa_dto.SetupResponseDTO
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/users/mfa/login-setup", "",
		mfa_dto.LoginSetupRequestDTO{MFAToken: signIn.MFAToken}, http.StatusOK, &setup)
	code, err := totp.GenerateCode(setup.Secret, time.Now())
	require.NoError(t, err)
	var enabled users_dto.SignInResponseDTO
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/users/mfa/login-enable", "",
		mfa_dto.LoginEnableRequestDTO{MFAToken: signIn.MFAToken, Code: code}, http.StatusOK, &enabled)
	require.NotEmpty(t, enabled.Token)
	assert.Equal(t, user.Email, enabled.Email)
	assert.True(t, audit.contains("User signed in: "+user.Email+" (MFA set up)"))

	var second users_dto.SignInResponseDTO
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/users/signin", "",
		users_dto.SignInRequestDTO{Email: user.Email, Password: "memberpass123"}, http.StatusOK, &second)
	require.True(t, second.MFARequired)
	assert.False(t, audit.contains("(MFA verified)"))

	// The next time step: the setup code's step is already consumed.
	loginCode, err := totp.GenerateCodeCustom(setup.Secret, time.Now().Add(30*time.Second),
		totp.ValidateOpts{Period: 30, Skew: 1, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	require.NoError(t, err)
	var verified users_dto.SignInResponseDTO
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/users/mfa/login-verify", "",
		mfa_dto.LoginVerifyRequestDTO{MFAToken: second.MFAToken, Code: loginCode}, http.StatusOK, &verified)
	require.NotEmpty(t, verified.Token)
	assert.True(t, audit.contains("User signed in: "+user.Email+" (MFA verified)"))
}

// Regression: linking a provider identity to an invited account was audited
// only when the same call issued a token, which a sign-in sent on to MFA
// setup never does.
func Test_SSOCallback_LinksInvitedAccount_WritesLinkAuditBeforeMFA(t *testing.T) {
	router := createSSOTestRouter()
	fake := newFakeOIDCServer(t, "client-1")
	connector := createGenericConnector(t, router, fake)

	email := "contoso-sso-invited-" + uuid.NewString()[:8] + "@example.com"
	tokenHash := "hash-" + uuid.NewString()
	expires := time.Now().UTC().Add(time.Hour)
	require.NoError(t, (&users_repositories.UserRepository{}).CreateUser(&users_models.User{
		ID: uuid.New(), Email: email, Name: "User", PasswordCreationTime: time.Now().UTC(),
		Role: users_enums.UserRoleMember, Status: users_enums.UserStatusInvited, CreatedAt: time.Now().UTC(),
		InviteTokenHash: &tokenHash, InviteTokenExpiresAt: &expires,
	}))

	audit := &recordingAuditWriter{}
	users_services.GetUserService().SetAuditLogWriter(audit)
	defer users_services.GetUserService().SetAuditLogWriter(&AuditLogWriterStub{})

	response := ssoSignIn(t, router, fake, connector.ID, "invited-"+uuid.NewString()[:8], email, nil)

	require.True(t, response.MFASetupRequired)
	require.Empty(t, response.Token)
	assert.True(t, audit.contains("sso OAuth linked to existing account"))
	assert.False(t, audit.contains("signed in"))
	assert.True(t, userByEmail(t, email).EmailVerified, "the provider's trusted email matched")
}

func inviteUserViaAPI(t *testing.T, router *gin.Engine, token, email string) users_dto.InviteUserResponseDTO {
	t.Helper()
	var response users_dto.InviteUserResponseDTO
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/users/invite", "Bearer "+token,
		users_dto.InviteUserRequestDTO{Email: email}, http.StatusOK, &response)
	return response
}

func userByEmail(t *testing.T, email string) *users_models.User {
	t.Helper()
	user, err := (&users_repositories.UserRepository{}).GetUserByEmail(email)
	require.NoError(t, err)
	require.NotNil(t, user)
	return user
}
