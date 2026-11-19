// Package sso_models holds portal-configured OIDC SSO connectors (see migration
// 20260915080550_add_sso_connectors.sql).
package sso_models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
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

// MFATrustMode says how far a connector's sign-ins may rely on the provider's
// MFA instead of Obzly's TOTP (see migration 20261119180000_add_sso_mfa_trust.sql).
type MFATrustMode string

const (
	// MFATrustOff: Obzly's own MFA applies wherever policy requires it.
	MFATrustOff MFATrustMode = "off"
	// MFATrustAMR: skip local TOTP only when the ID token's amr shows a second factor.
	MFATrustAMR MFATrustMode = "amr"
	// MFATrustAlways: the provider enforces MFA for everyone who can sign in
	// through it, so every sign-in through this connector skips local TOTP.
	MFATrustAlways MFATrustMode = "always"
)

func (m MFATrustMode) IsValid() bool {
	switch m {
	case MFATrustOff, MFATrustAMR, MFATrustAlways:
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
	// MFATrust: whether a sign-in through this connector may skip local TOTP.
	MFATrust MFATrustMode `json:"-" gorm:"column:mfa_trust"`
	// TrustIdPMFA is the pre-v1.2.2 column, written as MFATrust != off (see
	// BeforeSave) for older pods during a rollout or after a rollback; never read.
	TrustIdPMFA bool      `json:"-" gorm:"column:trust_idp_mfa"`
	ButtonLabel string    `json:"-" gorm:"column:button_label"`
	Enabled     bool      `json:"-" gorm:"column:enabled"`
	CreatedAt   time.Time `json:"-" gorm:"column:created_at"`
	UpdatedAt   time.Time `json:"-" gorm:"column:updated_at"`
}

// BeforeSave treats an unset mode as off and keeps the legacy column in step.
func (c *Connector) BeforeSave(*gorm.DB) error {
	if c.MFATrust == "" {
		c.MFATrust = MFATrustOff
	}
	c.TrustIdPMFA = c.MFATrust != MFATrustOff
	return nil
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
