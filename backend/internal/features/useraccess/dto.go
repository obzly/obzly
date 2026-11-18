// Package useraccess serves GET /api/v1/users/{id}/detail: how a user signs
// in and what they can reach, for the Users page's side panel. It reads
// users, SSO identities, organizations and grants, which live in different
// features, so it sits outside users (no import cycle).
package useraccess

import (
	"time"

	users_enums "obzly/internal/features/users/enums"

	"github.com/google/uuid"
)

// UserDetail is one user for an admin.
type UserDetail struct {
	ID           uuid.UUID              `json:"id"`
	Email        string                 `json:"email"`
	Name         string                 `json:"name"`
	Role         users_enums.UserRole   `json:"role"`
	Status       users_enums.UserStatus `json:"status"`
	IsActive     bool                   `json:"isActive"`
	IsRootAdmin  bool                   `json:"isRootAdmin"`
	CreatedAt    time.Time              `json:"createdAt"`
	LastLoginAt  *time.Time             `json:"lastLoginAt"`
	LastActiveAt *time.Time             `json:"lastActiveAt"`
	MFAEnabled   bool                   `json:"mfaEnabled"`
	MFARequired  bool                   `json:"mfaRequired"`
	// SignInMethods are the ways the account can sign in (not which it used last).
	SignInMethods []SignInMethod `json:"signInMethods"`
	// ProvisionedBy is the organization whose SCIM created the account.
	ProvisionedBy *OrgRef            `json:"provisionedBy"`
	Organizations []OrgMembership    `json:"organizations"`
	SiteAccess    []SiteAccess       `json:"siteAccess"`
	ProjectRoles  []LocalProjectRole `json:"projectRoles"`
	// ProjectRolesOnSites: on a control plane, roles given on a Site itself
	// (its project settings) aren't known here; SiteAccess is what the
	// control plane grants.
	ProjectRolesOnSites bool `json:"projectRolesOnSites"`
}

// SignInMethod Kind is password, sso, github or google; Name is the SSO
// connector's.
type SignInMethod struct {
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
}

type OrgRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

type OrgMembership struct {
	OrgID     uuid.UUID `json:"orgId"`
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	Suspended bool      `json:"suspended"`
	Teams     []string  `json:"teams"`
}

// SiteAccess is a control-plane grant on a Site: Site-wide when ProjectID is
// nil, else on that project (whose name only the Site knows). Via is direct,
// sso (SSO group sync) or team (Team and Org name it).
type SiteAccess struct {
	SiteID    string     `json:"siteId"`
	SiteName  string     `json:"siteName"`
	ProjectID *uuid.UUID `json:"projectId"`
	Role      string     `json:"role"`
	Via       string     `json:"via"`
	Team      string     `json:"team,omitempty"`
	Org       string     `json:"org,omitempty"`
}

// LocalProjectRole is a role on this instance's own projects: ProjectID nil
// is every project (a team grant). Via is direct, sso, control-plane or team.
type LocalProjectRole struct {
	ProjectID   *uuid.UUID `json:"projectId"`
	ProjectName string     `json:"projectName"`
	Role        string     `json:"role"`
	Via         string     `json:"via"`
	Team        string     `json:"team,omitempty"`
	Org         string     `json:"org,omitempty"`
}
