// Package control_plane_models holds the tables used only by a SERVICE_MODE=control-plane
// process (ARCHITECTURE.md "Multi-site: control plane and Sites").
package control_plane_models

import (
	"time"

	"github.com/google/uuid"
)

// Site is one independently deployed backend a control plane fronts. ApiURL is never
// serialized to non-admins: a Site's real address must not reach the browser.
type Site struct {
	ID        string    `json:"id"        gorm:"column:id;primaryKey"`
	Name      string    `json:"name"      gorm:"column:name"`
	ApiURL    string    `json:"-"         gorm:"column:api_url"`
	Enabled   bool      `json:"enabled"   gorm:"column:enabled"`
	CreatedAt time.Time `json:"createdAt" gorm:"column:created_at"`
	UpdatedAt time.Time `json:"updatedAt" gorm:"column:updated_at"`
	// DisplayName is an optional friendly label for the Site ID in the
	// picker; empty means show the ID itself.
	DisplayName string `json:"displayName" gorm:"column:display_name"`
	// Environment is "", "production", "staging" or "development". Only the
	// picker's colour and production warning read it; empty means neutral.
	Environment string `json:"environment" gorm:"column:environment"`
	// The shared-config document: the Site's pinned public key it is sealed
	// to, and which version the Site last fetched, when.
	SharedConfigKey       string     `json:"-" gorm:"column:shared_config_key"`
	SharedConfigVersion   string     `json:"-" gorm:"column:shared_config_version"`
	SharedConfigFetchedAt *time.Time `json:"-" gorm:"column:shared_config_fetched_at"`
	// When the Site's /site/info key was last read, and a different key it
	// served that an admin has to accept before it replaces the pinned one.
	SharedConfigKeyCheckedAt *time.Time `json:"-" gorm:"column:shared_config_key_checked_at"`
	SharedConfigPendingKey   string     `json:"-" gorm:"column:shared_config_pending_key"`
	// What the Site reports it applied, and when it last reported.
	SharedConfigAppliedVersion    string     `json:"-" gorm:"column:shared_config_applied_version"`
	SharedConfigAppliedReportedAt *time.Time `json:"-" gorm:"column:shared_config_applied_reported_at"`
}

func (Site) TableName() string {
	return "sites"
}

// SiteGrant gives UserID access to SiteID: ProjectID nil means Site-wide ADMIN,
// otherwise the given role on that project.
type SiteGrant struct {
	ID        uuid.UUID  `json:"id"        gorm:"column:id"`
	UserID    uuid.UUID  `json:"userId"    gorm:"column:user_id"`
	SiteID    string     `json:"siteId"    gorm:"column:site_id"`
	ProjectID *uuid.UUID `json:"projectId" gorm:"column:project_id"`
	Role      string     `json:"role"      gorm:"column:role"`
	CreatedBy uuid.UUID  `json:"createdBy" gorm:"column:created_by"`
	CreatedAt time.Time  `json:"createdAt" gorm:"column:created_at"`
	// SSOConnectorID is set on grants owned by SSO group sync; nil for manual grants.
	SSOConnectorID *uuid.UUID `json:"ssoConnectorId,omitempty" gorm:"column:sso_connector_id"`
}

func (SiteGrant) TableName() string {
	return "site_grants"
}

// SSOSiteGroupMapping maps an SSO group to Site access: ProjectID nil is a Site-wide
// role, otherwise a role on that project.
type SSOSiteGroupMapping struct {
	ID          uuid.UUID  `json:"id"          gorm:"column:id"`
	ConnectorID uuid.UUID  `json:"connectorId" gorm:"column:connector_id"`
	GroupName   string     `json:"groupName"   gorm:"column:group_name"`
	SiteID      string     `json:"siteId"      gorm:"column:site_id"`
	ProjectID   *uuid.UUID `json:"projectId"   gorm:"column:project_id"`
	Role        string     `json:"role"        gorm:"column:role"`
	CreatedAt   time.Time  `json:"createdAt"   gorm:"column:created_at"`
}

func (SSOSiteGroupMapping) TableName() string {
	return "sso_site_group_mappings"
}

// SigningKey is an ES256 assertion-signing keypair. RetiredAt nil marks the active key;
// retired keys are kept so Sites with a stale JWKS can still verify recent assertions.
type SigningKey struct {
	Kid                 string     `json:"kid"       gorm:"column:kid;primaryKey"`
	Algorithm           string     `json:"algorithm" gorm:"column:algorithm"`
	PrivateKeyEncrypted string     `json:"-"         gorm:"column:private_key_encrypted"`
	PublicKeyPEM        string     `json:"-"         gorm:"column:public_key_pem"`
	CreatedAt           time.Time  `json:"createdAt" gorm:"column:created_at"`
	RetiredAt           *time.Time `json:"retiredAt" gorm:"column:retired_at"`
}

func (SigningKey) TableName() string {
	return "control_plane_signing_keys"
}
