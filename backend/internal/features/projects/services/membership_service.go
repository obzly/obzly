package projects_services

import (
	"errors"
	"fmt"

	audit_logs "obzly/internal/features/audit_logs"
	projects_dto "obzly/internal/features/projects/dto"
	projects_models "obzly/internal/features/projects/models"
	projects_repositories "obzly/internal/features/projects/repositories"
	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"
	users_services "obzly/internal/features/users/services"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type MembershipService struct {
	membershipRepository *projects_repositories.MembershipRepository
	projectRepository    *projects_repositories.ProjectRepository
	userService          *users_services.UserService
	auditLogService      *audit_logs.AuditLogService
	projectService       *ProjectService
	settingsService      *users_services.SettingsService
}

func (s *MembershipService) GetMembers(
	projectID uuid.UUID,
	user *users_models.User,
) (*projects_dto.GetMembersResponseDTO, error) {
	canAccess, _, err := s.projectService.CanUserAccessProject(projectID, user)

	if err != nil {
		return nil, err
	}
	if !canAccess {
		return nil, errors.New("insufficient permissions to view project members")
	}

	members, err := s.membershipRepository.GetProjectMembers(projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to get project members: %w", err)
	}

	membersList := make([]projects_dto.ProjectMemberResponseDTO, len(members))
	for i, member := range members {
		membersList[i] = *member
	}

	return &projects_dto.GetMembersResponseDTO{
		Members: membersList,
	}, nil
}

func (s *MembershipService) AddMember(
	projectID uuid.UUID,
	request *projects_dto.AddMemberRequestDTO,
	addedBy *users_models.User,
) (*projects_dto.AddMemberResponseDTO, error) {
	if err := s.validateCanManageMembership(projectID, addedBy, request.Role); err != nil {
		return nil, err
	}

	targetUser, err := s.userService.GetUserByEmail(request.Email)
	if err != nil {
		return nil, err
	}

	if targetUser == nil {
		// User doesn't exist, invite them
		settings, err := s.settingsService.GetSettings()
		if err != nil {
			return nil, fmt.Errorf("failed to get settings: %w", err)
		}

		if !addedBy.CanInviteUsers(settings) {
			return nil, errors.New("insufficient permissions to invite users")
		}

		inviteRequest := &users_dto.InviteUserRequestDTO{
			Email:               request.Email,
			IntendedProjectID:   &projectID,
			IntendedProjectRole: &request.Role,
		}

		inviteResponse, err := s.userService.InviteUserForProject(inviteRequest, addedBy, projectID)
		if err != nil {
			return nil, err
		}

		membership := &projects_models.ProjectMembership{
			UserID:    inviteResponse.ID,
			ProjectID: projectID,
			Role:      request.Role,
		}

		// The invite and the membership are separate writes that can't share a
		// transaction. If this insert fails, the INVITED user grants no access and a
		// retry finds it by email and only creates the membership; the error says so.
		if err := s.membershipRepository.CreateMembership(membership); err != nil {
			return nil, fmt.Errorf(
				"user %s was invited, but adding them to the project failed - retrying will attach the existing invite: %w",
				request.Email,
				err,
			)
		}

		s.auditLogService.WriteAuditLog(
			fmt.Sprintf("User invited to project: %s and added as %s", request.Email, request.Role),
			&addedBy.ID,
			&projectID,
		)

		return &projects_dto.AddMemberResponseDTO{
			Status:      projects_dto.AddStatusInvited,
			InviteToken: inviteResponse.InviteToken,
		}, nil
	}

	existingMembership, _ := s.membershipRepository.GetMembershipByUserAndProject(targetUser.ID, projectID)
	if existingMembership != nil {
		return nil, errors.New("user is already a member of this project")
	}

	membership := &projects_models.ProjectMembership{
		UserID:    targetUser.ID,
		ProjectID: projectID,
		Role:      request.Role,
	}

	if err := s.membershipRepository.CreateMembership(membership); err != nil {
		return nil, fmt.Errorf("failed to add member: %w", err)
	}

	s.auditLogService.WriteAuditLog(
		fmt.Sprintf("User added to project: %s as %s", targetUser.Email, request.Role),
		&addedBy.ID,
		&projectID,
	)

	return &projects_dto.AddMemberResponseDTO{
		Status: projects_dto.AddStatusAdded,
	}, nil
}

func (s *MembershipService) ChangeMemberRole(
	projectID uuid.UUID,
	memberUserID uuid.UUID,
	request *projects_dto.ChangeMemberRoleRequestDTO,
	changedBy *users_models.User,
) error {
	if err := s.validateCanManageMembership(projectID, changedBy, request.Role); err != nil {
		return err
	}

	if memberUserID == changedBy.ID {
		return errors.New("cannot change your own role")
	}

	existingMembership, err := s.membershipRepository.GetMembershipByUserAndProject(memberUserID, projectID)
	if err != nil {
		return errors.New("user is not a member of this project")
	}

	if existingMembership.Role == users_enums.ProjectRoleOwner {
		return errors.New("cannot change owner role")
	}

	if err := s.requireCanManageAdmins(projectID, changedBy, existingMembership.Role); err != nil {
		return err
	}

	targetUser, err := s.userService.GetUserByID(memberUserID)
	if err != nil {
		return errors.New("user not found")
	}

	if err := s.membershipRepository.UpdateMemberRole(memberUserID, projectID, request.Role); err != nil {
		return fmt.Errorf("failed to update member role: %w", err)
	}

	s.auditLogService.WriteAuditLog(
		fmt.Sprintf(
			"Member role changed: %s from %s to %s",
			targetUser.Email,
			existingMembership.Role,
			request.Role,
		),
		&changedBy.ID,
		&projectID,
	)

	return nil
}

func (s *MembershipService) RemoveMember(
	projectID uuid.UUID,
	memberUserID uuid.UUID,
	removedBy *users_models.User,
) error {
	canManage, err := s.projectService.CanUserManageProject(projectID, removedBy)
	if err != nil {
		return err
	}

	if !canManage {
		return errors.New("insufficient permissions to remove members")
	}

	existingMembership, err := s.membershipRepository.GetMembershipByUserAndProject(memberUserID, projectID)
	if err != nil {
		return errors.New("user is not a member of this project")
	}

	if existingMembership.Role == users_enums.ProjectRoleOwner {
		return errors.New("cannot remove project owner, transfer ownership first")
	}

	if memberUserID != removedBy.ID {
		if err := s.requireCanManageAdmins(projectID, removedBy, existingMembership.Role); err != nil {
			return err
		}
	}

	targetUser, err := s.userService.GetUserByID(memberUserID)
	if err != nil {
		return errors.New("user not found")
	}

	if err := s.membershipRepository.RemoveMember(memberUserID, projectID); err != nil {
		return fmt.Errorf("failed to remove member: %w", err)
	}

	s.auditLogService.WriteAuditLog(
		fmt.Sprintf("Member removed from project: %s", targetUser.Email),
		&removedBy.ID,
		&projectID,
	)

	return nil
}

func (s *MembershipService) TransferOwnership(
	projectID uuid.UUID,
	request *projects_dto.TransferOwnershipRequestDTO,
	user *users_models.User,
) error {
	currentRole, err := s.membershipRepository.GetUserProjectRole(projectID, user.ID)
	if err != nil {
		return fmt.Errorf("failed to get current user role: %w", err)
	}

	if user.Role != users_enums.UserRoleAdmin &&
		(currentRole == nil || *currentRole != users_enums.ProjectRoleOwner) {
		return errors.New("only project owner or admin can transfer ownership")
	}

	newOwner, err := s.userService.GetUserByEmail(request.NewOwnerEmail)
	if err != nil {
		return errors.New("new owner not found")
	}

	if newOwner == nil {
		return errors.New("new owner not found")
	}

	_, err = s.membershipRepository.GetMembershipByUserAndProject(newOwner.ID, projectID)
	if err != nil {
		return errors.New("new owner must be a project member")
	}

	currentOwner, err := s.membershipRepository.GetProjectOwner(projectID)
	if err != nil {
		return fmt.Errorf("failed to find current project owner: %w", err)
	}

	if currentOwner == nil {
		return errors.New("no current project owner found")
	}

	// Otherwise the two updates below would leave the project with no owner.
	if newOwner.ID == currentOwner.UserID {
		return errors.New("new owner must be different from the current owner")
	}

	currentProject, err := s.projectRepository.GetProjectByID(projectID)
	if err != nil {
		return fmt.Errorf("failed to get project: %w", err)
	}

	if currentProject.Plan != nil && currentProject.Plan.Type != users_enums.UserPlanTypeDefault {
		if newOwner.Plan == nil || newOwner.Plan.Type == users_enums.UserPlanTypeDefault {
			return errors.New(
				"cannot transfer ownership of project with extended plan to user with default or no plan",
			)
		}

		if !s.projectService.CanCreateOneMoreProjectForUserPlan(newOwner) {
			return errors.New("cannot transfer ownership, because new owner reached the limit of projects for his plan")
		}
	}

	if err := s.membershipRepository.SwapOwner(currentOwner.UserID, newOwner.ID, projectID); err != nil {
		return fmt.Errorf("failed to transfer ownership: %w", err)
	}

	s.auditLogService.WriteAuditLog(
		fmt.Sprintf("Project ownership transferred to: %s", newOwner.Email),
		&user.ID,
		&projectID,
	)

	return nil
}

func (s *MembershipService) validateCanManageMembership(
	projectID uuid.UUID,
	user *users_models.User,
	changesRoleTo users_enums.ProjectRole,
) error {
	// JSON binding doesn't validate the enum, and the rest of the code assumes one of
	// the four real roles.
	if !changesRoleTo.IsValid() {
		return fmt.Errorf("invalid project role: %q", changesRoleTo)
	}

	// OWNER changes only via TransferOwnership, which keeps exactly one; a second owner
	// minted here could never be removed or demoted.
	if changesRoleTo == users_enums.ProjectRoleOwner {
		return errors.New("ownership can only be changed via project transfer")
	}

	canManageProject, err := s.projectService.CanUserManageProject(projectID, user)
	if err != nil {
		return err
	}

	if !canManageProject {
		return errors.New("insufficient permissions to manage members")
	}

	return s.requireCanManageAdmins(projectID, user, changesRoleTo)
}

// requireCanManageAdmins: only the owner (or a global admin) grants, demotes or removes
// PROJECT_ADMIN, so one project admin can't remove another.
func (s *MembershipService) requireCanManageAdmins(
	projectID uuid.UUID,
	user *users_models.User,
	targetRole users_enums.ProjectRole,
) error {
	if targetRole != users_enums.ProjectRoleAdmin || user.Role == users_enums.UserRoleAdmin {
		return nil
	}
	currentRole, err := s.membershipRepository.GetUserProjectRole(projectID, user.ID)
	if err != nil {
		return err
	}
	if currentRole == nil || *currentRole != users_enums.ProjectRoleOwner {
		return errors.New("only project owner can add/manage admins")
	}
	return nil
}

// managedMembershipSelector says which memberships a sync owns (sso_connector_id or
// central_managed) and how it creates/updates them, so syncManagedMemberships is shared.
type managedMembershipSelector struct {
	list   func(userID uuid.UUID) ([]*projects_models.ProjectMembership, error)
	update func(userID, projectID uuid.UUID, role users_enums.ProjectRole) error
	create func(userID, projectID uuid.UUID, role users_enums.ProjectRole) *projects_models.ProjectMembership
	// logPrefix names the source in audit log messages, e.g. "SSO group sync".
	logPrefix string
}

// syncManagedMemberships makes the memberships sel owns for userID match
// desired (project -> role):
//   - a project in desired the user isn't in yet: membership created, tagged
//     via sel.create;
//   - already owned by sel: role updated (via sel.update) if it differs;
//   - added manually (or owned by a different selector) and OWNER
//     memberships: left untouched, so manual decisions always win;
//   - owned by sel but no longer in desired: removed.
//
// Errors on one project don't stop the others; the first is returned.
func (s *MembershipService) syncManagedMemberships(
	userID uuid.UUID,
	desired map[uuid.UUID]users_enums.ProjectRole,
	sel managedMembershipSelector,
) error {
	var firstErr error
	record := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}

	managed, err := sel.list(userID)
	if err != nil {
		return fmt.Errorf("failed to list managed memberships: %w", err)
	}
	managedByProject := make(map[uuid.UUID]*projects_models.ProjectMembership, len(managed))
	for _, membership := range managed {
		managedByProject[membership.ProjectID] = membership
	}

	for projectID, role := range desired {
		if existing, ok := managedByProject[projectID]; ok {
			if existing.Role == users_enums.ProjectRoleOwner || existing.Role == role {
				continue
			}
			if err := sel.update(userID, projectID, role); err != nil {
				record(fmt.Errorf("failed to update role in project %s: %w", projectID, err))
				continue
			}
			s.auditLogService.WriteAuditLog(
				fmt.Sprintf("%s: role changed from %s to %s", sel.logPrefix, existing.Role, role),
				&userID,
				&projectID,
			)
			continue
		}

		if _, err := s.membershipRepository.GetMembershipByUserAndProject(userID, projectID); err == nil {
			continue
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			record(fmt.Errorf("failed to check membership in project %s: %w", projectID, err))
			continue
		}

		membership := sel.create(userID, projectID, role)
		if err := s.membershipRepository.CreateMembership(membership); err != nil {
			record(fmt.Errorf("failed to add membership in project %s: %w", projectID, err))
			continue
		}
		s.auditLogService.WriteAuditLog(
			fmt.Sprintf("%s: added as %s", sel.logPrefix, role),
			&userID,
			&projectID,
		)
	}

	for projectID, membership := range managedByProject {
		if _, stillWanted := desired[projectID]; stillWanted || membership.Role == users_enums.ProjectRoleOwner {
			continue
		}
		if err := s.membershipRepository.RemoveMember(userID, projectID); err != nil {
			record(fmt.Errorf("failed to remove membership in project %s: %w", projectID, err))
			continue
		}
		s.auditLogService.WriteAuditLog(
			fmt.Sprintf("%s: removed (no longer granted)", sel.logPrefix),
			&userID, &projectID,
		)
	}

	return firstErr
}

