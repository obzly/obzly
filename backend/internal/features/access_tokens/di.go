package access_tokens

import (
	"time"

	"obzly/internal/cache"
	audit_logs "obzly/internal/features/audit_logs"
	mfa_services "obzly/internal/features/users/mfa/services"
	users_repositories "obzly/internal/features/users/repositories"
	cache_utils "obzly/internal/util/cache"
)

var accessTokenService = &AccessTokenService{
	&AccessTokenRepository{},
	&users_repositories.UserRepository{},
	audit_logs.GetAuditLogService(),
	mfa_services.GetMFAService(),
	cache_utils.NewCacheUtil[bool](cache.GetCache(), "obz_pat_lastused:").WithExpiry(5 * time.Minute),
}

var accessTokenController = &AccessTokenController{accessTokenService}

func GetAccessTokenService() *AccessTokenService {
	return accessTokenService
}

func GetAccessTokenController() *AccessTokenController {
	return accessTokenController
}
