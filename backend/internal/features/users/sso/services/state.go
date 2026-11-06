package sso_services

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"

	user_repositories "obzly/internal/features/users/repositories"

	"github.com/google/uuid"
)

// stateTTL: enough for a login with the provider's MFA, short enough that a
// leaked state is soon useless.
const stateTTL = 10 * time.Minute

var (
	errStateMalformed = errors.New("malformed state parameter")
	errStateExpired   = errors.New("sso state has expired, please try signing in again")
	errStateInvalid   = errors.New("invalid sso state parameter")
)

// signState builds a self-contained HMAC-signed CSRF token bound to one connector,
// since there's no server-side session store.
func signState(secretKeyRepository *user_repositories.SecretKeyRepository, connectorID uuid.UUID) (string, error) {
	return signStateWithExpiry(secretKeyRepository, connectorID, time.Now().UTC().Add(stateTTL))
}

// signStateWithExpiry exists so tests can build an expired token.
func signStateWithExpiry(
	secretKeyRepository *user_repositories.SecretKeyRepository,
	connectorID uuid.UUID,
	expiresAt time.Time,
) (string, error) {
	key, err := secretKeyRepository.DeriveKey()
	if err != nil {
		return "", err
	}

	// A random part makes each state unique, so the SPA can recognise its own and
	// the derived nonce is single-use.
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}

	expiry := expiresAt.Unix()
	payload := connectorID.String() + "." + strconv.FormatInt(expiry, 10) + "." +
		base64.RawURLEncoding.EncodeToString(random)

	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	signature := mac.Sum(nil)

	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(signature), nil
}

// NonceForState derives the OIDC nonce from state; there's no session to store it in.
func NonceForState(state string) string {
	sum := sha256.Sum256([]byte("obzly-oidc-nonce:" + state))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func verifyState(secretKeyRepository *user_repositories.SecretKeyRepository, state string) (uuid.UUID, error) {
	parts := strings.SplitN(state, ".", 2)
	if len(parts) != 2 {
		return uuid.Nil, errStateMalformed
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return uuid.Nil, errStateMalformed
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return uuid.Nil, errStateMalformed
	}

	key, err := secretKeyRepository.DeriveKey()
	if err != nil {
		return uuid.Nil, err
	}

	mac := hmac.New(sha256.New, key)
	mac.Write(payloadBytes)
	expectedSignature := mac.Sum(nil)
	if subtle.ConstantTimeCompare(signature, expectedSignature) != 1 {
		return uuid.Nil, errStateInvalid
	}

	payloadParts := strings.SplitN(string(payloadBytes), ".", 3)
	if len(payloadParts) != 3 {
		return uuid.Nil, errStateMalformed
	}

	connectorID, err := uuid.Parse(payloadParts[0])
	if err != nil {
		return uuid.Nil, errStateMalformed
	}

	expiry, err := strconv.ParseInt(payloadParts[1], 10, 64)
	if err != nil {
		return uuid.Nil, errStateMalformed
	}
	if time.Now().UTC().Unix() > expiry {
		return uuid.Nil, errStateExpired
	}

	return connectorID, nil
}