// SyncSSOGroupMemberships makes the user's group-managed memberships match desired
// (project -> role); see syncManagedMemberships for the rules.
func (s *MembershipService) SyncSSOGroupMemberships(
	userID, connectorID uuid.UUID,
	desired map[uuid.UUID]users_enums.ProjectRole,
) error {
	return s.syncManagedMemberships(userID, desired, managedMembershipSelector{
		list: func(userID uuid.UUID) ([]*projects_models.ProjectMembership, error) {
			return s.membershipRepository.ListSSOManagedMemberships(userID, connectorID)
		},
		update: s.membershipRepository.UpdateSSOManagedRole,
		create: func(userID, projectID uuid.UUID, role users_enums.ProjectRole) *projects_models.ProjectMembership {
			return &projects_models.ProjectMembership{
				UserID: userID, ProjectID: projectID, Role: role, SSOConnectorID: &connectorID,
			}
		},
		logPrefix: "SSO group sync",
	})
}

// projectRoleRank orders project roles by privilege, for merging a
// Site-wide role with explicit per-project grants.
func projectRoleRank(role users_enums.ProjectRole) int {
	switch role {
	case users_enums.ProjectRoleOwner:
		return 4
	case users_enums.ProjectRoleAdmin:
		return 3
	case users_enums.ProjectRoleMember:
		return 2
	case users_enums.ProjectRoleViewer:
		return 1
	default:
		return 0
	}
}

