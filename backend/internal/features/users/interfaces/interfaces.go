package users_interfaces

import (
	users_enums "obzly/internal/features/users/enums"

	"github.com/google/uuid"
)

type AuditLogWriter interface {
	WriteAuditLog(message string, userID *uuid.UUID, projectID *uuid.UUID)
}

type PlanChangeListener interface {
	OnPlanChanged(planID uuid.UUID) error
}

type UserPlanChangeListener interface {
	OnUserPlanChanged(userID uuid.UUID, previousPlanID *uuid.UUID, newPlanID *uuid.UUID) error
}

type PlanDeletionListener interface {
	OnBeforePlanDeletion(planID uuid.UUID) error
}

// GroupMembershipSyncer makes a user's project memberships match their SSO groups
// or a control-plane assertion. Implemented by projects, injected to avoid an import cycle.
type GroupMembershipSyncer interface {
	SyncSSOGroupMemberships(
		userID, connectorID uuid.UUID,
		desired map[uuid.UUID]users_enums.ProjectRole,
	) error

	// SyncCentralMemberships applies an exchange's grants (see
	// ProjectMembership.CentralManaged); allProjectsRole, when set, applies to every
	// project on top of desired.
	SyncCentralMemberships(
		userID uuid.UUID,
		desired map[uuid.UUID]users_enums.ProjectRole,
		allProjectsRole users_enums.ProjectRole,
	) error
}

// SSOSiteGrantSyncer applies SSO group-to-Site mappings at sign-in on a control
// plane. Implemented by control_plane and injected only in that mode.
type SSOSiteGrantSyncer interface {
	SyncSSOSiteGrants(userID, connectorID uuid.UUID, groups []string) error
	// GroupsGrantSiteAccess reports whether any of groups maps to Site access on
	// connectorID, which lets a first sign-in create the account with registration closed.
	GroupsGrantSiteAccess(connectorID uuid.UUID, groups []string) (bool, error)
}

// SessionRevocationListener tears down a user's Site sessions after a central
// sign-out, deactivation, or role/password change (control plane only). Failures
// are logged, not returned: Site tokens expire within an hour anyway.
type SessionRevocationListener interface {
	OnUserSessionsRevoked(userID uuid.UUID) error
}
