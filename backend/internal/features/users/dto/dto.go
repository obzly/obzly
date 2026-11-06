package users_dto

import (
	"time"

	users_enums "obzly/internal/features/users/enums"

	"github.com/google/uuid"
)

type SignUpRequestDTO struct {
	Email    string `json:"email"    binding:"required"`
	Password string `json:"password" binding:"required,min=8"`
	Name     string `json:"name"     binding:"required"`
	// InviteToken is required only when completing an invited account.
	InviteToken string `json:"inviteToken"`
}

type SignInRequestDTO struct {
	Email    string `json:"email"    binding:"required"`
	Password string `json:"password" binding:"required"`
}

// SignInResponseDTO carries Token on a normal sign-in. When MFA applies, Token is
// empty and MFAToken is the challenge for /users/mfa/login-verify or login-setup.
type SignInResponseDTO struct {
	UserID uuid.UUID `json:"userId,omitempty"`
	Email  string    `json:"email,omitempty"`
	Token  string    `json:"token,omitempty"`

	MFARequired      bool   `json:"mfaRequired,omitempty"`
	MFASetupRequired bool   `json:"mfaSetupRequired,omitempty"`
	MFAToken         string `json:"mfaToken,omitempty"`
}

type SetAdminPasswordRequestDTO struct {
	Password string `json:"password" binding:"required,min=8"`
	// BootstrapToken is the token logged when the root admin was created.
	BootstrapToken string `json:"bootstrapToken"`
}

type IsAdminHasPasswordResponseDTO struct {
	HasPassword bool `json:"hasPassword"`
	// IsRegistrationEnabled is public so the sign-in page knows whether to offer sign-up.
	IsRegistrationEnabled bool `json:"isRegistrationEnabled"`
	// RootAdminLogin is set only until the root admin has a password, so the
	// break-glass login isn't advertised afterwards.
	RootAdminLogin string `json:"rootAdminLogin,omitempty"`
}

type ChangePasswordRequestDTO struct {
	// CurrentPassword proves the caller knows the password, not just holds a token.
	CurrentPassword string `json:"currentPassword" binding:"required"`
	NewPassword     string `json:"newPassword"     binding:"required,min=8"`
	// MFACode is required only when the caller has MFA enabled.
	MFACode *string `json:"mfaCode,omitempty"`
}

type UpdateUserInfoRequestDTO struct {
	Name *string `json:"name"`
	// CurrentPassword is required only when Email changes; the service enforces it.
	CurrentPassword string  `json:"currentPassword"`
	Email           *string `json:"email"           binding:"omitempty,email"`
	// MFACode is required with an email change when the caller has MFA
	// enabled, as for a password change.
	MFACode *string `json:"mfaCode,omitempty"`
}

type InviteUserRequestDTO struct {
	Email               string                   `json:"email"               binding:"required,email"`
	IntendedProjectID   *uuid.UUID               `json:"intendedProjectId"`
	IntendedProjectRole *users_enums.ProjectRole `json:"intendedProjectRole"`
}

type InviteUserResponseDTO struct {
	ID                  uuid.UUID                `json:"id"`
	Email               string                   `json:"email"`
	IntendedProjectID   *uuid.UUID               `json:"intendedProjectId"`
	IntendedProjectRole *users_enums.ProjectRole `json:"intendedProjectRole"`
	CreatedAt           time.Time                `json:"createdAt"`
	// InviteToken is the raw invite token, shown once. Without email delivery,
	// whoever invited the user relays it.
	InviteToken string `json:"inviteToken"`
	EmailSent   bool   `json:"emailSent"`
}

type UserProfileResponseDTO struct {
	ID        uuid.UUID            `json:"id"`
	Email     string               `json:"email"`
	Name      string               `json:"name"`
	Role      users_enums.UserRole `json:"role"`
	IsActive  bool                 `json:"isActive"`
	CreatedAt time.Time            `json:"createdAt"`
	// Null when the user never signed in / was never active.
	LastLoginAt  *time.Time `json:"lastLoginAt"`
	LastActiveAt *time.Time `json:"lastActiveAt"`
	// MFAEnabled: an authenticator is set up (Settings → Identity & access).
	MFAEnabled bool `json:"mfaEnabled"`
	// IsRootAdmin marks the bootstrap root admin, whose email can only change via ADMIN_EMAIL.
	IsRootAdmin bool `json:"isRootAdmin,omitempty"`
}

