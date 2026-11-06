// Package organizations holds organizations, their members, Sites and teams.
// They live where users are managed (the control plane, or a standalone
// install) and span Sites. A user's access is the strongest of their own grants
// and their teams' (control_plane GrantService.ResolveEffectiveGrant, or
// projects MembershipRepository on a standalone install).
package organizations

import (
	"time"

	"github.com/google/uuid"
)

// Organization roles.
const (
	RoleOwner  = "ORG_OWNER"
	RoleAdmin  = "ORG_ADMIN"
	RoleMember = "ORG_MEMBER"
)

// LocalSite is a team grant's site for this instance's own projects (a
// standalone install).
const LocalSite = ""

type Organization struct {
	ID        uuid.UUID `json:"id"        gorm:"column:id;primaryKey"`
	Name      string    `json:"name"      gorm:"column:name"`
	Slug      string    `json:"slug"      gorm:"column:slug"`
	CreatedAt time.Time `json:"createdAt" gorm:"column:created_at"`
	// MyRole: the caller's role ("" for a global admin who isn't a member).
	MyRole string `json:"myRole" gorm:"-"`
}

func (Organization) TableName() string { return "organizations" }

type Membership struct {
	OrgID     uuid.UUID `json:"orgId"     gorm:"column:org_id;primaryKey"`
	UserID    uuid.UUID `json:"userId"    gorm:"column:user_id;primaryKey"`
	Role      string    `json:"role"      gorm:"column:role"`
	CreatedAt time.Time `json:"createdAt" gorm:"column:created_at"`
	// Set by SCIM: the IdP's id, and deprovisioned (no team access).
	ExternalID *string `json:"externalId,omitempty" gorm:"column:external_id"`
	Suspended  bool    `json:"suspended"            gorm:"column:suspended"`
	Email      string  `json:"email"                gorm:"->;column:email"`
	Name       string  `json:"name"                 gorm:"->;column:name"`
}

func (Membership) TableName() string { return "organization_memberships" }

type OrgSite struct {
	OrgID  uuid.UUID `json:"orgId"  gorm:"column:org_id"`
	SiteID string    `json:"siteId" gorm:"column:site_id;primaryKey"`
}

func (OrgSite) TableName() string { return "organization_sites" }

type Team struct {
	ID         uuid.UUID `json:"id"         gorm:"column:id;primaryKey"`
	OrgID      uuid.UUID `json:"orgId"      gorm:"column:org_id"`
	Name       string    `json:"name"       gorm:"column:name"`
	ExternalID *string   `json:"externalId" gorm:"column:external_id"`
	CreatedAt  time.Time `json:"createdAt"  gorm:"column:created_at"`
	Members    []Member  `json:"members"    gorm:"-"`
	Grants     []Grant   `json:"grants"     gorm:"-"`
}

func (Team) TableName() string { return "teams" }

// Member is a team member.
type Member struct {
	UserID uuid.UUID `json:"userId" gorm:"column:user_id"`
	Email  string    `json:"email"  gorm:"column:email"`
	Name   string    `json:"name"   gorm:"column:name"`
}

type teamMembership struct {
	TeamID    uuid.UUID `gorm:"column:team_id;primaryKey"`
	UserID    uuid.UUID `gorm:"column:user_id;primaryKey"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

func (teamMembership) TableName() string { return "team_memberships" }

// Grant is a team's role on a Site (site-wide when ProjectID is nil: ADMIN, MEMBER
// or VIEWER) or on one of its projects (PROJECT_ADMIN, PROJECT_MEMBER or PROJECT_VIEWER).
type Grant struct {
	ID        uuid.UUID  `json:"id"        gorm:"column:id;primaryKey"`
	TeamID    uuid.UUID  `json:"teamId"    gorm:"column:team_id"`
	SiteID    string     `json:"siteId"    gorm:"column:site_id"`
	ProjectID *uuid.UUID `json:"projectId" gorm:"column:project_id"`
	Role      string     `json:"role"      gorm:"column:role"`
	CreatedBy *uuid.UUID `json:"createdBy" gorm:"column:created_by"`
	CreatedAt time.Time  `json:"createdAt" gorm:"column:created_at"`
}

func (Grant) TableName() string { return "team_grants" }
