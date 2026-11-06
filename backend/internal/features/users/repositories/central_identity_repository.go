package users_repositories

import (
	"errors"

	users_models "obzly/internal/features/users/models"
	"obzly/internal/storage"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CentralIdentity links a Site user to the control plane's (issuer, subject)
// after a token exchange (ARCHITECTURE.md "Multi-site: control plane and Sites").
type CentralIdentity struct {
	ID      uuid.UUID `gorm:"column:id"`
	UserID  uuid.UUID `gorm:"column:user_id"`
	Issuer  string    `gorm:"column:issuer"`
	Subject string    `gorm:"column:subject"`
}

func (CentralIdentity) TableName() string {
	return "central_identities"
}

type CentralIdentityRepository struct{}

// GetUserByIdentity returns the user linked to issuer+subject, if any.
func (r *CentralIdentityRepository) GetUserByIdentity(issuer, subject string) (*users_models.User, error) {
	var identity CentralIdentity
	err := storage.GetDb().
		Where("issuer = ? AND subject = ?", issuer, subject).
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

// LinkIdentity records that userID is subject at issuer; repeats are no-ops.
func (r *CentralIdentityRepository) LinkIdentity(userID uuid.UUID, issuer, subject string) error {
	identity := &CentralIdentity{
		ID:      uuid.New(),
		UserID:  userID,
		Issuer:  issuer,
		Subject: subject,
	}
	return storage.GetDb().
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(identity).Error
}