type ListUsersResponseDTO struct {
	Users []UserProfileResponseDTO `json:"users"`
	Total int64                    `json:"total"`
}

type ChangeUserRoleRequestDTO struct {
	Role users_enums.UserRole `json:"role" binding:"required"`
}

type SetUserPasswordRequestDTO struct {
	Password string `json:"password" binding:"required,min=8"`
}

type ListUsersRequestDTO struct {
	Limit      int        `form:"limit"      json:"limit"`
	Offset     int        `form:"offset"     json:"offset"`
	BeforeDate *time.Time `form:"beforeDate" json:"beforeDate"`
	Query      string     `form:"query"      json:"query"`
}

type CreatePlanRequestDTO struct {
	Name                 string                   `json:"name"                 binding:"required"`
	Type                 users_enums.UserPlanType `json:"type"                 binding:"required"`
	IsPublic             bool                     `json:"isPublic"`
	WarningText          string                   `json:"warningText"`
	UpgradeText          string                   `json:"upgradeText"`
	LogsPerSecondLimit   int                      `json:"logsPerSecondLimit"   binding:"gte=0"`
	MaxLogsAmount        int64                    `json:"maxLogsAmount"        binding:"gte=0"`
	MaxLogsSizeMB        int                      `json:"maxLogsSizeMb"        binding:"gte=0"`
	MaxLogsLifeDays      int                      `json:"maxLogsLifeDays"      binding:"gte=0"`
	MaxLogSizeKB         int                      `json:"maxLogSizeKb"         binding:"gte=0"`
	AllowedProjectsCount int                      `json:"allowedProjectsCount" binding:"gte=0"`
}

type UpdatePlanRequestDTO struct {
	Name                 *string                   `json:"name"`
	Type                 *users_enums.UserPlanType `json:"type"`
	IsPublic             *bool                     `json:"isPublic"`
	WarningText          *string                   `json:"warningText"`
	UpgradeText          *string                   `json:"upgradeText"`
	LogsPerSecondLimit   *int                      `json:"logsPerSecondLimit"`
	MaxLogsAmount        *int64                    `json:"maxLogsAmount"`
	MaxLogsSizeMB        *int                      `json:"maxLogsSizeMb"`
	MaxLogsLifeDays      *int                      `json:"maxLogsLifeDays"`
	MaxLogSizeKB         *int                      `json:"maxLogSizeKb"`
	AllowedProjectsCount *int                      `json:"allowedProjectsCount"`
}

type CountByPlanResponseDTO struct {
	Count int64 `json:"count"`
}

type OAuthCallbackRequestDTO struct {
	Code        string `json:"code"        binding:"required"`
	RedirectUri string `json:"redirectUri" binding:"required"`
	// State is the signed CSRF token from GenerateOAuthState, checked against this
	// callback's provider.
	State string `json:"state" binding:"required"`
}

// GenerateOAuthStateResponseDTO carries the state for the authorize URL.
type GenerateOAuthStateResponseDTO struct {
	State string `json:"state"`
}

// OAuthCallbackResponseDTO follows SignInResponseDTO's MFA convention.
type OAuthCallbackResponseDTO struct {
	UserID    uuid.UUID `json:"userId,omitempty"`
	Email     string    `json:"email,omitempty"`
	Token     string    `json:"token,omitempty"`
	IsNewUser bool      `json:"isNewUser,omitempty"`

	MFARequired      bool   `json:"mfaRequired,omitempty"`
	MFASetupRequired bool   `json:"mfaSetupRequired,omitempty"`
	MFAToken         string `json:"mfaToken,omitempty"`
}

// SiteExchangeRequestDTO is the body of POST /api/v1/auth/site/exchange.
type SiteExchangeRequestDTO struct {
	Assertion string `json:"assertion" binding:"required"`
}
