package control_plane_repositories

import (
	"errors"
	"time"

	control_plane_models "obzly/internal/features/control_plane/models"
	"obzly/internal/storage"

	"gorm.io/gorm"
)

type SigningKeyRepository struct{}

func (r *SigningKeyRepository) Create(key *control_plane_models.SigningKey) error {
	return storage.GetDb().Create(key).Error
}

// GetActive returns the newest key with no RetiredAt, or nil, nil if none exists.
func (r *SigningKeyRepository) GetActive() (*control_plane_models.SigningKey, error) {
	var key control_plane_models.SigningKey
	err := storage.GetDb().
		Where("retired_at IS NULL").
		Order("created_at DESC").
		First(&key).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &key, nil
}

// ListPublished is every active key and every key retired after cutoff.
func (r *SigningKeyRepository) ListPublished(cutoff time.Time) ([]*control_plane_models.SigningKey, error) {
	var keys []*control_plane_models.SigningKey
	err := storage.GetDb().Where("retired_at IS NULL OR retired_at >= ?", cutoff).
		Order("created_at DESC").Find(&keys).Error
	return keys, err
}

// RetireAllActiveExcept retires every active key other than keepKid, starting the
// JWKS grace window for the old ones.
func (r *SigningKeyRepository) RetireAllActiveExcept(keepKid string) error {
	return storage.GetDb().Model(&control_plane_models.SigningKey{}).
		Where("retired_at IS NULL AND kid <> ?", keepKid).
		Update("retired_at", gorm.Expr("NOW()")).Error
}
