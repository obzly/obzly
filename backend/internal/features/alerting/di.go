package alerting

import (
	"time"

	"obzly/internal/features/audit_logs"
	users_repositories "obzly/internal/features/users/repositories"
	"obzly/internal/util/logger"
)

var alertRepository = &repository{}

var evaluator = &Evaluator{alertRepository, logger.GetLogger(), func() time.Time { return time.Now().UTC() }}

var dispatcher = &Dispatcher{
	alertRepository,
	&users_repositories.SecretKeyRepository{},
	newSender(),
	logger.GetLogger(),
	func() time.Time { return time.Now().UTC() },
	breaker{},
}

var service = &Service{
	alertRepository,
	&users_repositories.SecretKeyRepository{},
	audit_logs.GetAuditLogService(),
	dispatcher.sender,
	evaluator,
}

var controller = &Controller{service}

var settingsService = &SettingsService{
	alertRepository,
	dispatcher.sender,
	audit_logs.GetAuditLogService(),
	&users_repositories.SecretKeyRepository{},
}

var settingsController = &SettingsController{settingsService}

func GetSettingsController() *SettingsController { return settingsController }

func GetController() *Controller { return controller }

func GetEvaluator() *Evaluator { return evaluator }

func GetDispatcher() *Dispatcher { return dispatcher }
