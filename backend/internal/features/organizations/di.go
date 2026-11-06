package organizations

import (
	"obzly/internal/features/audit_logs"
	projects_repositories "obzly/internal/features/projects/repositories"
	users_repositories "obzly/internal/features/users/repositories"
	users_services "obzly/internal/features/users/services"
)

var service = &Service{
	&repository{},
	&users_repositories.UserRepository{},
	&projects_repositories.ProjectRepository{},
	audit_logs.GetAuditLogService(),
	users_services.GetManagementService(),
	&users_repositories.UserPlanRepository{},
}

var controller = &Controller{service}

func GetService() *Service { return service }

func GetController() *Controller { return controller }
