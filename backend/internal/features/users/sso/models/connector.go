// Package sso_models holds portal-configured OIDC SSO connectors (see migration
// 20260915080550_add_sso_connectors.sql).
package sso_models

import (
	"time"

	"github.com/google/uuid"
)

// ProviderType is cosmetic: every type uses the same OIDC flow; it only picks
// the frontend's icon and label.
type ProviderType string

const (
	ProviderTypeCognito         ProviderType = "cognito"
	ProviderTypeGoogleWorkspace ProviderType = "google_workspace"
	ProviderTypeEntraID         ProviderType = "entra_id"
	ProviderTypeGenericOIDC     ProviderType = "generic_oidc"
)

func (p ProviderType) IsValid() bool {
	switch p {
	case ProviderTypeCognito, ProviderTypeGoogleWorkspace, ProviderTypeEntraID, ProviderTypeGenericOIDC:
		return true
	default:
		return false
	}
}

// Connector is one admin-configured OIDC provider. ClientSecretEncrypted is never
// serialized; see ConnectorResponseDTO for the API shape.
type Connector struct {
	ID                    uuid.UUID    `json:"-" gorm:"column:id"`
	Name                  string       `json:"-" gorm:"column:name"`
	ProviderType          ProviderType `json:"-" gorm:"column:provider_type"`
	IssuerURL             string       `json:"-" gorm:"column:issuer_url"`
	ClientID              string       `json:"-" gorm:"column:client_id"`
	ClientSecretEncrypted string       `json:"-" gorm:"column:client_secret_encrypted"`
	Scopes                string       `json:"-" gorm:"column:scopes"`
	GroupsClaim           string       `json:"-" gorm:"column:groups_claim"`
	RequireVerifiedEmail  bool         `json:"-" gorm:"column:require_verified_email"`
	// TrustIdPMFA: sign-ins whose ID token amr shows MFA skip local TOTP.
	TrustIdPMFA bool      `json:"-" gorm:"column:trust_idp_mfa"`
	ButtonLabel string    `json:"-" gorm:"column:button_label"`
	Enabled     bool      `json:"-" gorm:"column:enabled"`
	CreatedAt   time.Time `json:"-" gorm:"column:created_at"`
	UpdatedAt   time.Time `json:"-" gorm:"column:updated_at"`
}

func (Connector) TableName() string {
	return "oauth_connectors"
}

// UserSSOIdentity links a user to their subject at one connector (any number of
// connectors, unlike the fixed GitHub/Google columns).
type UserSSOIdentity struct {
	ID          uuid.UUID `gorm:"column:id"`
	UserID      uuid.UUID `gorm:"column:user_id"`
	ConnectorID uuid.UUID `gorm:"column:connector_id"`
	Subject     string    `gorm:"column:subject"`
	CreatedAt   time.Time `gorm:"column:created_at"`
}

func (UserSSOIdentity) TableName() string {
	return "user_sso_identities"
}

// GroupMapping grants members of one IdP group a role in one project. See
// the migration 20260921020000_add_sso_group_mappings.sql for the sync rules.
type GroupMapping struct {
	ID          uuid.UUID `gorm:"column:id"`
	ConnectorID uuid.UUID `gorm:"column:connector_id"`
	GroupName   string    `gorm:"column:group_name"`
	// ProjectID nil = the mapping grants the global ADMIN role (Role "ADMIN").
	ProjectID *uuid.UUID `gorm:"column:project_id"`
	Role      string     `gorm:"column:role"`
	CreatedAt time.Time  `gorm:"column:created_at"`
}

func (GroupMapping) TableName() string {
	return "sso_group_mappings"
}
