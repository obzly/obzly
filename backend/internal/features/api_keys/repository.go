package api_keys

import (
	"time"

	"obzly/internal/storage"

	"github.com/google/uuid"
)

type ApiKeyRepository struct{}

func (r *ApiKeyRepository) CreateApiKey(apiKey *ApiKey) error {
	if apiKey.ID == uuid.Nil {
		apiKey.ID = uuid.New()
	}

	if apiKey.CreatedAt.IsZero() {
		apiKey.CreatedAt = time.Now().UTC()
	}

	return storage.GetDb().Create(apiKey).Error
}

func (r *ApiKeyRepository) GetApiKeysByProjectID(projectID uuid.UUID) ([]*ApiKey, error) {
	var apiKeys []*ApiKey

	err := storage.GetDb().
		Where("project_id = ?", projectID).
		Order("created_at DESC").
		Find(&apiKeys).Error

	return apiKeys, err
}

func (r *ApiKeyRepository) GetApiKeyByID(apiKeyID uuid.UUID) (*ApiKey, error) {
	var apiKey ApiKey

	err := storage.GetDb().
		Where("id = ?", apiKeyID).
		First(&apiKey).Error

	if err != nil {
		return nil, err
	}

	return &apiKey, nil
}

func (r *ApiKeyRepository) GetApiKeyByTokenHash(tokenHash string) (*ApiKey, error) {
	var apiKey ApiKey

	err := storage.GetDb().
		Where("token_hash = ? AND status = ?", tokenHash, ApiKeyStatusActive).
		First(&apiKey).Error

	if err != nil {
		return nil, err
	}

	return &apiKey, nil
}

func (r *ApiKeyRepository) UpdateApiKey(apiKey *ApiKey) error {
	return storage.GetDb().Save(apiKey).Error
}

func (r *ApiKeyRepository) DeleteApiKey(apiKeyID uuid.UUID) error {
	return storage.GetDb().Delete(&ApiKey{}, apiKeyID).Error
}

func (r *ApiKeyRepository) DeleteApiKeys(apiKeyIDs []uuid.UUID) error {
	if len(apiKeyIDs) == 0 {
		return nil
	}
	return storage.GetDb().Delete(&ApiKey{}, "id IN ?", apiKeyIDs).Error
}

func (r *ApiKeyRepository) GetApiKeysByIDs(apiKeyIDs []uuid.UUID) ([]*ApiKey, error) {
	var apiKeys []*ApiKey

	if len(apiKeyIDs) == 0 {
		return apiKeys, nil
	}

	err := storage.GetDb().Where("id IN ?", apiKeyIDs).Find(&apiKeys).Error

	return apiKeys, err
}

// UpdateLastUsedAt is a targeted column update (not Save) so it never
// clobbers a concurrent Name/Status change made via UpdateApiKey.
func (r *ApiKeyRepository) UpdateLastUsedAt(apiKeyID uuid.UUID, t time.Time) error {
	return storage.GetDb().
		Model(&ApiKey{}).
		Where("id = ?", apiKeyID).
		Update("last_used_at", t).Error
}
