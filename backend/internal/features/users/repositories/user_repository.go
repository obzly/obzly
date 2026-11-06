package users_repositories

import (
	"errors"
	"fmt"
	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"
	"obzly/internal/storage"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// postgresUniqueViolationCode lets the replica losing the CreateInitialAdmin
// race tell that apart from a real failure.
const postgresUniqueViolationCode = "23505"

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == postgresUniqueViolationCode
}

// normalizeEmail is applied on every write and lookup so case variants can't
// become separate accounts; idx_users_email_lower is the database backstop.
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

type UserRepository struct{}

func (r *UserRepository) CreateUser(user *users_models.User) error {
	user.Email = normalizeEmail(user.Email)
	return storage.GetDb().Create(user).Error
}

func (r *UserRepository) GetUserByEmail(email string) (*users_models.User, error) {
	var user users_models.User

	if err := storage.GetDb().Preload("Plan").Where("email = ?", normalizeEmail(email)).First(&user).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}

		return nil, err
	}

	return &user, nil
}

func (r *UserRepository) GetUserByID(userID uuid.UUID) (*users_models.User, error) {
	var user users_models.User

	if err := storage.GetDb().Preload("Plan").Where("id = ?", userID).First(&user).Error; err != nil {
		return nil, err
	}

	return &user, nil
}

// GetUserByIDWithoutPlan is GetUserByID for the per-request auth check,
// which never reads the plan; callers that do load it themselves.
func (r *UserRepository) GetUserByIDWithoutPlan(userID uuid.UUID) (*users_models.User, error) {
	var user users_models.User

	if err := storage.GetDb().Where("id = ?", userID).First(&user).Error; err != nil {
		return nil, err
	}

	return &user, nil
}

// UpdateUserPassword also revokes the user's personal access and OAuth
// tokens: a new password must lock out whoever held the old one.
func (r *UserRepository) UpdateUserPassword(userID uuid.UUID, hashedPassword string) error {
	return storage.GetDb().Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		if err := tx.Model(&users_models.User{}).
			Where("id = ?", userID).
			Updates(map[string]any{
				"hashed_password":        hashedPassword,
				"password_creation_time": now,
			}).Error; err != nil {
			return err
		}
		return revokeUserTokens(tx, userID, now)
	})
}

// CreateInitialAdmin creates the root admin (found by IsRootAdmin, not email)
// if none exists. idx_users_single_root_admin settles the race between replicas.
// It reports whether this call created it, so only that caller logs the token.
func (r *UserRepository) CreateInitialAdmin(email, bootstrapTokenHash string) (created bool, err error) {
	admin, err := r.GetRootAdmin()
	if err != nil {
		return false, fmt.Errorf("failed to get admin user: %w", err)
	}

	if admin != nil {
		return false, nil
	}

	admin = &users_models.User{
		ID:                   uuid.New(),
		Email:                normalizeEmail(email),
		Name:                 "Admin",
		HashedPassword:       nil,
		PasswordCreationTime: time.Now().UTC(),
		Role:                 users_enums.UserRoleAdmin,
		Status:               users_enums.UserStatusActive,
		IsRootAdmin:          true,
		CreatedAt:            time.Now().UTC(),
		BootstrapTokenHash:   &bootstrapTokenHash,
	}

	if err := storage.GetDb().Create(admin).Error; err != nil {
		if isUniqueViolation(err) {
			return false, nil
		}
		return false, err
	}

	return true, nil
}

// ClearBootstrapToken makes the bootstrap token single-use.
func (r *UserRepository) ClearBootstrapToken(userID uuid.UUID) error {
	return storage.GetDb().Model(&users_models.User{}).
		Where("id = ?", userID).
		Update("bootstrap_token_hash", nil).Error
}

// SetLastLogin also counts as activity.
func (r *UserRepository) SetLastLogin(userID uuid.UUID, at time.Time) error {
	return storage.GetDb().Model(&users_models.User{}).
		Where("id = ?", userID).
		Updates(map[string]any{"last_login_at": at, "last_active_at": at}).Error
}

// TouchLastActive writes only when the stored value is older than
// minInterval, so replicas that each throttle on their own still write a
// user's activity about once per interval.
func (r *UserRepository) TouchLastActive(userID uuid.UUID, at time.Time, minInterval time.Duration) error {
	return storage.GetDb().Model(&users_models.User{}).
		Where("id = ? AND (last_active_at IS NULL OR last_active_at < ?)", userID, at.Add(-minInterval)).
		Update("last_active_at", at).Error
}

