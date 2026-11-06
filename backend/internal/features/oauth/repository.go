package oauth

import (
	"errors"
	"time"

	"obzly/internal/storage"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type OAuthRepository struct{}

func (r *OAuthRepository) CreateClient(client *RegisteredClient) error {
	return storage.GetDb().Create(client).Error
}

func (r *OAuthRepository) GetClient(clientID string) (*RegisteredClient, error) {
	var client RegisteredClient
	err := storage.GetDb().Where("client_id = ?", clientID).First(&client).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &client, err
}

func (r *OAuthRepository) CreateRefreshToken(token *RefreshToken) error {
	return storage.GetDb().Create(token).Error
}

func (r *OAuthRepository) GetRefreshTokenByHash(tokenHash string) (*RefreshToken, error) {
	var token RefreshToken
	err := storage.GetDb().Where("token_hash = ?", tokenHash).First(&token).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &token, err
}

// MarkUsed consumes a refresh token; false if it was already used or revoked.
func (r *OAuthRepository) MarkUsed(id uuid.UUID, now time.Time) (bool, error) {
	result := storage.GetDb().Model(&RefreshToken{}).
		Where("id = ? AND used_at IS NULL AND revoked_at IS NULL", id).
		Update("used_at", now)
	return result.RowsAffected == 1, result.Error
}

func (r *OAuthRepository) RevokeFamily(familyID uuid.UUID, now time.Time) error {
	return storage.GetDb().Model(&RefreshToken{}).
		Where("family_id = ? AND revoked_at IS NULL", familyID).
		Update("revoked_at", now).Error
}

// RevokeUserFamily revokes a family only if it belongs to userID.
func (r *OAuthRepository) RevokeUserFamily(userID, familyID uuid.UUID, now time.Time) (bool, error) {
	result := storage.GetDb().Model(&RefreshToken{}).
		Where("user_id = ? AND family_id = ? AND revoked_at IS NULL", userID, familyID).
		Update("revoked_at", now)
	return result.RowsAffected > 0, result.Error
}

// ListActiveByUser returns the current (unused, live) refresh token of each
// of the user's families, newest first - one row per connected app grant.
func (r *OAuthRepository) ListActiveByUser(userID uuid.UUID, now time.Time) ([]*RefreshToken, error) {
	var tokens []*RefreshToken
	err := storage.GetDb().
		Where("user_id = ? AND used_at IS NULL AND revoked_at IS NULL AND expires_at > ?", userID, now).
		Order("created_at DESC").
		Find(&tokens).Error
	return tokens, err
}

func (r *OAuthRepository) DeleteExpired(userID uuid.UUID, now time.Time) error {
	return storage.GetDb().
		Where("user_id = ? AND family_expires_at < ?", userID, now).
		Delete(&RefreshToken{}).Error
}
