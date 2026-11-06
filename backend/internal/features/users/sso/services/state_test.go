package sso_services

import (
	"testing"
	"time"

	user_repositories "obzly/internal/features/users/repositories"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_SignVerifyState_RoundTrips(t *testing.T) {
	repo := &user_repositories.SecretKeyRepository{}
	connectorID := uuid.New()

	state, err := signState(repo, connectorID)
	require.NoError(t, err)

	verified, err := verifyState(repo, state)
	require.NoError(t, err)
	assert.Equal(t, connectorID, verified)
}

func Test_VerifyState_TamperedSignature_Rejected(t *testing.T) {
	repo := &user_repositories.SecretKeyRepository{}
	state, err := signState(repo, uuid.New())
	require.NoError(t, err)

	tampered := state[:len(state)-4] + "AAAA"
	_, err = verifyState(repo, tampered)
	assert.Error(t, err)
}

// A state with a swapped connector ID (not re-signed) must be rejected.
func Test_VerifyState_TamperedConnectorID_Rejected(t *testing.T) {
	repo := &user_repositories.SecretKeyRepository{}
	realState, err := signState(repo, uuid.New())
	require.NoError(t, err)

	forgedConnectorID := uuid.New()
	forgedState, err := signState(repo, forgedConnectorID)
	require.NoError(t, err)

	realParts := splitState(t, realState)
	forgedParts := splitState(t, forgedState)
	frankenstein := forgedParts[0] + "." + realParts[1]

	_, err = verifyState(repo, frankenstein)
	assert.Error(t, err)
}

func Test_VerifyState_Expired_Rejected(t *testing.T) {
	repo := &user_repositories.SecretKeyRepository{}

	expiredState, err := signStateWithExpiry(repo, uuid.New(), time.Now().UTC().Add(-time.Minute))
	require.NoError(t, err)

	_, err = verifyState(repo, expiredState)
	assert.ErrorIs(t, err, errStateExpired)
}

func Test_VerifyState_NotYetExpired_Accepted(t *testing.T) {
	repo := &user_repositories.SecretKeyRepository{}

	state, err := signStateWithExpiry(repo, uuid.New(), time.Now().UTC().Add(time.Second))
	require.NoError(t, err)

	_, err = verifyState(repo, state)
	assert.NoError(t, err)
}

func Test_VerifyState_Malformed_Rejected(t *testing.T) {
	repo := &user_repositories.SecretKeyRepository{}

	for _, malformed := range []string{"", "no-dot-here", "a.b.c", "!!!.###"} {
		_, err := verifyState(repo, malformed)
		assert.Error(t, err, "expected %q to be rejected", malformed)
	}
}

func splitState(t *testing.T, state string) []string {
	t.Helper()
	for i := len(state) - 1; i >= 0; i-- {
		if state[i] == '.' {
			return []string{state[:i], state[i+1:]}
		}
	}
	t.Fatalf("state %q has no '.' separator", state)
	return nil
}