func (r *UserRepository) SetBootstrapTokenHash(userID uuid.UUID, tokenHash string) error {
	return storage.GetDb().Model(&users_models.User{}).
		Where("id = ?", userID).
		Update("bootstrap_token_hash", tokenHash).Error
}

func (r *UserRepository) GetUsers(
	limit, offset int,
	beforeCreatedAt *time.Time,
	query string,
) ([]*users_models.User, int64, error) {
	var users []*users_models.User
	var total int64

	countQuery := storage.GetDb().Model(&users_models.User{})
	if beforeCreatedAt != nil {
		countQuery = countQuery.Where("created_at < ?", *beforeCreatedAt)
	}
	if query != "" {
		searchPattern := "%" + query + "%"
		countQuery = countQuery.Where("email ILIKE ? OR name ILIKE ?", searchPattern, searchPattern)
	}

	if err := countQuery.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	dataQuery := storage.GetDb().
		Preload("Plan").
		Limit(limit).
		Offset(offset).
		Order("created_at DESC")

	if beforeCreatedAt != nil {
		dataQuery = dataQuery.Where("created_at < ?", *beforeCreatedAt)
	}
	if query != "" {
		searchPattern := "%" + query + "%"
		dataQuery = dataQuery.Where("email ILIKE ? OR name ILIKE ?", searchPattern, searchPattern)
	}

	if err := dataQuery.Find(&users).Error; err != nil {
		return nil, 0, err
	}

	return users, total, nil
}

func (r *UserRepository) UpdateUserStatus(userID uuid.UUID, status users_enums.UserStatus) error {
	return storage.GetDb().Model(&users_models.User{}).
		Where("id = ?", userID).
		Updates(map[string]any{
			"status": status,
		}).Error
}

// SetInviteToken records an invite token's hash and expiry.
func (r *UserRepository) SetInviteToken(userID uuid.UUID, tokenHash string, expiresAt time.Time) error {
	return storage.GetDb().Model(&users_models.User{}).
		Where("id = ?", userID).
		Updates(map[string]any{
			"invite_token_hash":       tokenHash,
			"invite_token_expires_at": expiresAt,
		}).Error
}

// ClearInviteToken makes an invite single-use once claimed or the account is
// activated another way.
func (r *UserRepository) ClearInviteToken(userID uuid.UUID) error {
	return storage.GetDb().Model(&users_models.User{}).
		Where("id = ?", userID).
		Updates(map[string]any{
			"invite_token_hash":       nil,
			"invite_token_expires_at": nil,
		}).Error
}

func (r *UserRepository) UpdateUserRole(userID uuid.UUID, role users_enums.UserRole) error {
	return storage.GetDb().Model(&users_models.User{}).
		Where("id = ?", userID).
		Updates(map[string]any{
			"role":                   role,
			"sso_admin_connector_id": nil,
		}).Error
}

// GrantSSOAdmin promotes a MEMBER to ADMIN on behalf of an SSO group mapping
// and tags the role as group-granted. Existing admins (manual, root, or from
// another connector) are left as they are. Reports whether it promoted.
func (r *UserRepository) GrantSSOAdmin(userID, connectorID uuid.UUID) (bool, error) {
	result := storage.GetDb().Model(&users_models.User{}).
		Where("id = ? AND role = ?", userID, users_enums.UserRoleMember).
		Updates(map[string]any{
			"role":                   users_enums.UserRoleAdmin,
			"sso_admin_connector_id": connectorID,
		})
	return result.RowsAffected > 0, result.Error
}

// RevokeSSOAdmin demotes an admin whose role this connector's groups granted.
// It never demotes the root admin, and never the last remaining admin (that
// would lock everyone out of settings). Reports whether it demoted.
func (r *UserRepository) RevokeSSOAdmin(userID, connectorID uuid.UUID) (bool, error) {
	result := storage.GetDb().Exec(`
		UPDATE users SET role = ?, sso_admin_connector_id = NULL
		WHERE id = ? AND sso_admin_connector_id = ? AND role = ? AND is_root_admin = FALSE
		  AND (SELECT COUNT(*) FROM users WHERE role = ?) > 1`,
		users_enums.UserRoleMember, userID, connectorID, users_enums.UserRoleAdmin, users_enums.UserRoleAdmin,
	)
	return result.RowsAffected > 0, result.Error
}

