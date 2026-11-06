package mfa_services

import (
	"errors"
	"fmt"

	users_interfaces "obzly/internal/features/users/interfaces"
	mfa_dto "obzly/internal/features/users/mfa/dto"
	users_models "obzly/internal/features/users/models"
	users_repositories "obzly/internal/features/users/repositories"

	"github.com/google/uuid"
)

var (
	ErrAlreadyEnabled   = errors.New("MFA is already enabled")
	ErrNotSetUp         = errors.New("MFA setup has not been started - call setup first")
	ErrNotEnabled       = errors.New("MFA is not enabled on this account")
	ErrRequiredForAdmin = errors.New("MFA is required for admin accounts and cannot be disabled")
	ErrUserNotFound     = errors.New("user not found")
)

type Service struct {
	userRepository      *users_repositories.UserRepository
	secretKeyRepository *users_repositories.SecretKeyRepository
	auditLogWriter      users_interfaces.AuditLogWriter
}

func (s *Service) SetAuditLogWriter(writer users_interfaces.AuditLogWriter) {
	s.auditLogWriter = writer
}

func (s *Service) GetStatus(user *users_models.User) *mfa_dto.StatusResponseDTO {
	return &mfa_dto.StatusResponseDTO{
		Enabled:  user.MFAEnabled,
		Required: user.IsMFARequired(),
	}
}

// Setup stores a new encrypted, not-yet-enabled TOTP secret, replacing any
// pending one so an abandoned setup leaves nothing usable.
func (s *Service) Setup(user *users_models.User) (*mfa_dto.SetupResponseDTO, error) {
	if user.MFAEnabled {
		return nil, ErrAlreadyEnabled
	}

	key, err := generateSecret(user.Email)
	if err != nil {
		return nil, err
	}

	encrypted, err := s.secretKeyRepository.Encrypt(key.Secret())
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt MFA secret: %w", err)
	}

	if err := s.userRepository.SetPendingMFASecret(user.ID, encrypted); err != nil {
		return nil, fmt.Errorf("failed to store MFA secret: %w", err)
	}

	return &mfa_dto.SetupResponseDTO{
		Secret:     key.Secret(),
		OtpAuthURL: key.URL(),
	}, nil
}

// Enable requires one valid code, proving the user captured the secret before
// MFA is enforced.
func (s *Service) Enable(user *users_models.User, code string) error {
	if user.MFAEnabled {
		return ErrAlreadyEnabled
	}
	if !user.HasMFASecret() {
		return ErrNotSetUp
	}

	step, err := s.verifyCode(user, code)
	if err != nil {
		return err
	}

	if err := s.userRepository.EnableMFA(user.ID, step); err != nil {
		return fmt.Errorf("failed to enable MFA: %w", err)
	}

	if s.auditLogWriter != nil {
		s.auditLogWriter.WriteAuditLog("MFA enabled", &user.ID, nil)
	}

	return nil
}

// Disable requires a valid code and is refused for Admin-role accounts, which
// is what makes MFA required for them.
func (s *Service) Disable(user *users_models.User, code string) error {
	if !user.MFAEnabled {
		return ErrNotEnabled
	}
	if user.IsMFARequired() {
		return ErrRequiredForAdmin
	}

	if _, err := s.verifyCode(user, code); err != nil {
		return err
	}

	if err := s.userRepository.ClearMFA(user.ID); err != nil {
		return fmt.Errorf("failed to disable MFA: %w", err)
	}

	if s.auditLogWriter != nil {
		s.auditLogWriter.WriteAuditLog("MFA disabled", &user.ID, nil)
	}

	return nil
}

// AdminReset clears targetUser's MFA (lost device). Callers authorize
// actingAdmin; this only resets and audits.
func (s *Service) AdminReset(targetUser *users_models.User, actingAdmin *users_models.User) error {
	if !targetUser.MFAEnabled {
		return ErrNotEnabled
	}

	if err := s.userRepository.ClearMFA(targetUser.ID); err != nil {
		return fmt.Errorf("failed to reset MFA: %w", err)
	}

	if s.auditLogWriter != nil {
		s.auditLogWriter.WriteAuditLog(
			fmt.Sprintf("MFA reset for %s by admin", targetUser.Email),
			&actingAdmin.ID,
			nil,
		)
	}

	return nil
}

