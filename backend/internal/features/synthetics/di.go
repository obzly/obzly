package synthetics

import (
	"time"

	"obzly/internal/features/audit_logs"
	users_repositories "obzly/internal/features/users/repositories"
)

var service = &Service{
	&repository{},
	&users_repositories.SecretKeyRepository{},
	audit_logs.GetAuditLogService(),
	func() time.Time { return time.Now().UTC() },
}

var controller = &Controller{service}

var probeController = &ProbeController{service}

func GetController() *Controller { return controller }

func GetProbeController() *ProbeController { return probeController }

func GetService() *Service { return service }
