package projects_controllers

import (
	"testing"

	projects_repositories "obzly/internal/features/projects/repositories"
	projects_services "obzly/internal/features/projects/services"
	projects_testing "obzly/internal/features/projects/testing"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A Site-wide MEMBER/VIEWER grant (allProjectsRole) gives that role on every
// project; an explicit per-project grant that ranks higher wins for that
// project; and dropping the Site-wide grant removes the memberships it made.
func Test_SyncCentralMemberships_AllProjectsRole_ExpandsAndMerges(t *testing.T) {
	users_testing.CleanupPlans()
	router := projects_testing.CreateTestRouter(GetProjectController(), GetMembershipController())
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	projectA := projects_testing.CreateTestProject("Site-wide A", owner, router)
	projectB := projects_testing.CreateTestProject("Site-wide B", owner, router)

	sync := func(desired map[uuid.UUID]users_enums.ProjectRole, all users_enums.ProjectRole) {
		t.Helper()
		require.NoError(t, projects_services.GetMembershipService().SyncCentralMemberships(user.UserID, desired, all))
	}
	membership := func(projectID uuid.UUID) (users_enums.ProjectRole, bool, bool) {
		m, err := (&projects_repositories.MembershipRepository{}).GetMembershipByUserAndProject(user.UserID, projectID)
		if err != nil {
			return "", false, false
		}
		return m.Role, m.CentralManaged, true
	}

	sync(map[uuid.UUID]users_enums.ProjectRole{projectA.ID: users_enums.ProjectRoleAdmin}, users_enums.ProjectRoleViewer)

	role, managed, found := membership(projectA.ID)
	require.True(t, found)
	assert.Equal(t, users_enums.ProjectRoleAdmin, role, "the explicit, higher per-project grant wins")
	assert.True(t, managed)

	role, managed, found = membership(projectB.ID)
	require.True(t, found, "the Site-wide grant covers a project with no explicit grant")
	assert.Equal(t, users_enums.ProjectRoleViewer, role)
	assert.True(t, managed)

	// Site-wide MEMBER outranks an explicit VIEWER.
	sync(map[uuid.UUID]users_enums.ProjectRole{projectA.ID: users_enums.ProjectRoleViewer}, users_enums.ProjectRoleMember)
	role, _, _ = membership(projectA.ID)
	assert.Equal(t, users_enums.ProjectRoleMember, role)

	// Grant removed: the memberships it created go away.
	sync(map[uuid.UUID]users_enums.ProjectRole{}, "")
	_, _, found = membership(projectA.ID)
	assert.False(t, found)
	_, _, found = membership(projectB.ID)
	assert.False(t, found)
}
