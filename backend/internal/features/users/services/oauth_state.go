package users_services

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

	users_repositories "obzly/internal/features/users/repositories"
)

// oauthStateTTL matches sso_services' stateTTL: enough for a login with the
// provider's MFA, short enough that a leaked state is soon useless.
const oauthStateTTL = 10 * time.Minute

var (
	errOAuthStateMalformed = errors.New("malformed oauth state parameter")
	errOAuthStateExpired   = errors.New("oauth state has expired, please try signing in again")
	errOAuthStateInvalid   = errors.New("invalid oauth state parameter")
)

// signOAuthState/verifyOAuthState give the GitHub/Google flow a signed,
// provider-bound, expiring CSRF state, closing login CSRF (a victim's browser
// completing a login as the attacker). Separate from sso_services' version,
// which signs a connector ID.
func signOAuthState(secretKeyRepository *users_repositories.SecretKeyRepository, provider string) (string, error) {
	return signOAuthStateWithExpiry(secretKeyRepository, provider, time.Now().UTC().Add(oauthStateTTL))
}

// signOAuthStateWithExpiry exists so tests can build an expired token.
func signOAuthStateWithExpiry(
	secretKeyRepository *users_repositories.SecretKeyRepository,
	provider string,
	expiresAt time.Time,
) (string, error) {
	key, err := secretKeyRepository.DeriveKey()
	if err != nil {
		return "", err
	}

	// Random, so each state is unique and the SPA can check the one coming
	// back is the one it stored before leaving (sessionStorage).
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	payload := provider + "." + strconv.FormatInt(expiresAt.Unix(), 10) + "." +
		base64.RawURLEncoding.EncodeToString(random)

	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	signature := mac.Sum(nil)

	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(signature), nil
}

// verifyOAuthState also checks the provider, so a "google" state can't be
// replayed against the GitHub callback.
func verifyOAuthState(
	secretKeyRepository *users_repositories.SecretKeyRepository, state, wantProvider string,
) error {
	parts := strings.SplitN(state, ".", 2)
	if len(parts) != 2 {
		return errOAuthStateMalformed
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return errOAuthStateMalformed
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return errOAuthStateMalformed
	}

	key, err := secretKeyRepository.DeriveKey()
	if err != nil {
		return err
	}

	mac := hmac.New(sha256.New, key)
	mac.Write(payloadBytes)
	expectedSignature := mac.Sum(nil)
	if subtle.ConstantTimeCompare(signature, expectedSignature) != 1 {
		return errOAuthStateInvalid
	}

	payloadParts := strings.SplitN(string(payloadBytes), ".", 3)
	if len(payloadParts) != 3 {
		return errOAuthStateMalformed
	}

	if payloadParts[0] != wantProvider {
		return errOAuthStateInvalid
	}

	expiry, err := strconv.ParseInt(payloadParts[1], 10, 64)
	if err != nil {
		return errOAuthStateMalformed
	}
	if time.Now().UTC().Unix() > expiry {
		return errOAuthStateExpired
	}

	return nil
}
