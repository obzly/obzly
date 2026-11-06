package projects_services

import (
	"context"
	"errors"
	"fmt"
	"time"

	"obzly/internal/authz"
	audit_logs "obzly/internal/features/audit_logs"
	"obzly/internal/features/orgplan"
	projects_dto "obzly/internal/features/projects/dto"
	projects_interfaces "obzly/internal/features/projects/interfaces"
	projects_models "obzly/internal/features/projects/models"
	projects_repositories "obzly/internal/features/projects/repositories"
	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"
	users_services "obzly/internal/features/users/services"
	cache_utils "obzly/internal/util/cache"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
)

// SystemProjectName is the reserved system project's name (EnsureSystemProject).
const SystemProjectName = "Obzly (system)"

// systemProjectRetentionDays: the system project holds sampled request
// traces for troubleshooting Obzly itself, not long-term history.
const systemProjectRetentionDays = 7

// ErrSystemProjectNotDeletable: it is recreated at the next start anyway, and
// deleting it underneath a running exporter would fail its writes.
var (
	ErrSystemProjectNotDeletable = errors.New("the system project cannot be deleted")
	// ErrPlanLimit: the organization's plan allows no more projects here.
	ErrPlanLimit = errors.New("plan limit reached")
)

type ProjectService struct {
	projectRepository        *projects_repositories.ProjectRepository
	membershipRepository     *projects_repositories.MembershipRepository
	userService              *users_services.UserService
	auditLogService          *audit_logs.AuditLogService
	settingsService          *users_services.SettingsService
	userPlanService          *users_services.UserPlanService
	projectDeletionListeners []projects_interfaces.ProjectDeletionListener

	projectCacheUtil *cache_utils.CacheUtil[projects_models.Project]
	singleflight     singleflight.Group // Prevents thundering herd on DB calls
}

func (s *ProjectService) AddProjectDeletionListener(listener projects_interfaces.ProjectDeletionListener) {
	s.projectDeletionListeners = append(s.projectDeletionListeners, listener)
}

func (s *ProjectService) CreateProject(
	request *projects_dto.CreateProjectRequestDTO,
	creator *users_models.User,
) (*projects_dto.ProjectResponseDTO, error) {
	settings, err := s.settingsService.GetSettings()

	if err != nil {
		return nil, fmt.Errorf("failed to get settings: %w", err)
	}

	if !creator.CanCreateProjects(settings) {
		return nil, errors.New("insufficient permissions to create projects")
	}

	if limit := orgplan.Current().MaxProjects; limit > 0 {
		count, err := s.projectRepository.CountUserProjects()
		if err != nil {
			return nil, fmt.Errorf("failed to count projects: %w", err)
		}
		if count >= int64(limit) {
			return nil, fmt.Errorf("%w: the organization's plan allows %d projects on this Site", ErrPlanLimit, limit)
		}
	}

	project := &projects_models.Project{
		ID:   uuid.New(),
		Name: request.Name,
		// Require an API key by default so a new project never accepts unauthenticated
		// ingestion; owners can turn it off.
		IsApiKeyRequired:      true,
		IsFilterByDomain:      false,
		IsFilterByIP:          false,
		AllowedDomainsRaw:     "",
		AllowedIPsRaw:         "",
		IsRateLimitingEnabled: true,
		LogsPerSecondLimit:    1000,
		MaxLogsAmount:         100_000_000,
		MaxLogsSizeMB:         100_000, // 100 GB
		MaxLogsLifeDays:       180,
		MaxLogSizeKB:          64,
		CreatedAt:             time.Now().UTC(),
	}

	var plan *users_models.UserPlan

	// The request's user comes from the auth path, which skips the plan.
	if creator.Plan == nil && creator.PlanID != nil {
		if withPlan, err := s.userService.GetUserByID(creator.ID); err == nil && withPlan != nil {
			creator = withPlan
		}
	}

	if s.CanCreateOneMoreProjectForUserPlan(creator) {
		plan = creator.Plan
	} else {
		plan, err = s.userPlanService.GetDefaultPlan()
		if err != nil {
			return nil, fmt.Errorf("failed to get default plan: %w", err)
		}
	}

	if plan != nil {
		project.PlanID = &plan.ID
		project.SetLimitsFromPlan(plan)
	}

	membership := &projects_models.ProjectMembership{
		UserID: creator.ID,
		Role:   users_enums.ProjectRoleOwner,
	}

	// One transaction: a project without its owner membership could never be managed or
	// deleted again.
	if err := s.projectRepository.CreateProjectWithOwnerMembership(project, membership); err != nil {
		return nil, fmt.Errorf("failed to create project: %w", err)
	}

	// Pre-warm cache with new project for immediate availability
	s.projectCacheUtil.Set(project.ID.String(), project)

	s.auditLogService.WriteAuditLog(
		fmt.Sprintf("Project created: %s", project.Name),
		&creator.ID,
		&project.ID,
	)

	ownerRole := users_enums.ProjectRoleOwner
	return &projects_dto.ProjectResponseDTO{
		ID:        project.ID,
		Name:      project.Name,
		CreatedAt: project.CreatedAt,
		UserRole:  &ownerRole,
	}, nil
}

