package api_keys

import (
	"github.com/google/uuid"
)

type CreateApiKeyRequestDTO struct {
	Name string `json:"name" binding:"required,min=1,max=100"`
}

type VerifyApiKeyRequestDTO struct {
	Token string `json:"token" binding:"required"`
}

type VerifyApiKeyResponseDTO struct {
	IsValid  bool      `json:"isValid"`
	ApiKeyID uuid.UUID `json:"apiKeyId,omitempty"`
}

type GetApiKeysResponseDTO struct {
	ApiKeys []*ApiKey `json:"apiKeys"`
}

type UpdateApiKeyRequestDTO struct {
	Name   *string       `json:"name,omitempty"   binding:"omitempty,min=1,max=100"`
	Status *ApiKeyStatus `json:"status,omitempty"`
}

// DeleteApiKeyRequestDTO is the optional DELETE body; MFACode is required only
// when the user has MFA enabled.
type DeleteApiKeyRequestDTO struct {
	MFACode *string `json:"mfaCode,omitempty"`
}

type BulkDeleteApiKeysRequestDTO struct {
	ApiKeyIDs []uuid.UUID `json:"apiKeyIds"         binding:"required,min=1"`
	MFACode   *string     `json:"mfaCode,omitempty"`
}

type BulkDeleteApiKeysResponseDTO struct {
	DeletedCount int `json:"deletedCount"`
}

type ValidateTokenRequest struct {
	Token     string    `json:"token"`
	ProjectID uuid.UUID `json:"projectId"`
}

type ValidateTokenResponse struct {
	IsValid   bool      `json:"isValid"`
	ApiKeyID  uuid.UUID `json:"apiKeyId,omitempty"`
	ProjectID uuid.UUID `json:"projectId,omitempty"`
}

type CachedApiKey struct {
	ID        uuid.UUID    `json:"id"`
	ProjectID uuid.UUID    `json:"projectId"`
	Status    ApiKeyStatus `json:"status"`
}
