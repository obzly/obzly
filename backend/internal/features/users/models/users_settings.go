package users_models

import "github.com/google/uuid"

type UsersSettings struct {
	ID uuid.UUID `json:"id" gorm:"type:uuid;primary_key;default:gen_random_uuid()"`
	// means that any user can register via sign up form without invitation
	IsAllowExternalRegistrations bool `json:"isAllowExternalRegistrations" gorm:"column:is_allow_external_registrations"`
	// means that any user with role MEMBER can invite other users
	IsAllowMemberInvitations bool `json:"isAllowMemberInvitations" gorm:"column:is_allow_member_invitations"`
	// means that any user with role MEMBER can create their own projects
	IsMemberAllowedToCreateProjects bool `json:"isMemberAllowedToCreateProjects" gorm:"column:is_member_allowed_to_create_projects"`
	// Always true: MFA is required for every account. Kept in responses for API
	// compatibility; the DB column is no longer read or written (a rollback to
	// v1.2.x still selects it).
	IsMFARequiredForEveryone bool `json:"isMfaRequiredForEveryone" gorm:"-"`
	// Theme defaults for users who never picked one (light, dark or system).
	DefaultThemeMode  string `json:"defaultThemeMode"  gorm:"column:default_theme_mode"`
	DefaultLightTheme string `json:"defaultLightTheme" gorm:"column:default_light_theme"`
	DefaultDarkTheme  string `json:"defaultDarkTheme"  gorm:"column:default_dark_theme"`
	// Off: everyone sees the default themes; users still pick light/dark/system mode.
	IsUserThemeChoiceAllowed bool `json:"isUserThemeChoiceAllowed" gorm:"column:is_user_theme_choice_allowed"`
}

func (UsersSettings) TableName() string {
	return "users_settings"
}