// GrantCentralAdmin promotes a MEMBER to ADMIN for a control-plane assertion and
// marks it central, so only RevokeCentralAdmin undoes it. Reports whether it promoted.
func (r *UserRepository) GrantCentralAdmin(userID uuid.UUID) (bool, error) {
	result := storage.GetDb().Model(&users_models.User{}).
		Where("id = ? AND role = ?", userID, users_enums.UserRoleMember).
		Updates(map[string]any{
			"role":          users_enums.UserRoleAdmin,
			"central_admin": true,
		})
	return result.RowsAffected > 0, result.Error
}

// RevokeCentralAdmin is RevokeSSOAdmin's twin: demotes an admin whose role
// a central assertion granted, never the root admin, and never the last
// remaining admin. Reports whether it demoted.
func (r *UserRepository) RevokeCentralAdmin(userID uuid.UUID) (bool, error) {
	result := storage.GetDb().Exec(`
		UPDATE users SET role = ?, central_admin = FALSE
		WHERE id = ? AND central_admin = TRUE AND role = ? AND is_root_admin = FALSE
		  AND (SELECT COUNT(*) FROM users WHERE role = ?) > 1`,
		users_enums.UserRoleMember, userID, users_enums.UserRoleAdmin, users_enums.UserRoleAdmin,
	)
	return result.RowsAffected > 0, result.Error
}

// RetireRootAdminForTests un-flags the current root admin so CreateInitialAdmin
// creates a fresh one. Test-only.
func (r *UserRepository) RetireRootAdminForTests(newEmail string) error {
	return storage.GetDb().Model(&users_models.User{}).
		Where("is_root_admin = ?", true).
		Updates(map[string]any{
			"email":         newEmail,
			"is_root_admin": false,
		}).Error
}

func (r *UserRepository) UpdateUserInfo(userID uuid.UUID, name *string, email *string) error {
	updates := make(map[string]any)

	if name != nil {
		updates["name"] = *name
	}
	if email != nil {
		updates["email"] = normalizeEmail(*email)
	}

	if len(updates) == 0 {
		return nil
	}

	return storage.GetDb().Model(&users_models.User{}).
		Where("id = ?", userID).
		Updates(updates).Error
}

func (r *UserRepository) CountUsersByPlan(planID uuid.UUID) (int64, error) {
	var count int64
	err := storage.GetDb().
		Model(&users_models.User{}).
		Where("plan_id = ?", planID).
		Count(&count).Error

	if err != nil {
		return 0, err
	}

	return count, nil
}

func (r *UserRepository) GetUserByGitHubOAuthID(githubID string) (*users_models.User, error) {
	var user users_models.User
	err := storage.GetDb().Preload("Plan").Where("github_oauth_id = ?", githubID).First(&user).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *UserRepository) GetUserByGoogleOAuthID(googleID string) (*users_models.User, error) {
	var user users_models.User
	err := storage.GetDb().Preload("Plan").Where("google_oauth_id = ?", googleID).First(&user).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *UserRepository) LinkOAuthID(userID uuid.UUID, oauthColumn, oauthID string) error {
	updates := map[string]any{oauthColumn: oauthID}
	return storage.GetDb().Model(&users_models.User{}).
		Where("id = ?", userID).
		Updates(updates).Error
}

// SetPendingMFASecret stores an unconfirmed TOTP secret and resets mfa_enabled
// and the replay counter, so a restarted setup can't inherit a stale step.
func (r *UserRepository) SetPendingMFASecret(userID uuid.UUID, encryptedSecret string) error {
	return storage.GetDb().Model(&users_models.User{}).
		Where("id = ?", userID).
		Updates(map[string]any{
			"mfa_secret_encrypted": encryptedSecret,
			"mfa_enabled":          false,
			"mfa_last_used_step":   0,
		}).Error
}

func (r *UserRepository) EnableMFA(userID uuid.UUID, lastUsedStep int64) error {
	return storage.GetDb().Model(&users_models.User{}).
		Where("id = ?", userID).
		Updates(map[string]any{
			"mfa_enabled":        true,
			"mfa_last_used_step": lastUsedStep,
		}).Error
}

// UpdateMFALastUsedStep records a consumed TOTP step only if it is newer
// than the stored one, so two requests racing with the same code can't
// both succeed. False means another request consumed it first.
func (r *UserRepository) UpdateMFALastUsedStep(userID uuid.UUID, lastUsedStep int64) (bool, error) {
	result := storage.GetDb().Model(&users_models.User{}).
		Where("id = ? AND mfa_last_used_step < ?", userID, lastUsedStep).
		Update("mfa_last_used_step", lastUsedStep)
	return result.RowsAffected > 0, result.Error
}

