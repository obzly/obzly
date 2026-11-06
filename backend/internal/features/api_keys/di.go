package api_keys

import (
	"time"

	"obzly/internal/cache"
	audit_logs "obzly/internal/features/audit_logs"
	projects_services "obzly/internal/features/projects/services"
	mfa_services "obzly/internal/features/users/mfa/services"
	cache_utils "obzly/internal/util/cache"

	"golang.org/x/sync/singleflight"
)

var apiKeyRepository = &ApiKeyRepository{}

var apiKeyService = &ApiKeyService{
	apiKeyRepository,
	projects_services.GetProjectService(),
	audit_logs.GetAuditLogService(),
	mfa_services.GetMFAService(),
	cache_utils.NewCacheUtil[CachedApiKey](cache.GetCache(), "lb_apikey:"),
	cache_utils.NewCacheUtil[bool](cache.GetCache(), "lb_apikey_lastused_throttle:"),
	// Same prefix (same entries), much shorter TTL: see ApiKeyService.negativeCacheUtil.
	cache_utils.NewCacheUtil[CachedApiKey](cache.GetCache(), "lb_apikey:").WithExpiry(30 * time.Second),
	singleflight.Group{},
}

var apiKeyController = &ApiKeyController{
	apiKeyService,
}

func GetApiKeyService() *ApiKeyService {
	return apiKeyService
}

func GetApiKeyController() *ApiKeyController {
	return apiKeyController
}
