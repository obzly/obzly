package control_plane_dto

import (
	"time"

	"github.com/google/uuid"
)

// SiteResponseDTO is the non-admin view of a Site; it never includes ApiURL.
type SiteResponseDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	DisplayName string `json:"displayName"`
	Environment string `json:"environment"`
	// SiteRole is "ADMIN", or "" when the caller only has project-scoped grants.
	SiteRole string `json:"siteRole,omitempty"`
}

// SiteAdminResponseDTO is the central-ADMIN view of a Site, including ApiURL.
type SiteAdminResponseDTO struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	ApiURL      string    `json:"apiUrl"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	DisplayName string    `json:"displayName"`
	Environment string    `json:"environment"`
}

type CreateSiteRequestDTO struct {
	ID          string `json:"id"          binding:"required"`
	Name        string `json:"name"        binding:"required"`
	ApiURL      string `json:"apiUrl"      binding:"required"`
	DisplayName string `json:"displayName"`
	Environment string `json:"environment"`
}

// DisplayName and Environment are pointers so a caller that omits them
// (e.g. the enable/disable toggle) keeps the stored values.
type UpdateSiteRequestDTO struct {
	Name        string  `json:"name"        binding:"required"`
	ApiURL      string  `json:"apiUrl"      binding:"required"`
	Enabled     bool    `json:"enabled"`
	DisplayName *string `json:"displayName"`
	Environment *string `json:"environment"`
}

// SiteCheckResponseDTO is the result of a server-side call to the Site's
// /api/v1/site/info, comparing SiteID to catch a wrong apiUrl before granting access.
type SiteCheckResponseDTO struct {
	Reachable      bool   `json:"reachable"`
	SiteIDMatches  bool   `json:"siteIdMatches"`
	ReportedSiteID string `json:"reportedSiteId,omitempty"`
	Version        string `json:"version,omitempty"`
	Error          string `json:"error,omitempty"`
}

type GrantResponseDTO struct {
	ID        uuid.UUID  `json:"id"`
	UserID    uuid.UUID  `json:"userId"`
	UserEmail string     `json:"userEmail"`
	SiteID    string     `json:"siteId"`
	ProjectID *uuid.UUID `json:"projectId"`
	Role      string     `json:"role"`
	CreatedAt time.Time  `json:"createdAt"`
	// SSOManaged: created by SSO group sync (an SSO Site group mapping);
	// removing it by hand only lasts until the user's next SSO sign-in.
	SSOManaged bool `json:"ssoManaged,omitempty"`
}

// CreateSSOSiteGroupMappingRequestDTO maps an SSO connector's group to a
// Site: a Site-wide role (ProjectID nil: ADMIN / MEMBER / VIEWER) or a
// project role on one of that Site's projects.
type CreateSSOSiteGroupMappingRequestDTO struct {
	GroupName string     `json:"groupName" binding:"required"`
	SiteID    string     `json:"siteId"    binding:"required"`
	ProjectID *uuid.UUID `json:"projectId"`
	Role      string     `json:"role"      binding:"required"`
}

type CreateGrantRequestDTO struct {
	UserID    uuid.UUID  `json:"userId"    binding:"required"`
	SiteID    string     `json:"siteId"    binding:"required"`
	ProjectID *uuid.UUID `json:"projectId"`
	Role      string     `json:"role"      binding:"required"`
}