func (s *ProjectService) GetProject(projectID uuid.UUID, user *users_models.User) (*projects_models.Project, error) {
	isCanAccess, _, err := s.CanUserAccessProject(projectID, user)

	if err != nil {
		return nil, err
	}
	if !isCanAccess {
		return nil, errors.New("insufficient permissions to view project")
	}

	project, err := s.projectRepository.GetProjectByID(projectID)
	if err != nil {
		return nil, err
	}
	if limits := orgplan.Current(); limits != (orgplan.Limits{}) {
		project.OrgPlan = &limits
	}
	return project, nil
}

func (s *ProjectService) GetUserProjects(user *users_models.User) (*projects_dto.ListProjectsResponseDTO, error) {
	projects, err := s.membershipRepository.GetProjectsWithRolesByUserID(user.Role, user.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user projects: %w", err)
	}

	return &projects_dto.ListProjectsResponseDTO{
		Projects: projects,
	}, nil
}

func (s *ProjectService) UpdateProject(
	projectID uuid.UUID,
	updateDTO *projects_models.Project,
	user *users_models.User,
) (*projects_models.Project, error) {
	canManage, err := s.CanUserManageProject(projectID, user)

	if err != nil {
		return nil, err
	}
	if !canManage {
		return nil, errors.New("insufficient permissions to update project")
	}

	existingProject, err := s.projectRepository.GetProjectByID(projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to get project: %w", err)
	}

	updateDTO.ID = projectID
	updateDTO.CreatedAt = existingProject.CreatedAt

	existingProject.UpdateFromDTO(updateDTO)

	if err := s.projectRepository.UpdateProject(existingProject); err != nil {
		return nil, fmt.Errorf("failed to update project: %w", err)
	}

	s.projectCacheUtil.Invalidate(projectID.String())

	s.auditLogService.WriteAuditLog(
		fmt.Sprintf("Project updated: %s", updateDTO.Name),
		&user.ID,
		&projectID,
	)

	return existingProject, nil
}

// DeleteProject deletes the row (cascading to every project-scoped table) only after
// every projectDeletionListener has cleaned up its non-relational data (e.g. on-disk
// segments), which no transaction can cover. If a listener fails, the row is kept so
// the operator sees the error and can retry; listeners must be safe to re-run.
func (s *ProjectService) DeleteProject(projectID uuid.UUID, user *users_models.User) error {
	if user.Role != users_enums.UserRoleAdmin {
		userProjectRole, err := s.GetUserProjectRole(projectID, user.ID)
		if err != nil {
			return fmt.Errorf("failed to get user role: %w", err)
		}

		if userProjectRole == nil || *userProjectRole != users_enums.ProjectRoleOwner {
			return errors.New("only project owner or admin can delete project")
		}
	}

	project, err := s.projectRepository.GetProjectByID(projectID)
	if err != nil {
		return fmt.Errorf("failed to get project: %w", err)
	}
	if project.IsSystem {
		return ErrSystemProjectNotDeletable
	}

	for _, listener := range s.projectDeletionListeners {
		if err := listener.OnBeforeProjectDeletion(projectID); err != nil {
			return fmt.Errorf("failed to delete project: %w", err)
		}
	}

	if err := s.projectRepository.DeleteProject(projectID); err != nil {
		return fmt.Errorf("failed to delete project: %w", err)
	}

	s.projectCacheUtil.Invalidate(projectID.String())

	s.auditLogService.WriteAuditLog(
		fmt.Sprintf("Project deleted: %s", project.Name),
		&user.ID,
		&projectID,
	)

	return nil
}

