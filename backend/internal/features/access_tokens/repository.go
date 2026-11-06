package access_tokens

import (
	"errors"
	"time"

	"obzly/internal/storage"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type AccessTokenRepository struct{}

func (r *AccessTokenRepository) Create(token *AccessToken) error {
	return storage.GetDb().Create(token).Error
}

func (r *AccessTokenRepository) GetByHash(tokenHash string) (*AccessToken, error) {
	var token AccessToken
	err := storage.GetDb().Where("token_hash = ?", tokenHash).First(&token).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &token, nil
}

// ListActiveByUser returns the user's unrevoked, unexpired personal tokens, newest first.
func (r *AccessTokenRepository) ListActiveByUser(userID uuid.UUID, now time.Time) ([]*AccessToken, error) {
	var tokens []*AccessToken
	err := storage.GetDb().
		Where("user_id = ? AND kind = ? AND revoked_at IS NULL AND expires_at > ?", userID, KindPersonal, now).
		Order("created_at DESC").
		Find(&tokens).Error
	return tokens, err
}

// Revoke marks the user's token revoked; false if it isn't theirs or is already revoked.
func (r *AccessTokenRepository) Revoke(userID, tokenID uuid.UUID, now time.Time) (bool, error) {
	result := storage.GetDb().Model(&AccessToken{}).
		Where("id = ? AND user_id = ? AND kind = ? AND revoked_at IS NULL", tokenID, userID, KindPersonal).
		Update("revoked_at", now)
	return result.RowsAffected > 0, result.Error
}

func (r *AccessTokenRepository) RevokeFamily(familyID uuid.UUID, now time.Time) error {
	return storage.GetDb().Model(&AccessToken{}).
		Where("family_id = ? AND revoked_at IS NULL", familyID).
		Update("revoked_at", now).Error
}

// DeleteExpiredOAuth removes the user's OAuth access tokens that expired over a day ago.
func (r *AccessTokenRepository) DeleteExpiredOAuth(userID uuid.UUID, now time.Time) error {
	return storage.GetDb().
		Where("user_id = ? AND kind = ? AND expires_at < ?", userID, KindOAuth, now.Add(-24*time.Hour)).
		Delete(&AccessToken{}).Error
}

func (r *AccessTokenRepository) UpdateLastUsedAt(tokenID uuid.UUID, at time.Time) error {
	return storage.GetDb().Model(&AccessToken{}).Where("id = ?", tokenID).Update("last_used_at", at).Error
}
