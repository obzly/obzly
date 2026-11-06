package projects_services

import (
	"obzly/internal/cache"
	"obzly/internal/features/audit_logs"
	projects_interfaces "obzly/internal/features/projects/interfaces"
	projects_models "obzly/internal/features/projects/models"
	projects_repositories "obzly/internal/features/projects/repositories"
	users_services "obzly/internal/features/users/services"
	cache_utils "obzly/internal/util/cache"

	"sync"

	"golang.org/x/sync/singleflight"
)

var projectRepository = &projects_repositories.ProjectRepository{}
var membershipRepository = &projects_repositories.MembershipRepository{}

var projectService = &ProjectService{
	projectRepository,
	membershipRepository,
	users_services.GetUserService(),
	audit_logs.GetAuditLogService(),
	users_services.GetSettingsService(),
	users_services.GetUserPlanService(),
	[]projects_interfaces.ProjectDeletionListener{},
	cache_utils.NewCacheUtil[projects_models.Project](cache.GetCache(), "lb_project:"),
	singleflight.Group{},
}

var retentionPolicyService = &RetentionPolicyService{
	projectService,
	&projects_repositories.RetentionPolicyRepository{},
	audit_logs.GetAuditLogService(),
	sync.Map{},
}

var promotedFieldService = &PromotedFieldService{
	projectService,
	&projects_repositories.PromotedFieldRepository{},
	audit_logs.GetAuditLogService(),
	sync.Map{},
}

var membershipService = &MembershipService{
	membershipRepository,
	projectRepository,
	users_services.GetUserService(),
	audit_logs.GetAuditLogService(),
	projectService,
	users_services.GetSettingsService(),
}

func init() {
	users_services.GetUserService().SetGroupMembershipSyncer(membershipService)
}

func GetProjectService() *ProjectService {
	return projectService
}

func GetMembershipService() *MembershipService {
	return membershipService
}

func GetRetentionPolicyService() *RetentionPolicyService {
	return retentionPolicyService
}

func GetPromotedFieldService() *PromotedFieldService {
	return promotedFieldService
}