// SyncCentralMemberships does the same for a control plane's token-exchange assertion
// (desired = its projects claim; owner marker: CentralManaged). allProjectsRole, from a
// Site-wide MEMBER/VIEWER grant, gives at least that role on every project, re-expanded
// on each exchange so new projects are picked up; a higher explicit grant wins.
func (s *MembershipService) SyncCentralMemberships(
	userID uuid.UUID,
	desired map[uuid.UUID]users_enums.ProjectRole,
	allProjectsRole users_enums.ProjectRole,
) error {
	if allProjectsRole != "" {
		projects, err := s.projectRepository.GetAllProjects()
		if err != nil {
			return fmt.Errorf("failed to list projects for a Site-wide grant: %w", err)
		}
		merged := make(map[uuid.UUID]users_enums.ProjectRole, len(projects)+len(desired))
		for projectID, role := range desired {
			merged[projectID] = role
		}
		for _, project := range projects {
			if current, ok := merged[project.ID]; !ok || projectRoleRank(allProjectsRole) > projectRoleRank(current) {
				merged[project.ID] = allProjectsRole
			}
		}
		desired = merged
	}

	return s.syncManagedMemberships(userID, desired, managedMembershipSelector{
		list:   s.membershipRepository.ListCentralManagedMemberships,
		update: s.membershipRepository.UpdateCentralManagedRole,
		create: func(userID, projectID uuid.UUID, role users_enums.ProjectRole) *projects_models.ProjectMembership {
			return &projects_models.ProjectMembership{
				UserID: userID, ProjectID: projectID, Role: role, CentralManaged: true,
			}
		},
		logPrefix: "Control-plane sync",
	})
}
