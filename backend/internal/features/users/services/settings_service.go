package users_services

import (
	"cmp"
	"fmt"

	users_dto "obzly/internal/features/users/dto"
	users_interfaces "obzly/internal/features/users/interfaces"
	users_models "obzly/internal/features/users/models"
	users_repositories "obzly/internal/features/users/repositories"
)

type SettingsService struct {
	userSettingsRepository *users_repositories.UsersSettingsRepository
	auditLogWriter         users_interfaces.AuditLogWriter
}

func (s *SettingsService) SetAuditLogWriter(writer users_interfaces.AuditLogWriter) {
	s.auditLogWriter = writer
}

func (s *SettingsService) GetSettings() (*users_models.UsersSettings, error) {
	settings, err := s.userSettingsRepository.GetSettings()
	if err != nil {
		return nil, err
	}
	settings.IsMFARequiredForEveryone = true
	return settings, nil
}

func (s *SettingsService) UpdateSettings(
	request users_dto.UpdateUsersSettingsRequestDTO,
	updatedBy *users_models.User,
) (*users_models.UsersSettings, error) {
	if !updatedBy.CanUpdateSettings() {
		return nil, fmt.Errorf("insufficient permissions to update settings")
	}

	existingSettings, err := s.userSettingsRepository.GetSettings()
	if err != nil {
		return nil, fmt.Errorf("failed to get current settings: %w", err)
	}

	themeMode := cmp.Or(request.DefaultThemeMode, existingSettings.DefaultThemeMode, users_models.DefaultThemeMode)
	lightTheme := cmp.Or(request.DefaultLightTheme, existingSettings.DefaultLightTheme, users_models.DefaultLightTheme)
	darkTheme := cmp.Or(request.DefaultDarkTheme, existingSettings.DefaultDarkTheme, users_models.DefaultDarkTheme)
	if err := users_models.ValidateThemeMode(themeMode); err != nil {
		return nil, err
	}
	if err := users_models.ValidateThemePair(lightTheme, darkTheme); err != nil {
		return nil, err
	}

	auditLogMessages := []string{}

	change := func(name string, current *bool, next bool) {
		if *current != next {
			auditLogMessages = append(auditLogMessages, fmt.Sprintf("%s: %t -> %t", name, *current, next))
			*current = next
		}
	}
	change("isAllowExternalRegistrations", &existingSettings.IsAllowExternalRegistrations,
		request.IsAllowExternalRegistrations)
	change("isAllowMemberInvitations", &existingSettings.IsAllowMemberInvitations, request.IsAllowMemberInvitations)
	change("isMemberAllowedToCreateProjects", &existingSettings.IsMemberAllowedToCreateProjects,
		request.IsMemberAllowedToCreateProjects)
	if request.IsUserThemeChoiceAllowed != nil {
		change("isUserThemeChoiceAllowed", &existingSettings.IsUserThemeChoiceAllowed,
			*request.IsUserThemeChoiceAllowed)
	}

	changeText := func(name string, current *string, next string) {
		if *current != next {
			auditLogMessages = append(auditLogMessages, fmt.Sprintf("%s: %s -> %s", name, *current, next))
			*current = next
		}
	}
	changeText("defaultThemeMode", &existingSettings.DefaultThemeMode, themeMode)
	changeText("defaultLightTheme", &existingSettings.DefaultLightTheme, lightTheme)
	changeText("defaultDarkTheme", &existingSettings.DefaultDarkTheme, darkTheme)

	if err := s.userSettingsRepository.UpdateSettings(existingSettings); err != nil {
		return nil, fmt.Errorf("failed to update settings: %w", err)
	}
	existingSettings.IsMFARequiredForEveryone = true

	for _, message := range auditLogMessages {
		s.auditLogWriter.WriteAuditLog(
			message,
			&updatedBy.ID,
			nil,
		)
	}

	return existingSettings, nil
}
