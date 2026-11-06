package sso_dto

import (
	"time"

	sso_models "obzly/internal/features/users/sso/models"

	"github.com/google/uuid"
)

// ConnectorResponseDTO never includes the client secret after creation;
// HasClientSecret tells the UI one is configured.
type ConnectorResponseDTO struct {
	ID                   uuid.UUID               `json:"id"`
	Name                 string                  `json:"name"`
	ProviderType         sso_models.ProviderType `json:"providerType"`
	IssuerURL            string                  `json:"issuerUrl"`
	ClientID             string                  `json:"clientId"`
	HasClientSecret      bool                    `json:"hasClientSecret"`
	Scopes               string                  `json:"scopes"`
	GroupsClaim          string                  `json:"groupsClaim"`
	RequireVerifiedEmail bool                    `json:"requireVerifiedEmail"`
	TrustIdPMFA          bool                    `json:"trustIdpMfa"`
	ButtonLabel          string                  `json:"buttonLabel"`
	Enabled              bool                    `json:"enabled"`
	CreatedAt            time.Time               `json:"createdAt"`
	UpdatedAt            time.Time               `json:"updatedAt"`
}

func ToConnectorResponseDTO(c *sso_models.Connector) *ConnectorResponseDTO {
	return &ConnectorResponseDTO{
		ID:                   c.ID,
		Name:                 c.Name,
		ProviderType:         c.ProviderType,
		IssuerURL:            c.IssuerURL,
		ClientID:             c.ClientID,
		HasClientSecret:      c.ClientSecretEncrypted != "",
		Scopes:               c.Scopes,
		GroupsClaim:          c.GroupsClaim,
		RequireVerifiedEmail: c.RequireVerifiedEmail,
		TrustIdPMFA:          c.TrustIdPMFA,
		ButtonLabel:          c.ButtonLabel,
		Enabled:              c.Enabled,
		CreatedAt:            c.CreatedAt,
		UpdatedAt:            c.UpdatedAt,
	}
}

type ListConnectorsResponseDTO struct {
	Connectors []*ConnectorResponseDTO `json:"connectors"`
}

// CreateConnectorRequestDTO requires ClientSecret; on update an empty one keeps
// the existing secret.
type CreateConnectorRequestDTO struct {
	Name         string                  `json:"name"         binding:"required,min=1,max=100"`
	ProviderType sso_models.ProviderType `json:"providerType" binding:"required"`
	IssuerURL    string                  `json:"issuerUrl"    binding:"required,url"`
	ClientID     string                  `json:"clientId"     binding:"required"`
	ClientSecret string                  `json:"clientSecret" binding:"required"`
	Scopes       string                  `json:"scopes"`
	GroupsClaim  string                  `json:"groupsClaim"  binding:"max=100"`
	// RequireVerifiedEmail defaults to true when omitted.
	RequireVerifiedEmail *bool `json:"requireVerifiedEmail"`
	// TrustIdPMFA defaults to false when omitted.
	TrustIdPMFA *bool  `json:"trustIdpMfa"`
	ButtonLabel string `json:"buttonLabel" binding:"required,min=1,max=100"`
	Enabled     bool   `json:"enabled"`
}

type UpdateConnectorRequestDTO struct {
	Name         string                  `json:"name"         binding:"required,min=1,max=100"`
	ProviderType sso_models.ProviderType `json:"providerType" binding:"required"`
	IssuerURL    string                  `json:"issuerUrl"    binding:"required,url"`
	ClientID     string                  `json:"clientId"     binding:"required"`
	ClientSecret string                  `json:"clientSecret"`
	Scopes       string                  `json:"scopes"`
	GroupsClaim  string                  `json:"groupsClaim"  binding:"max=100"`
	// RequireVerifiedEmail keeps the current value when omitted.
	RequireVerifiedEmail *bool `json:"requireVerifiedEmail"`
	// TrustIdPMFA keeps the current value when omitted.
	TrustIdPMFA *bool  `json:"trustIdpMfa"`
	ButtonLabel string `json:"buttonLabel" binding:"required,min=1,max=100"`
	Enabled     bool   `json:"enabled"`
}

// PublicConnectorDTO is what the unauthenticated login page gets; AuthorizeURL
// is complete, so the frontend needs no provider-specific URL building.
type PublicConnectorDTO struct {
	ID           uuid.UUID               `json:"id"`
	Name         string                  `json:"name"`
	ProviderType sso_models.ProviderType `json:"providerType"`
	ButtonLabel  string                  `json:"buttonLabel"`
	AuthorizeURL string                  `json:"authorizeUrl"`
}

type ListPublicConnectorsResponseDTO struct {
	Connectors []*PublicConnectorDTO `json:"connectors"`
}

type SSOCallbackRequestDTO struct {
	State       string `json:"state"       binding:"required"`
	Code        string `json:"code"        binding:"required"`
	RedirectUri string `json:"redirectUri" binding:"required"`
}

type GroupMappingResponseDTO struct {
	ID          uuid.UUID `json:"id"`
	ConnectorID uuid.UUID `json:"connectorId"`
	GroupName   string    `json:"groupName"`
	// ProjectID null = global Admin mapping.
	ProjectID *uuid.UUID `json:"projectId"`
	Role      string     `json:"role"`
	CreatedAt time.Time  `json:"createdAt"`
}

type ListGroupMappingsResponseDTO struct {
	Mappings []*GroupMappingResponseDTO `json:"mappings"`
}

type CreateGroupMappingRequestDTO struct {
	GroupName string `json:"groupName" binding:"required,min=1,max=255"`
	// ProjectID omitted/null with Role "ADMIN" = grant the global Admin role
	// (root admin only).
	ProjectID *uuid.UUID `json:"projectId"`
	Role      string     `json:"role"      binding:"required"`
}
