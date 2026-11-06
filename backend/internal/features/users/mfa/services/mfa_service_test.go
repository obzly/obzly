package mfa_services

import (
	"testing"
	"time"

	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"

	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTestUser(t *testing.T, role users_enums.UserRole) *users_models.User {
	t.Helper()

	hashedPassword := "$2a$10$test"
	user := &users_models.User{
		ID:                   uuid.New(),
		Email:                "mfa-test-" + uuid.New().String()[:8] + "@example.com",
		Name:                 "MFA Test User",
		HashedPassword:       &hashedPassword,
		PasswordCreationTime: time.Now().UTC(),
		Role:                 role,
		Status:               users_enums.UserStatusActive,
		CreatedAt:            time.Now().UTC(),
	}
	require.NoError(t, userRepository.CreateUser(user))
	return user
}

func codeFor(t *testing.T, secret string) string {
	t.Helper()
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	return code
}

// nextCodeFor returns the next step's code, for tests that verify twice against
// one secret (the same step is never accepted twice).
func nextCodeFor(t *testing.T, secret string) string {
	t.Helper()
	code, err := totp.GenerateCodeCustom(secret, time.Now().Add(totpPeriodSeconds*time.Second), validateOpts)
	require.NoError(t, err)
	return code
}

func Test_Setup_ThenEnable_WithValidCode_EnablesMFA(t *testing.T) {
	user := createTestUser(t, users_enums.UserRoleMember)

	setup, err := mfaService.Setup(user)
	require.NoError(t, err)
	require.NotEmpty(t, setup.Secret)

	reloaded, err := userRepository.GetUserByID(user.ID)
	require.NoError(t, err)

	err = mfaService.Enable(reloaded, codeFor(t, setup.Secret))
	require.NoError(t, err)

	final, err := userRepository.GetUserByID(user.ID)
	require.NoError(t, err)
	assert.True(t, final.MFAEnabled)
}

func Test_Enable_WithWrongCode_Rejected(t *testing.T) {
	user := createTestUser(t, users_enums.UserRoleMember)

	_, err := mfaService.Setup(user)
	require.NoError(t, err)

	err = mfaService.Enable(user, "000000")
	assert.Error(t, err)
}

func Test_Enable_WithoutSetup_Rejected(t *testing.T) {
	user := createTestUser(t, users_enums.UserRoleMember)

	err := mfaService.Enable(user, "123456")
	assert.ErrorIs(t, err, ErrNotSetUp)
}

func Test_Disable_ForMember_WithValidCode_Succeeds(t *testing.T) {
	user := createTestUser(t, users_enums.UserRoleMember)

	setup, err := mfaService.Setup(user)
	require.NoError(t, err)
	reloadedForEnable, err := userRepository.GetUserByID(user.ID)
	require.NoError(t, err)
	require.NoError(t, mfaService.Enable(reloadedForEnable, codeFor(t, setup.Secret)))

	reloaded, err := userRepository.GetUserByID(user.ID)
	require.NoError(t, err)

	err = mfaService.Disable(reloaded, nextCodeFor(t, setup.Secret))
	require.NoError(t, err)

	final, err := userRepository.GetUserByID(user.ID)
	require.NoError(t, err)
	assert.False(t, final.MFAEnabled)
	assert.Nil(t, final.MFASecretEncrypted)
}

// Even with a valid code, an Admin can't disable their own MFA.
func Test_Disable_ForAdmin_Rejected(t *testing.T) {
	user := createTestUser(t, users_enums.UserRoleAdmin)

	setup, err := mfaService.Setup(user)
	require.NoError(t, err)
	reloadedForEnable, err := userRepository.GetUserByID(user.ID)
	require.NoError(t, err)
	require.NoError(t, mfaService.Enable(reloadedForEnable, codeFor(t, setup.Secret)))

	reloaded, err := userRepository.GetUserByID(user.ID)
	require.NoError(t, err)

	err = mfaService.Disable(reloaded, codeFor(t, setup.Secret))
	assert.ErrorIs(t, err, ErrRequiredForAdmin)

	stillEnabled, err := userRepository.GetUserByID(user.ID)
	require.NoError(t, err)
	assert.True(t, stillEnabled.MFAEnabled)
}

// AdminReset is how a locked-out admin's MFA is cleared, including by another admin.
func Test_AdminReset_ClearsAnotherAdmins_MFA(t *testing.T) {
	target := createTestUser(t, users_enums.UserRoleAdmin)
	actingAdmin := createTestUser(t, users_enums.UserRoleAdmin)

	setup, err := mfaService.Setup(target)
	require.NoError(t, err)
	reloadedForEnable, err := userRepository.GetUserByID(target.ID)
	require.NoError(t, err)
	require.NoError(t, mfaService.Enable(reloadedForEnable, codeFor(t, setup.Secret)))

	reloadedTarget, err := userRepository.GetUserByID(target.ID)
	require.NoError(t, err)

	err = mfaService.AdminReset(reloadedTarget, actingAdmin)
	require.NoError(t, err)

	final, err := userRepository.GetUserByID(target.ID)
	require.NoError(t, err)
	assert.False(t, final.MFAEnabled)
	assert.Nil(t, final.MFASecretEncrypted)
}

func Test_LoginChallengeFlow_SetupTokenThenEnable_ReturnsUsableUser(t *testing.T) {
	user := createTestUser(t, users_enums.UserRoleAdmin)

	setupToken, err := mfaService.IssueSetupChallengeToken(user.ID)
	require.NoError(t, err)

	setup, err := mfaService.SetupWithToken(setupToken)
	require.NoError(t, err)

	enabledUser, err := mfaService.EnableWithToken(setupToken, codeFor(t, setup.Secret))
	require.NoError(t, err)
	assert.Equal(t, user.ID, enabledUser.ID)
	assert.True(t, enabledUser.MFAEnabled)
}

func Test_VerifyLoginToken_WithReusedCode_Rejected(t *testing.T) {
	user := createTestUser(t, users_enums.UserRoleMember)
	setup, err := mfaService.Setup(user)
	require.NoError(t, err)
	code := codeFor(t, setup.Secret)
	reloadedForEnable, err := userRepository.GetUserByID(user.ID)
	require.NoError(t, err)
	require.NoError(t, mfaService.Enable(reloadedForEnable, code))

	reloaded, err := userRepository.GetUserByID(user.ID)
	require.NoError(t, err)

	loginToken, err := mfaService.IssueLoginChallengeToken(reloaded.ID)
	require.NoError(t, err)

	// Use the next step's code: Enable already consumed the current one.
	freshCode := nextCodeFor(t, setup.Secret)

	_, err = mfaService.VerifyLoginToken(loginToken, freshCode)
	require.NoError(t, err)

	// Replaying the exact same code that just succeeded must be rejected.
	loginToken2, err := mfaService.IssueLoginChallengeToken(reloaded.ID)
	require.NoError(t, err)
	_, err = mfaService.VerifyLoginToken(loginToken2, freshCode)
	assert.Error(t, err)
}
