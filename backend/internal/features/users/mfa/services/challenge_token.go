package mfa_services

import (
	"errors"
	"fmt"
	"time"

	user_repositories "obzly/internal/features/users/repositories"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// challengeTokenTTL: long enough to open an authenticator app, short enough that
// a leaked challenge token is soon useless.
const challengeTokenTTL = 10 * time.Minute

type challengePurpose string

const (
	purposeLogin challengePurpose = "mfa_login"
	purposeSetup challengePurpose = "mfa_setup"
)

var (
	errChallengeTokenInvalid      = errors.New("invalid or expired MFA token")
	errChallengeTokenWrongPurpose = errors.New("MFA token was not issued for this action")
)

// issueChallengeToken signs a short-lived token for userID with the access-token
// key but no passwordCreationTime claim, so GetUserFromToken rejects it; only
// parseChallengeToken (the MFA login endpoints) accepts it.
func issueChallengeToken(
	secretKeyRepository *user_repositories.SecretKeyRepository,
	userID uuid.UUID,
	purpose challengePurpose,
) (string, error) {
	secretKey, err := secretKeyRepository.GetSecretKey()
	if err != nil {
		return "", fmt.Errorf("failed to get secret key: %w", err)
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":     userID.String(),
		"purpose": string(purpose),
		"exp":     time.Now().UTC().Add(challengeTokenTTL).Unix(),
		"iat":     time.Now().UTC().Unix(),
	})

	signed, err := token.SignedString([]byte(secretKey))
	if err != nil {
		return "", fmt.Errorf("failed to sign MFA token: %w", err)
	}
	return signed, nil
}

func parseChallengeToken(
	secretKeyRepository *user_repositories.SecretKeyRepository,
	tokenString string,
	expectedPurpose challengePurpose,
) (uuid.UUID, error) {
	secretKey, err := secretKeyRepository.GetSecretKey()
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to get secret key: %w", err)
	}

	parsed, err := jwt.Parse(tokenString, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(secretKey), nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuedAt())
	if err != nil || !parsed.Valid {
		return uuid.Nil, errChallengeTokenInvalid
	}

	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return uuid.Nil, errChallengeTokenInvalid
	}

	purpose, _ := claims["purpose"].(string)
	if purpose != string(expectedPurpose) {
		return uuid.Nil, errChallengeTokenWrongPurpose
	}

	userIDStr, ok := claims["sub"].(string)
	if !ok {
		return uuid.Nil, errChallengeTokenInvalid
	}

	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return uuid.Nil, errChallengeTokenInvalid
	}

	return userID, nil
}
