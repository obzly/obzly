package users_services

import (
	"testing"
	"time"

	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func parseUnverified(t *testing.T, token string) jwt.MapClaims {
	t.Helper()
	parsed, _, err := jwt.NewParser().ParseUnverified(token, jwt.MapClaims{})
	require.NoError(t, err)
	claims, ok := parsed.Claims.(jwt.MapClaims)
	require.True(t, ok)
	return claims
}

// The default token must keep exactly the established claim set and 30-day TTL
// that existing deployments understand.
func Test_GenerateAccessToken_ClaimSetUnchanged(t *testing.T) {
	user := &users_models.User{
		ID:                   uuid.New(),
		Role:                 users_enums.UserRoleMember,
		PasswordCreationTime: time.Now().UTC().Add(-time.Hour),
	}

	response, err := GetUserService().GenerateAccessToken(user)
	require.NoError(t, err)

	claims := parseUnverified(t, response.Token)
	keys := make([]string, 0, len(claims))
	for k := range claims {
		keys = append(keys, k)
	}
	assert.ElementsMatch(t,
		[]string{"sub", "exp", "iat", "jti", "role", "passwordCreationTime"},
		keys,
		"the default token must carry exactly the historical claim set",
	)
	assert.Equal(t, user.ID.String(), claims["sub"])
	assert.Equal(t, string(users_enums.UserRoleMember), claims["role"])
	assert.NotEmpty(t, claims["jti"])

	exp := time.Unix(int64(claims["exp"].(float64)), 0)
	iat := time.Unix(int64(claims["iat"].(float64)), 0)
	assert.InDelta(t, accessTokenTTL.Seconds(), exp.Sub(iat).Seconds(), 2, "default TTL must stay 30 days")
}

// Exchange claims and a shorter TTL apply, but ExtraClaims never override the
// reserved claims authentication relies on.
func Test_GenerateAccessTokenWithOptions_ExtraClaimsCannotShadowReserved(t *testing.T) {
	user := &users_models.User{
		ID:                   uuid.New(),
		Role:                 users_enums.UserRoleAdmin,
		PasswordCreationTime: time.Now().UTC().Add(-time.Hour),
	}

	response, err := GetUserService().GenerateAccessTokenWithOptions(user, AccessTokenOptions{
		TTL: time.Hour,
		ExtraClaims: map[string]any{
			AuthOriginClaim: AuthOriginControlPlane,
			"amr":           []string{"mfa"},
			// Attempted overrides of reserved claims, all ignored.
			"sub":  "someone-else",
			"role": "ADMIN-forever",
			"exp":  float64(time.Now().Add(365 * 24 * time.Hour).Unix()),
		},
	})
	require.NoError(t, err)

	claims := parseUnverified(t, response.Token)
	assert.Equal(t, AuthOriginControlPlane, claims[AuthOriginClaim])
	assert.Equal(t, []any{"mfa"}, claims["amr"])
	assert.Equal(t, user.ID.String(), claims["sub"], "sub must not be overridable")
	assert.Equal(t, string(users_enums.UserRoleAdmin), claims["role"], "role must not be overridable")

	exp := time.Unix(int64(claims["exp"].(float64)), 0)
	iat := time.Unix(int64(claims["iat"].(float64)), 0)
	assert.InDelta(t, time.Hour.Seconds(), exp.Sub(iat).Seconds(), 2, "custom TTL must be honored, not the injected exp")
}

// Session tokens: HS256 only; expired and future-issued tokens are refused.
func Test_GetUserAndClaimsFromToken_RefusesOtherAlgorithmsAndBadTimes(t *testing.T) {
	service := GetUserService()
	secret, err := service.secretKeyRepository.GetSecretKey()
	require.NoError(t, err)
	now := time.Now().UTC()
	claims := func(iat, exp time.Time) jwt.MapClaims {
		return jwt.MapClaims{"sub": uuid.NewString(), "iat": iat.Unix(), "exp": exp.Unix(), "jti": uuid.NewString()}
	}
	sign := func(method jwt.SigningMethod, c jwt.MapClaims) string {
		token, err := jwt.NewWithClaims(method, c).SignedString([]byte(secret))
		require.NoError(t, err)
		return token
	}
	unsigned, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims(now, now.Add(time.Hour))).
		SignedString(jwt.UnsafeAllowNoneSignatureType)
	require.NoError(t, err)

	for name, token := range map[string]string{
		"HS384":      sign(jwt.SigningMethodHS384, claims(now, now.Add(time.Hour))),
		"none":       unsigned,
		"future iat": sign(jwt.SigningMethodHS256, claims(now.Add(time.Hour), now.Add(2*time.Hour))),
		"expired":    sign(jwt.SigningMethodHS256, claims(now.Add(-2*time.Hour), now.Add(-time.Hour))),
	} {
		_, _, err := service.GetUserAndClaimsFromToken(token)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "invalid token", name)
	}

	_, _, err = service.GetUserAndClaimsFromToken(sign(jwt.SigningMethodHS256, claims(now, now.Add(time.Hour))))
	require.Error(t, err, "an unknown user")
	assert.NotContains(t, err.Error(), "invalid token", "a well-formed HS256 token passes parsing")
}
