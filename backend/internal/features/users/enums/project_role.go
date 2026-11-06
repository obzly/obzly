package users_enums

type ProjectRole string

const (
	ProjectRoleOwner  ProjectRole = "OWNER"
	ProjectRoleAdmin  ProjectRole = "PROJECT_ADMIN"
	ProjectRoleMember ProjectRole = "PROJECT_MEMBER"
	// ProjectRoleViewer can see every section (Logs, Traces, Metrics,
	// Dashboards) and run queries, but can't create/edit/delete anything -
	// see authz.ProjectWrite for the read/write split every write-capable
	// endpoint checks against.
	ProjectRoleViewer ProjectRole = "PROJECT_VIEWER"
)

// IsValid validates the ProjectRole
func (r ProjectRole) IsValid() bool {
	switch r {
	case ProjectRoleOwner, ProjectRoleAdmin, ProjectRoleMember, ProjectRoleViewer:
		return true
	default:
		return false
	}
}
