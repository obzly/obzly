package users_services

import (
	"errors"
	"fmt"
	"strings"
	"time"

	user_enums "obzly/internal/features/users/enums"
	user_interfaces "obzly/internal/features/users/interfaces"
	mfa_services "obzly/internal/features/users/mfa/services"
	user_models "obzly/internal/features/users/models"
	user_repositories "obzly/internal/features/users/repositories"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

var (
	ErrDeleteUsersForbidden = errors.New("insufficient permissions to delete users")
	ErrUserNotFound         = errors.New("user not found")
	ErrUserOwnsProjects     = errors.New("user owns projects; transfer their ownership first")
)

type UserManagementService struct {
	userRepository  *user_repositories.UserRepository
	userPlanService *UserPlanService
	auditLogWriter  user_interfaces.AuditLogWriter
}

func (s *UserManagementService) SetAuditLogWriter(writer user_interfaces.AuditLogWriter) {
	s.auditLogWriter = writer
}

func (s *UserManagementService) GetUsers(
	currentUser *user_models.User,
	limit, offset int,
	beforeCreatedAt *time.Time,
	query string,
) ([]*user_models.User, int64, error) {
	if !currentUser.CanManageUsers() {
		return nil, 0, errors.New("insufficient permissions to list users")
	}

	return s.userRepository.GetUsers(limit, offset, beforeCreatedAt, query)
}

func (s *UserManagementService) GetUserProfile(
	userID uuid.UUID,
	requestedBy *user_models.User,
) (*user_models.User, error) {
	if userID != requestedBy.ID && !requestedBy.CanManageUsers() {
		return nil, errors.New("insufficient permissions to view user profile")
	}

	return s.userRepository.GetUserByID(userID)
}

func (s *UserManagementService) DeactivateUser(userID uuid.UUID, deactivatedBy *user_models.User) error {
	if !deactivatedBy.CanManageUsers() {
		return errors.New("insufficient permissions to deactivate users")
	}

	if userID == deactivatedBy.ID {
		return errors.New("cannot deactivate your own account")
	}

	user, err := s.userRepository.GetUserByID(userID)
	if err != nil {
		return fmt.Errorf("failed to get user: %w", err)
	}

	if user.Role == user_enums.UserRoleAdmin && !deactivatedBy.IsRootAdmin {
		return errors.New("only the root admin user can deactivate admin accounts")
	}

	if err := s.userRepository.UpdateUserStatus(userID, user_enums.UserStatusInactive); err != nil {
		return fmt.Errorf("failed to deactivate user: %w", err)
	}

	if s.auditLogWriter != nil {
		s.auditLogWriter.WriteAuditLog(
			fmt.Sprintf("User deactivated: %s", user.Email),
			&deactivatedBy.ID,
			nil,
		)
	}

	notifySessionRevocation(userID)

	return nil
}

// SetStatusByProvisioning is SCIM's (de)activation of an account its organization
// created; deactivating revokes sessions.
func (s *UserManagementService) SetStatusByProvisioning(user *user_models.User, active bool, by string) error {
	status, verb := user_enums.UserStatusInactive, "deactivated"
	if active {
		status, verb = user_enums.UserStatusActive, "activated"
	}
	if err := s.userRepository.UpdateUserStatus(user.ID, status); err != nil {
		return fmt.Errorf("failed to update user status: %w", err)
	}
	if s.auditLogWriter != nil {
		s.auditLogWriter.WriteAuditLog(fmt.Sprintf("User %s by %s: %s", verb, by, user.Email), nil, nil)
	}
	if !active {
		notifySessionRevocation(user.ID)
	}
	return nil
}

// DeleteUser deletes a user for good. Never oneself or the root admin, an admin
// only by the root admin, and not while the user owns a project.
func (s *UserManagementService) DeleteUser(userID uuid.UUID, deletedBy *user_models.User) error {
	if !deletedBy.CanManageUsers() {
		return ErrDeleteUsersForbidden
	}
	if userID == deletedBy.ID {
		return errors.New("cannot delete your own account")
	}

	user, err := s.userRepository.GetUserByID(userID)
	if err != nil {
		return ErrUserNotFound
	}
	if user.IsRootAdmin {
		return errors.New("the root admin account cannot be deleted")
	}
	if user.Role == user_enums.UserRoleAdmin && !deletedBy.IsRootAdmin {
		return errors.New("only the root admin user can delete admin accounts")
	}

	owned, err := s.userRepository.OwnedProjectNames(userID)
	if err != nil {
		return fmt.Errorf("failed to check project ownership: %w", err)
	}
	if len(owned) > 0 {
		return fmt.Errorf("%w: %s", ErrUserOwnsProjects, strings.Join(owned, ", "))
	}

	if err := s.userRepository.DeleteUser(userID, deletedBy.ID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrUserNotFound
		}
		return err
	}

	if s.auditLogWriter != nil {
		s.auditLogWriter.WriteAuditLog(
			fmt.Sprintf("User deleted: %s", user.Email),
			&deletedBy.ID,
			nil,
		)
	}

	notifySessionRevocation(userID)

	return nil
}

