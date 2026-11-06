package users_repositories

import (
	user_models "obzly/internal/features/users/models"
	"obzly/internal/storage"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type UsersSettingsRepository struct{}

func (r *UsersSettingsRepository) GetSettings() (*user_models.UsersSettings, error) {
	var settings user_models.UsersSettings

	if err := storage.GetDb().First(&settings).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			defaultSettings := &user_models.UsersSettings{
				ID: uuid.New(),
				// Off by default: a fresh instance shouldn't let anyone on the network sign up.
				// Invitations still work.
				IsAllowExternalRegistrations:    false,
				IsAllowMemberInvitations:        true,
				IsMemberAllowedToCreateProjects: true,
			}

			if createErr := storage.GetDb().Create(defaultSettings).Error; createErr != nil {
				return nil, createErr
			}

			return defaultSettings, nil
		}
		return nil, err
	}

	return &settings, nil
}

func (r *UsersSettingsRepository) UpdateSettings(settings *user_models.UsersSettings) error {
	existingSettings, err := r.GetSettings()
	if err != nil {
		return err
	}

	settings.ID = existingSettings.ID

	return storage.GetDb().Save(settings).Error
}
