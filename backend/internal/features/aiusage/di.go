package aiusage

import (
	"obzly/internal/config"
	"obzly/internal/features/audit_logs"
	projects_services "obzly/internal/features/projects/services"
)

var aiUsageService = &AIUsageService{
	projects_services.GetProjectService(),
	audit_logs.GetAuditLogService(),
	valkeySpendCache{},
	storedSpend{},
	func() int64 { return config.GetEnv().QueryMaxScanBytes },
}

var aiUsageController = &AIUsageController{
	aiUsageService,
}

var budgetController = &BudgetController{
	aiUsageService,
}

func GetAIUsageController() *AIUsageController {
	return aiUsageController
}

func GetBudgetController() *BudgetController {
	return budgetController
}

func GetAIUsageService() *AIUsageService {
	return aiUsageService
}
