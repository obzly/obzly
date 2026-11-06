package mfa_services

import (
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_GenerateSecret_ProducesValidGoogleAuthenticatorCompatibleKey(t *testing.T) {
	key, err := generateSecret("person@example.com")
	require.NoError(t, err)

	assert.NotEmpty(t, key.Secret())
	assert.Equal(t, "Obzly", key.Issuer())
	assert.Equal(t, "person@example.com", key.AccountName())

	// A code from pquerna/otp directly must validate, so the secret works with real authenticator apps.
	code, err := totp.GenerateCode(key.Secret(), time.Now())
	require.NoError(t, err)
	assert.True(t, totp.Validate(code, key.Secret()))
}

func Test_VerifyAndFindStep_ValidCode_ReturnsCurrentStep(t *testing.T) {
	key, err := generateSecret("a@example.com")
	require.NoError(t, err)

	code, err := totp.GenerateCode(key.Secret(), time.Now())
	require.NoError(t, err)

	step, err := verifyAndFindStep(key.Secret(), code, 0)
	require.NoError(t, err)
	assert.Equal(t, currentStep(), step)
}

func Test_VerifyAndFindStep_WrongCode_Rejected(t *testing.T) {
	key, err := generateSecret("a@example.com")
	require.NoError(t, err)

	_, err = verifyAndFindStep(key.Secret(), "000000", 0)
	assert.ErrorIs(t, err, errInvalidCode)
}

// A consumed code must be refused again even within its validity window.
func Test_VerifyAndFindStep_SameCodeTwice_SecondRejectedAsReplay(t *testing.T) {
	key, err := generateSecret("a@example.com")
	require.NoError(t, err)

	code, err := totp.GenerateCode(key.Secret(), time.Now())
	require.NoError(t, err)

	step, err := verifyAndFindStep(key.Secret(), code, 0)
	require.NoError(t, err)

	_, err = verifyAndFindStep(key.Secret(), code, step)
	assert.ErrorIs(t, err, errCodeReused)
}

// An earlier valid code must be refused once a later one was consumed.
func Test_VerifyAndFindStep_OlderCodeAfterNewerConsumed_Rejected(t *testing.T) {
	key, err := generateSecret("a@example.com")
	require.NoError(t, err)

	now := time.Now()
	olderCode, err := totp.GenerateCodeCustom(key.Secret(), now.Add(-totpPeriodSeconds*time.Second), validateOpts)
	require.NoError(t, err)
	newerCode, err := totp.GenerateCodeCustom(key.Secret(), now, validateOpts)
	require.NoError(t, err)

	newerStep, err := verifyAndFindStep(key.Secret(), newerCode, 0)
	require.NoError(t, err)

	_, err = verifyAndFindStep(key.Secret(), olderCode, newerStep)
	assert.Error(t, err)
}

func Test_VerifyAndFindStep_WithinSkewWindow_Accepted(t *testing.T) {
	key, err := generateSecret("a@example.com")
	require.NoError(t, err)

	// One period back is within the skew tolerance.
	pastCode, err := totp.GenerateCodeCustom(
		key.Secret(), time.Now().Add(-totpPeriodSeconds*time.Second), validateOpts,
	)
	require.NoError(t, err)

	_, err = verifyAndFindStep(key.Secret(), pastCode, 0)
	assert.NoError(t, err)
}
