package users_services

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	users_interfaces "obzly/internal/features/users/interfaces"
	users_models "obzly/internal/features/users/models"
	users_repositories "obzly/internal/features/users/repositories"
)

type SettingsService struct {
	userSettingsRepository *users_repositories.UsersSettingsRepository
	auditLogWriter         users_interfaces.AuditLogWriter
}

// Every authenticated request asks whether MFA is required; other replicas
// see a change within this long.
const mfaSettingTTL = 15 * time.Second

var mfaSetting struct {
	sync.Mutex
	value   bool
	readAt  time.Time
	present bool
}

func (s *SettingsService) SetAuditLogWriter(writer users_interfaces.AuditLogWriter) {
	s.auditLogWriter = writer
}

func (s *SettingsService) GetSettings() (*users_models.UsersSettings, error) {
	return s.userSettingsRepository.GetSettings()
}

// MFARequiredForEveryone is the setting, cached for mfaSettingTTL; on a
// read error it keeps the last value it had.
func (s *SettingsService) MFARequiredForEveryone() bool {
	mfaSetting.Lock()
	defer mfaSetting.Unlock()
	if mfaSetting.present && time.Since(mfaSetting.readAt) < mfaSettingTTL {
		return mfaSetting.value
	}
	settings, err := s.userSettingsRepository.GetSettings()
	if err != nil {
		slog.Warn("reading the MFA setting failed; keeping the last value", "error", err)
		return mfaSetting.value
	}
	mfaSetting.value, mfaSetting.readAt, mfaSetting.present = settings.IsMFARequiredForEveryone, time.Now().UTC(), true
	return mfaSetting.value
}

func (s *SettingsService) UpdateSettings(
	request users_models.UsersSettings,
	updatedBy *users_models.User,
) (*users_models.UsersSettings, error) {
	if !updatedBy.CanUpdateSettings() {
		return nil, fmt.Errorf("insufficient permissions to update settings")
	}

	existingSettings, err := s.userSettingsRepository.GetSettings()
	if err != nil {
		return nil, fmt.Errorf("failed to get current settings: %w", err)
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
	change("isMfaRequiredForEveryone", &existingSettings.IsMFARequiredForEveryone, request.IsMFARequiredForEveryone)

	if err := s.userSettingsRepository.UpdateSettings(existingSettings); err != nil {
		return nil, fmt.Errorf("failed to update settings: %w", err)
	}
	mfaSetting.Lock()
	mfaSetting.value, mfaSetting.readAt, mfaSetting.present = existingSettings.IsMFARequiredForEveryone, time.Now().
		UTC(),
		true
	mfaSetting.Unlock()

	for _, message := range auditLogMessages {
		s.auditLogWriter.WriteAuditLog(
			message,
			&updatedBy.ID,
			nil,
		)
	}

	return existingSettings, nil
}
