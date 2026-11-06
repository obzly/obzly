package users_services

import (
	"context"
	"fmt"
	"time"

	"obzly/internal/cache"
)

// revokedTokenKeyPrefix namespaces the deny-list of individually revoked tokens
// (by jti). A password change ends every session; this ends one (SignOut).
const revokedTokenKeyPrefix = "revoked_token:"

// checkTokenRevoked is isTokenRevoked, replaceable in tests.
var checkTokenRevoked = isTokenRevoked

// revokeToken keeps jti for the token's remaining lifetime, so the deny-list
// expires on its own. ttl <= 0 is a no-op.
func revokeToken(jti string, ttl time.Duration) error {
	if jti == "" || ttl <= 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := cache.GetCache()
	result := client.Do(ctx, client.B().Set().Key(revokedTokenKeyPrefix+jti).Value("1").Ex(ttl).Build())
	if result.Error() != nil {
		return fmt.Errorf("failed to revoke token: %w", result.Error())
	}
	return nil
}

// isTokenRevoked reports whether jti is on the deny-list; an empty jti can't be.
func isTokenRevoked(jti string) (bool, error) {
	if jti == "" {
		return false, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := cache.GetCache()
	result := client.Do(ctx, client.B().Exists().Key(revokedTokenKeyPrefix+jti).Build())
	if result.Error() != nil {
		return false, fmt.Errorf("failed to check token revocation: %w", result.Error())
	}

	count, err := result.AsInt64()
	if err != nil {
		return false, fmt.Errorf("failed to parse token revocation check: %w", err)
	}
	return count > 0, nil
}