func (s *UserManagementService) ActivateUser(userID uuid.UUID, activatedBy *user_models.User) error {
	if !activatedBy.CanManageUsers() {
		return errors.New("insufficient permissions to activate users")
	}

	user, err := s.userRepository.GetUserByID(userID)
	if err != nil {
		return fmt.Errorf("failed to get user: %w", err)
	}

	if user.Role == user_enums.UserRoleAdmin && !activatedBy.IsRootAdmin {
		return errors.New("only the root admin user can activate admin accounts")
	}

	if err := s.userRepository.UpdateUserStatus(userID, user_enums.UserStatusActive); err != nil {
		return fmt.Errorf("failed to activate user: %w", err)
	}

	if s.auditLogWriter != nil {
		s.auditLogWriter.WriteAuditLog(
			fmt.Sprintf("User activated: %s", user.Email),
			&activatedBy.ID,
			nil,
		)
	}

	return nil
}

func (s *UserManagementService) ChangeUserRole(
	userID uuid.UUID,
	newRole user_enums.UserRole,
	changedBy *user_models.User,
) error {
	if !changedBy.CanManageUsers() {
		return errors.New("insufficient permissions to change user roles")
	}

	if !newRole.IsValid() {
		return errors.New("invalid user role")
	}

	if userID == changedBy.ID {
		return errors.New("cannot change your own role")
	}

	user, err := s.userRepository.GetUserByID(userID)
	if err != nil {
		return fmt.Errorf("failed to get user: %w", err)
	}

	if (newRole == user_enums.UserRoleAdmin || user.Role == user_enums.UserRoleAdmin) && !changedBy.IsRootAdmin {
		return errors.New("only the root admin user can promote users to admin or demote admin users")
	}

	if err := s.userRepository.UpdateUserRole(userID, newRole); err != nil {
		return fmt.Errorf("failed to update user role: %w", err)
	}

	if s.auditLogWriter != nil {
		s.auditLogWriter.WriteAuditLog(
			fmt.Sprintf("User role changed: %s from %s to %s", user.Email, user.Role, newRole),
			&changedBy.ID,
			nil,
		)
	}

	return nil
}

func (s *UserManagementService) CountByPlan(planID uuid.UUID, requester *user_models.User) (int64, error) {
	if !requester.CanManageUsers() {
		return 0, errors.New("insufficient permissions to view user counts")
	}

	count, err := s.userRepository.CountUsersByPlan(planID)
	if err != nil {
		return 0, fmt.Errorf("failed to count users for plan: %w", err)
	}

	return count, nil
}

// ResetUserMFA clears a user's MFA after a lost device; Admin-role targets are
// prompted to set it up again.
func (s *UserManagementService) ResetUserMFA(userID uuid.UUID, actingAdmin *user_models.User) error {
	if !actingAdmin.CanManageUsers() {
		return errors.New("insufficient permissions to reset MFA")
	}

	targetUser, err := s.userRepository.GetUserByID(userID)
	if err != nil {
		return fmt.Errorf("failed to get user: %w", err)
	}

	// Without this, any admin could strip an admin's authenticator and enroll their own.
	if targetUser.Role == user_enums.UserRoleAdmin && !actingAdmin.IsRootAdmin {
		return errors.New("only the root admin user can reset an admin account's MFA")
	}

	return mfa_services.GetMFAService().AdminReset(targetUser, actingAdmin)
}

// SetUserPassword lets an admin set a user's password: without SMTP it's how an
// invited user gets in, so it also activates an INVITED user.
func (s *UserManagementService) SetUserPassword(
	userID uuid.UUID,
	newPassword string,
	actingAdmin *user_models.User,
) error {
	if !actingAdmin.CanManageUsers() {
		return errors.New("insufficient permissions to set user passwords")
	}

	targetUser, err := s.userRepository.GetUserByID(userID)
	if err != nil {
		return fmt.Errorf("failed to get user: %w", err)
	}

	if targetUser.Role == user_enums.UserRoleAdmin && !actingAdmin.IsRootAdmin {
		return errors.New("only the root admin user can set an admin account's password")
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("failed to hash new password: %w", err)
	}

	if err := s.userRepository.UpdateUserPassword(userID, string(hashedPassword)); err != nil {
		return fmt.Errorf("failed to update password: %w", err)
	}

	if targetUser.Status == user_enums.UserStatusInvited {
		if err := s.userRepository.UpdateUserStatus(userID, user_enums.UserStatusActive); err != nil {
			return fmt.Errorf("failed to activate user: %w", err)
		}
		if err := s.userRepository.ClearInviteToken(userID); err != nil {
			return fmt.Errorf("failed to clear invite token: %w", err)
		}
	}

	if s.auditLogWriter != nil {
		s.auditLogWriter.WriteAuditLog(
			fmt.Sprintf("Password set for user by admin: %s", targetUser.Email),
			&actingAdmin.ID,
			nil,
		)
	}

	return nil
}
