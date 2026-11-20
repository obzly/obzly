package mfa_services

import (
	"testing"
	"time"

	user_repositories "obzly/internal/features/users/repositories"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// decodeClaimsForTest reads claims without verifying the signature.
func decodeClaimsForTest(tokenString string) (jwt.MapClaims, error) {
	parser := jwt.Parser{}
	token, _, err := parser.ParseUnverified(tokenString, jwt.MapClaims{})
	if err != nil {
		return nil, err
	}
	return token.Claims.(jwt.MapClaims), nil
}

func Test_IssueParseChallengeToken_RoundTrips(t *testing.T) {
	repo := &user_repositories.SecretKeyRepository{}
	userID := uuid.New()

	token, err := issueChallengeToken(repo, userID, purposeLogin)
	require.NoError(t, err)

	parsedID, err := parseChallengeToken(repo, token, purposeLogin)
	require.NoError(t, err)
	assert.Equal(t, userID, parsedID)
}

// Sign-in issues the token on one replica and the code check may run on
// another whose clock is slightly behind.
func Test_ParseChallengeToken_WhenIssuedSecondsAheadOnAnotherReplica_Accepts(t *testing.T) {
	repo := &user_repositories.SecretKeyRepository{}
	secret, err := repo.GetSecretKey()
	require.NoError(t, err)
	userID := uuid.New()
	now := time.Now().UTC()
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": userID.String(), "purpose": string(purposeLogin),
		"iat": now.Add(2 * time.Second).Unix(), "exp": now.Add(5 * time.Minute).Unix(),
	}).SignedString([]byte(secret))
	require.NoError(t, err)

	parsedID, err := parseChallengeToken(repo, token, purposeLogin)

	require.NoError(t, err)
	assert.Equal(t, userID, parsedID)
}

func Test_ParseChallengeToken_WrongPurpose_Rejected(t *testing.T) {
	repo := &user_repositories.SecretKeyRepository{}
	userID := uuid.New()

	token, err := issueChallengeToken(repo, userID, purposeSetup)
	require.NoError(t, err)

	_, err = parseChallengeToken(repo, token, purposeLogin)
	assert.ErrorIs(t, err, errChallengeTokenWrongPurpose)
}

func Test_ParseChallengeToken_Garbage_Rejected(t *testing.T) {
	repo := &user_repositories.SecretKeyRepository{}

	_, err := parseChallengeToken(repo, "not-a-jwt", purposeLogin)
	assert.ErrorIs(t, err, errChallengeTokenInvalid)
}

// A challenge token is signed with the access-token key, so it must lack
// passwordCreationTime or it would pass GetUserFromToken without a code.
func Test_ChallengeToken_MissingPasswordCreationTimeClaim(t *testing.T) {
	repo := &user_repositories.SecretKeyRepository{}
	token, err := issueChallengeToken(repo, uuid.New(), purposeLogin)
	require.NoError(t, err)

	claims, err := decodeClaimsForTest(token)
	require.NoError(t, err)

	_, hasPasswordCreationTime := claims["passwordCreationTime"]
	assert.False(t, hasPasswordCreationTime,
		"a challenge token must never carry passwordCreationTime - that's what makes GetUserFromToken reject it")
}