// ClearMFA removes MFA (self-disable for Members, or an admin reset) and
// revokes tokens created while it was on.
func (r *UserRepository) ClearMFA(userID uuid.UUID) error {
	return storage.GetDb().Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&users_models.User{}).
			Where("id = ?", userID).
			Updates(map[string]any{
				"mfa_secret_encrypted": nil,
				"mfa_enabled":          false,
				"mfa_last_used_step":   0,
			}).Error; err != nil {
			return err
		}
		return revokeUserTokens(tx, userID, time.Now().UTC())
	})
}

// revokeUserTokens revokes every personal access token, OAuth access token
// and OAuth refresh token the user holds. Raw SQL because those features
// import this package, not the other way round.
func revokeUserTokens(tx *gorm.DB, userID uuid.UUID, now time.Time) error {
	if err := tx.Exec(
		"UPDATE access_tokens SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL", now, userID,
	).Error; err != nil {
		return fmt.Errorf("failed to revoke access tokens: %w", err)
	}
	if err := tx.Exec(
		"UPDATE oauth_refresh_tokens SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL", now, userID,
	).Error; err != nil {
		return fmt.Errorf("failed to revoke OAuth refresh tokens: %w", err)
	}
	return nil
}

// GetRootAdmin returns the account flagged is_root_admin.
func (r *UserRepository) GetRootAdmin() (*users_models.User, error) {
	var user users_models.User

	if err := storage.GetDb().Where("is_root_admin = ?", true).First(&user).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}

	return &user, nil
}

// SyncRootAdminEmail sets the root admin's email to newEmail. Returns (false, nil)
// when there's nothing to do and an error if newEmail belongs to another account.
func (r *UserRepository) SyncRootAdminEmail(newEmail string) (bool, error) {
	newEmail = normalizeEmail(newEmail)
	if newEmail == "" {
		return false, nil
	}

	admin, err := r.GetRootAdmin()
	if err != nil {
		return false, fmt.Errorf("failed to get root admin: %w", err)
	}
	if admin == nil {
		return false, nil
	}
	if admin.Email == newEmail {
		return false, nil
	}

	existing, err := r.GetUserByEmail(newEmail)
	if err != nil {
		return false, fmt.Errorf("failed to check for existing user with email %q: %w", newEmail, err)
	}
	if existing != nil {
		return false, fmt.Errorf("cannot rename root admin to %q: that email is already in use", newEmail)
	}

	if err := storage.GetDb().Model(&users_models.User{}).
		Where("id = ?", admin.ID).
		Update("email", newEmail).Error; err != nil {
		return false, fmt.Errorf("failed to update root admin email: %w", err)
	}

	return true, nil
}

// OwnedProjectNames is the projects userID owns; a project needs an owner,
// so the user can't be deleted while it has any.
func (r *UserRepository) OwnedProjectNames(userID uuid.UUID) ([]string, error) {
	var names []string
	err := storage.GetDb().Raw(
		"SELECT p.name FROM project_memberships m JOIN projects p ON p.id = m.project_id "+
			"WHERE m.user_id = ? AND m.role = ? ORDER BY p.name",
		userID, users_enums.ProjectRoleOwner,
	).Scan(&names).Error
	return names, err
}

// DeleteUser deletes userID in one transaction. Memberships, tokens and
// SSO/central identities go with it (ON DELETE CASCADE) and audit log rows
// keep their text without the user; the dashboards it created lose their
// creator, and Site grants it created are attributed to deletedBy.
func (r *UserRepository) DeleteUser(userID, deletedBy uuid.UUID) error {
	return storage.GetDb().Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("UPDATE dashboards SET created_by = NULL WHERE created_by = ?", userID).Error; err != nil {
			return fmt.Errorf("failed to clear dashboard creator: %w", err)
		}
		if err := tx.Exec(
			"UPDATE site_grants SET created_by = ? WHERE created_by = ?",
			deletedBy,
			userID,
		).Error; err != nil {
			return fmt.Errorf("failed to reassign site grants: %w", err)
		}
		result := tx.Exec("DELETE FROM users WHERE id = ?", userID)
		if result.Error != nil {
			return fmt.Errorf("failed to delete user: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}
