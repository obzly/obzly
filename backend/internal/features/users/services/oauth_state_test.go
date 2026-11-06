package users_services

import (
	"testing"
	"time"

	users_repositories "obzly/internal/features/users/repositories"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_SignVerifyOAuthState_RoundTrips(t *testing.T) {
	repo := &users_repositories.SecretKeyRepository{}

	state, err := signOAuthState(repo, "github")
	require.NoError(t, err)

	require.NoError(t, verifyOAuthState(repo, state, "github"))
}

func Test_VerifyOAuthState_TamperedSignature_Rejected(t *testing.T) {
	repo := &users_repositories.SecretKeyRepository{}
	state, err := signOAuthState(repo, "github")
	require.NoError(t, err)

	tampered := state[:len(state)-4] + "AAAA"
	assert.Error(t, verifyOAuthState(repo, tampered, "github"))
}

// A state signed for one provider must not verify for another, though both
// use the same secret.
func Test_VerifyOAuthState_WrongProvider_Rejected(t *testing.T) {
	repo := &users_repositories.SecretKeyRepository{}
	state, err := signOAuthState(repo, "google")
	require.NoError(t, err)

	assert.ErrorIs(t, verifyOAuthState(repo, state, "github"), errOAuthStateInvalid)
}

func Test_VerifyOAuthState_Expired_Rejected(t *testing.T) {
	repo := &users_repositories.SecretKeyRepository{}

	expiredState, err := signOAuthStateWithExpiry(repo, "github", time.Now().UTC().Add(-time.Minute))
	require.NoError(t, err)

	assert.ErrorIs(t, verifyOAuthState(repo, expiredState, "github"), errOAuthStateExpired)
}

func Test_VerifyOAuthState_NotYetExpired_Accepted(t *testing.T) {
	repo := &users_repositories.SecretKeyRepository{}

	state, err := signOAuthStateWithExpiry(repo, "github", time.Now().UTC().Add(time.Second))
	require.NoError(t, err)

	assert.NoError(t, verifyOAuthState(repo, state, "github"))
}

func Test_VerifyOAuthState_Malformed_Rejected(t *testing.T) {
	repo := &users_repositories.SecretKeyRepository{}

	for _, malformed := range []string{"", "no-dot-here", "a.b.c", "!!!.###"} {
		err := verifyOAuthState(repo, malformed, "github")
		assert.Error(t, err, "expected %q to be rejected", malformed)
	}
}

// The old literal "github"/"google" state must be rejected as unsigned.
func Test_VerifyOAuthState_FixedLegacyStateString_Rejected(t *testing.T) {
	repo := &users_repositories.SecretKeyRepository{}

	assert.Error(t, verifyOAuthState(repo, "github", "github"))
	assert.Error(t, verifyOAuthState(repo, "google", "google"))
}
