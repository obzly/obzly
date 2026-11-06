package audit_logs

import (
	"obzly/internal/features/sharedconfig"
	mfa_services "obzly/internal/features/users/mfa/services"
	users_services "obzly/internal/features/users/services"
	sso_services "obzly/internal/features/users/sso/services"
	"obzly/internal/util/logger"
)

var auditLogRepository = &AuditLogRepository{}
var auditLogService = &AuditLogService{
	auditLogRepository: auditLogRepository,
	logger:             logger.GetLogger(),
}
var auditLogController = &AuditLogController{
	auditLogService: auditLogService,
}

func GetAuditLogService() *AuditLogService {
	return auditLogService
}

func GetAuditLogController() *AuditLogController {
	return auditLogController
}

func SetupDependencies() {
	sharedconfig.SetAuditLogWriter(auditLogService)
	users_services.GetUserService().SetAuditLogWriter(auditLogService)
	users_services.GetSettingsService().SetAuditLogWriter(auditLogService)
	users_services.GetManagementService().SetAuditLogWriter(auditLogService)
	users_services.GetUserPlanService().SetAuditLogWriter(auditLogService)
	sso_services.GetConnectorService().SetAuditLogWriter(auditLogService)
	mfa_services.GetMFAService().SetAuditLogWriter(auditLogService)
}
