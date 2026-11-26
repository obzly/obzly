package access_tokens

import (
	"time"

	"github.com/google/uuid"
)

type CreateAccessTokenRequestDTO struct {
	Name          string  `json:"name"          binding:"required,min=1,max=100"`
	ExpiresInDays int     `json:"expiresInDays" binding:"required,oneof=30 90 365"`
	MFACode       *string `json:"mfaCode"`
	// Write adds the mcp:write scope (MCP write tools). Needs a session that passed MFA.
	Write bool `json:"write"`
}

type AccessTokenDTO struct {
	ID          uuid.UUID  `json:"id"`
	Name        string     `json:"name"`
	TokenPrefix string     `json:"tokenPrefix"`
	Scopes      []string   `json:"scopes"`
	ExpiresAt   time.Time  `json:"expiresAt"`
	LastUsedAt  *time.Time `json:"lastUsedAt"`
	CreatedAt   time.Time  `json:"createdAt"`
}

type CreateAccessTokenResponseDTO struct {
	// Token is the full value, returned only here.
	Token       string         `json:"token"`
	AccessToken AccessTokenDTO `json:"accessToken"`
}

type ListAccessTokensResponseDTO struct {
	AccessTokens []AccessTokenDTO `json:"accessTokens"`
	// WriteAvailable: new tokens can ask for write access (MCP_WRITE_ENABLED).
	WriteAvailable bool `json:"writeAvailable"`
}

func toDTO(t *AccessToken) AccessTokenDTO {
	return AccessTokenDTO{
		ID:          t.ID,
		Name:        t.Name,
		TokenPrefix: t.TokenPrefix,
		Scopes:      splitScopes(t.Scopes),
		ExpiresAt:   t.ExpiresAt,
		LastUsedAt:  t.LastUsedAt,
		CreatedAt:   t.CreatedAt,
	}
}
