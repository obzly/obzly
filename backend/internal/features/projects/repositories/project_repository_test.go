package projects_repositories

import (
	"testing"

	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"

	projects_models "obzly/internal/features/projects/models"
	"obzly/internal/storage"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Both inserts succeeding leave both rows, correctly linked.
func Test_CreateProjectWithOwnerMembership_BothRowsCreatedTogether(t *testing.T) {
	repo := &ProjectRepository{}
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)

	project := &projects_models.Project{Name: "Atomic Create Test"}
	membership := &projects_models.ProjectMembership{
		UserID: owner.UserID,
		Role:   users_enums.ProjectRoleOwner,
	}

	require.NoError(t, repo.CreateProjectWithOwnerMembership(project, membership))
	assert.NotEqual(t, uuid.Nil, project.ID)

	fetched, err := repo.GetProjectByID(project.ID)
	require.NoError(t, err)
	require.NotNil(t, fetched)

	var storedMembership projects_models.ProjectMembership
	require.NoError(t, storage.GetDb().
		Where("project_id = ? AND user_id = ?", project.ID, owner.UserID).
		First(&storedMembership).Error)
	assert.Equal(t, users_enums.ProjectRoleOwner, storedMembership.Role)
}

// Forces the membership insert to fail (an invalid role, rejected by a CHECK constraint)
// and asserts the project row was rolled back too.
func Test_CreateProjectWithOwnerMembership_MembershipFailure_RollsBackProject(t *testing.T) {
	repo := &ProjectRepository{}
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)

	project := &projects_models.Project{Name: "Rollback Test"}
	membership := &projects_models.ProjectMembership{
		UserID: owner.UserID,
		Role:   "NOT_A_REAL_ROLE",
	}

	err := repo.CreateProjectWithOwnerMembership(project, membership)
	require.Error(t, err)

	var count int64
	require.NoError(t, storage.GetDb().
		Model(&projects_models.Project{}).
		Where("id = ?", project.ID).
		Count(&count).Error)
	assert.Equal(t, int64(0), count, "the project row must not survive a failed membership insert")
}