// verifyCode checks code with anti-replay and persists the consumed step. Every
// TOTP check goes through it, so it also limits attempts: a 6-digit code is
// brute-forceable within the challenge window otherwise.
func (s *Service) verifyCode(user *users_models.User, code string) (int64, error) {
	if !user.HasMFASecret() {
		return 0, ErrNotSetUp
	}

	locked, err := isLockedOut(user.ID)
	if err != nil {
		return 0, fmt.Errorf("failed to check MFA attempt lockout: %w", err)
	}
	if locked {
		return 0, ErrTooManyAttempts
	}

	secret, err := s.secretKeyRepository.Decrypt(*user.MFASecretEncrypted)
	if err != nil {
		return 0, fmt.Errorf("failed to decrypt MFA secret: %w", err)
	}

	step, verifyErr := verifyAndFindStep(secret, code, user.MFALastUsedStep)
	if verifyErr != nil {
		if lockedOut, recErr := recordFailedAttempt(user.ID); recErr == nil && lockedOut {
			if s.auditLogWriter != nil {
				s.auditLogWriter.WriteAuditLog(
					fmt.Sprintf("MFA locked for %s after too many failed codes", user.Email), &user.ID, nil,
				)
			}
			return 0, ErrTooManyAttempts
		}
		return 0, verifyErr
	}

	consumed, err := s.userRepository.UpdateMFALastUsedStep(user.ID, step)
	if err != nil {
		return 0, fmt.Errorf("failed to record consumed MFA code: %w", err)
	}
	if !consumed {
		return 0, errCodeReused
	}

	// Success resets the counter so earlier typos don't count later.
	_ = resetAttempts(user.ID)

	return step, nil
}

// VerifyCurrentUserCode is step-up verification for a sensitive action. Skip
// it for users without MFA; there's nothing to verify against.
func (s *Service) VerifyCurrentUserCode(user *users_models.User, code string) error {
	if !user.MFAEnabled {
		return ErrNotEnabled
	}
	_, err := s.verifyCode(user, code)
	return err
}

// Login-time flow: the user has only the challenge token, not a session.

func (s *Service) IssueLoginChallengeToken(userID uuid.UUID) (string, error) {
	return issueChallengeToken(s.secretKeyRepository, userID, purposeLogin)
}

func (s *Service) IssueSetupChallengeToken(userID uuid.UUID) (string, error) {
	return issueChallengeToken(s.secretKeyRepository, userID, purposeSetup)
}

func (s *Service) SetupWithToken(mfaToken string) (*mfa_dto.SetupResponseDTO, error) {
	user, err := s.userFromToken(mfaToken, purposeSetup)
	if err != nil {
		return nil, err
	}
	return s.Setup(user)
}

// EnableWithToken completes forced first-time setup and returns the user so the
// caller can issue an access token.
func (s *Service) EnableWithToken(mfaToken, code string) (*users_models.User, error) {
	user, err := s.userFromToken(mfaToken, purposeSetup)
	if err != nil {
		return nil, err
	}
	if err := s.Enable(user, code); err != nil {
		return nil, err
	}
	user.MFAEnabled = true
	return user, nil
}

// VerifyLoginToken completes an MFA login and returns the verified user.
func (s *Service) VerifyLoginToken(mfaToken, code string) (*users_models.User, error) {
	user, err := s.userFromToken(mfaToken, purposeLogin)
	if err != nil {
		return nil, err
	}
	if !user.MFAEnabled {
		return nil, ErrNotEnabled
	}
	if _, err := s.verifyCode(user, code); err != nil {
		return nil, err
	}
	return user, nil
}

func (s *Service) userFromToken(mfaToken string, purpose challengePurpose) (*users_models.User, error) {
	userID, err := parseChallengeToken(s.secretKeyRepository, mfaToken, purpose)
	if err != nil {
		return nil, err
	}

	user, err := s.userRepository.GetUserByID(userID)
	if err != nil {
		return nil, ErrUserNotFound
	}
	if !user.IsActiveUser() {
		return nil, errors.New("user account is deactivated")
	}

	return user, nil
}