// EnsureSystemProject returns the reserved project Obzly's own request traces go to,
// creating it on first use. It has no members (only global admins see it) and requires
// an API key it doesn't have, so nothing external can ingest into it. Safe on N
// replicas: a unique partial index lets one insert win.
func (s *ProjectService) EnsureSystemProject() (*projects_models.Project, error) {
	existing, err := s.projectRepository.GetSystemProject()
	if err != nil || existing != nil {
		return existing, err
	}

	project := &projects_models.Project{
		Name:                  SystemProjectName,
		IsSystem:              true,
		IsApiKeyRequired:      true,
		IsRateLimitingEnabled: true,
		LogsPerSecondLimit:    1000,
		MaxLogsAmount:         10_000_000,
		MaxLogsSizeMB:         5_000,
		MaxLogsLifeDays:       systemProjectRetentionDays,
		MaxLogSizeKB:          64,
	}
	if err := s.projectRepository.CreateProject(project); err != nil {
		// Another replica created it first.
		if existing, getErr := s.projectRepository.GetSystemProject(); getErr == nil && existing != nil {
			return existing, nil
		}
		return nil, fmt.Errorf("failed to create system project: %w", err)
	}
	return project, nil
}

func (s *ProjectService) GetUserProjectRole(projectID uuid.UUID, userID uuid.UUID) (*users_enums.ProjectRole, error) {
	return s.membershipRepository.GetUserProjectRole(projectID, userID)
}

// CanUserAccessProject and CanUserManageProject wrap authz.Authorize (ProjectRead,
// ProjectManage); new code calls authz directly. A failed lookup reads as no access.
func (s *ProjectService) CanUserAccessProject(
	projectID uuid.UUID,
	user *users_models.User,
) (bool, *users_enums.ProjectRole, error) {
	decision, err := authz.Authorize(context.Background(), user, authz.ProjectRead, authz.Project(projectID))
	if err != nil {
		return false, nil, nil
	}
	return decision.Allowed, decision.Role, nil
}

func (s *ProjectService) CanUserManageProject(projectID uuid.UUID, user *users_models.User) (bool, error) {
	decision, err := authz.Authorize(context.Background(), user, authz.ProjectManage, authz.Project(projectID))
	return decision.Allowed, err
}

func (s *ProjectService) GetProjectAuditLogs(
	projectID uuid.UUID,
	user *users_models.User,
	request *audit_logs.GetAuditLogsRequest,
) (*audit_logs.GetAuditLogsResponse, error) {
	isCanAccess, _, err := s.CanUserAccessProject(projectID, user)
	if err != nil {
		return nil, err
	}
	if !isCanAccess {
		return nil, errors.New("insufficient permissions to view project audit logs")
	}

	return s.auditLogService.GetProjectAuditLogs(projectID, request)
}

func (s *ProjectService) GetProjectWithCache(projectID uuid.UUID) (*projects_models.Project, error) {
	projectIDStr := projectID.String()

	// Tier 1: cache
	if cachedProject := s.projectCacheUtil.Get(projectIDStr); cachedProject != nil {
		if cachedProject.IsNotExists {
			return nil, errors.New("project not found")
		}

		return cachedProject, nil
	}

	// Tier 2: Database lookup with singleflight protection (prevents thundering herd)
	result, err, _ := s.singleflight.Do(projectIDStr, func() (any, error) {
		return s.projectRepository.GetProjectByID(projectID)
	})

	if err != nil {
		// Cache the invalid project to prevent future DB hits
		invalidCachedProject := &projects_models.Project{
			ID:          projectID,
			IsNotExists: true,
		}
		s.projectCacheUtil.Set(projectIDStr, invalidCachedProject)
		return nil, errors.New("project not found")
	}

	project, ok := result.(*projects_models.Project)
	if !ok {
		return nil, fmt.Errorf("failed to cast result to Project")
	}

	// Cache the valid project
	s.projectCacheUtil.Set(projectIDStr, project)

	return project, nil
}

func (s *ProjectService) GetAllProjects() ([]*projects_models.Project, error) {
	return s.projectRepository.GetAllProjects()
}

func (s *ProjectService) CanCreateOneMoreProjectForUserPlan(user *users_models.User) bool {
	if user.Plan == nil {
		return false
	}

	if user.Plan.Type == users_enums.UserPlanTypeDefault {
		return true
	}

	if user.Plan.AllowedProjectsCount == 0 {
		// means unlimited
		return true
	}

	projectsCountWithSamePlan, err := s.projectRepository.GetProjectsCountByOwnerIDAndPlanID(user.ID, user.Plan.ID)
	if err != nil {
		return false
	}

	return projectsCountWithSamePlan < int64(user.Plan.AllowedProjectsCount)
}
