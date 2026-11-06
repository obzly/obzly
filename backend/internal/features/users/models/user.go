package users_models

import (
	users_enums "obzly/internal/features/users/enums"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

type User struct {
	ID                   uuid.UUID              `json:"id"`
	Email                string                 `json:"email"`
	Name                 string                 `json:"name"`
	HashedPassword       *string                `json:"-"                gorm:"column:hashed_password"`
	PasswordCreationTime time.Time              `json:"-"                gorm:"column:password_creation_time"`
	Role                 users_enums.UserRole   `json:"role"`
	PlanID               *uuid.UUID             `json:"planId,omitempty" gorm:"column:plan_id"`
	Plan                 *UserPlan              `json:"plan"`
	Status               users_enums.UserStatus `json:"status"`
	GitHubOAuthID        *string                `json:"-"                gorm:"column:github_oauth_id"`
	GoogleOAuthID        *string                `json:"-"                gorm:"column:google_oauth_id"`
	CreatedAt            time.Time              `json:"createdAt"`
	// LastLoginAt: the last sign-in that issued a session (any method,
	// including a control-plane token exchange). LastActiveAt: the last
	// authenticated request, written at most every activityWriteInterval.
	LastLoginAt  *time.Time `json:"lastLoginAt,omitempty"  gorm:"column:last_login_at"`
	LastActiveAt *time.Time `json:"lastActiveAt,omitempty" gorm:"column:last_active_at"`

	// MFASecretEncrypted is written when setup starts, before MFAEnabled.
	// MFALastUsedStep is the anti-replay guard (last consumed TOTP step).
	MFASecretEncrypted *string `json:"-"          gorm:"column:mfa_secret_encrypted"`
	MFAEnabled         bool    `json:"mfaEnabled" gorm:"column:mfa_enabled"`
	MFALastUsedStep    int64   `json:"-"          gorm:"column:mfa_last_used_step"`

	// IsRootAdmin marks the bootstrap admin by flag, not email, so ADMIN_EMAIL can
	// change its login without breaking root-only checks.
	IsRootAdmin bool `json:"-" gorm:"column:is_root_admin"`

	// SSOAdminConnectorID is set when the ADMIN role was granted by an SSO
	// group mapping; only such admins are demoted by a later sync.
	SSOAdminConnectorID *uuid.UUID `json:"-" gorm:"column:sso_admin_connector_id"`

	// CentralAdmin is true when ADMIN came from a control-plane assertion; only such
	// admins are demoted by a later exchange.
	CentralAdmin bool `json:"-" gorm:"column:central_admin"`

	// InviteTokenHash/InviteTokenExpiresAt gate SignUp for an INVITED user; cleared once used.
	InviteTokenHash      *string    `json:"-" gorm:"column:invite_token_hash"`
	InviteTokenExpiresAt *time.Time `json:"-" gorm:"column:invite_token_expires_at"`

	// BootstrapTokenHash gates the root admin's one-time set-password call; cleared once used.
	BootstrapTokenHash *string `json:"-" gorm:"column:bootstrap_token_hash"`

	// SCIMOrgID is the organization whose SCIM created the account - the
	// only one that may deactivate it.
	SCIMOrgID *uuid.UUID `json:"-" gorm:"column:scim_org_id"`
}

func (User) TableName() string {
	return "users"
}

func (u *User) CanInviteUsers(settings *UsersSettings) bool {
	if u.Role == users_enums.UserRoleAdmin {
		return true
	}

	return u.Role == users_enums.UserRoleMember && settings.IsAllowMemberInvitations
}

func (u *User) CanManageUsers() bool {
	return u.Role == users_enums.UserRoleAdmin
}

func (u *User) CanUpdateSettings() bool {
	return u.Role == users_enums.UserRoleAdmin
}

func (u *User) CanCreateProjects(settings *UsersSettings) bool {
	if u.Role == users_enums.UserRoleAdmin {
		return true
	}
	return u.Role == users_enums.UserRoleMember && settings.IsMemberAllowedToCreateProjects
}

func (u *User) IsActiveUser() bool {
	return u.Status == users_enums.UserStatusActive
}

func (u *User) HasPassword() bool {
	return u.HashedPassword != nil && *u.HashedPassword != ""
}

// mfaForEveryone reads the "require MFA for everyone" setting; the
// settings service sets it (cached), so models don't import services.
var mfaForEveryone atomic.Pointer[func() bool]

func SetMFAForEveryone(source func() bool) {
	mfaForEveryone.Store(&source)
}

// IsMFARequired is true for Admin-role users, and for everyone when the setting
// says so. Derived from the current role, so there's no stored flag to drift.
func (u *User) IsMFARequired() bool {
	if u.Role == users_enums.UserRoleAdmin {
		return true
	}
	source := mfaForEveryone.Load()
	return source != nil && (*source)()
}

func (u *User) HasMFASecret() bool {
	return u.MFASecretEncrypted != nil && *u.MFASecretEncrypted != ""
}
