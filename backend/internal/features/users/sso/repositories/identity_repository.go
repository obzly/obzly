package sso_repositories

import (
	"errors"

	users_models "obzly/internal/features/users/models"
	sso_models "obzly/internal/features/users/sso/models"
	"obzly/internal/storage"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type IdentityRepository struct{}

// GetUserByIdentity returns the user linked to connector+subject, if any.
func (r *IdentityRepository) GetUserByIdentity(connectorID uuid.UUID, subject string) (*users_models.User, error) {
	var identity sso_models.UserSSOIdentity
	err := storage.GetDb().
		Where("connector_id = ? AND subject = ?", connectorID, subject).
		First(&identity).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var user users_models.User
	if err := storage.GetDb().Preload("Plan").Where("id = ?", identity.UserID).First(&user).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

// HasAnyIdentity reports whether userID is linked to any SSO connector.
func (r *IdentityRepository) HasAnyIdentity(userID uuid.UUID) (bool, error) {
	var count int64
	err := storage.GetDb().Model(&sso_models.UserSSOIdentity{}).Where("user_id = ?", userID).Count(&count).Error
	return count > 0, err
}

// LinkIdentity records that userID is subject at connectorID; repeats are no-ops.
func (r *IdentityRepository) LinkIdentity(userID, connectorID uuid.UUID, subject string) error {
	identity := &sso_models.UserSSOIdentity{
		ID:          uuid.New(),
		UserID:      userID,
		ConnectorID: connectorID,
		Subject:     subject,
	}
	return storage.GetDb().
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(identity).Error
}
