package oauth

import (
	"time"

	"obzly/internal/cache"
	access_tokens "obzly/internal/features/access_tokens"
	audit_logs "obzly/internal/features/audit_logs"
	users_repositories "obzly/internal/features/users/repositories"
	cache_utils "obzly/internal/util/cache"
	"obzly/internal/util/rate_limit"
)

var oauthService = &OAuthService{
	&OAuthRepository{},
	&users_repositories.UserRepository{},
	access_tokens.GetAccessTokenService(),
	audit_logs.GetAuditLogService(),
	cache.GetCache(),
	cache_utils.NewCacheUtil[client](cache.GetCache(), "oauth_cimd:").WithExpiry(time.Hour),
}

var oauthController = &OAuthController{oauthService, rate_limit.NewRateLimiter()}

func GetOAuthService() *OAuthService {
	return oauthService
}

func GetOAuthController() *OAuthController {
	return oauthController
}
