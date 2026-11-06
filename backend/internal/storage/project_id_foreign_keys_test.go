package storage_test

import (
	"testing"
	"time"

	projects_models "obzly/internal/features/projects/models"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	"obzly/internal/storage"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createTestProject inserts a bare row: importing projects/services here
// would be a dependency cycle.
func createTestProject(t *testing.T) uuid.UUID {
	t.Helper()
	project := &projects_models.Project{
		ID:        uuid.New(),
		Name:      "FK test project",
		CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, storage.GetDb().Create(project).Error)
	return project.ID
}

// dashboards and dashboard_folders used to accept any UUID in project_id.
// stream_segments is unconstrained on purpose (see migration 20260922060000).
func Test_ProjectIDColumns_RejectUnknownProject(t *testing.T) {
	bogusProjectID := uuid.New()

	t.Run("dashboards", func(t *testing.T) {
		err := storage.GetDb().Exec(
			`INSERT INTO dashboards (id, project_id, title, description) VALUES (?, ?, 'x', '')`,
			uuid.New(), bogusProjectID,
		).Error
		assert.Error(t, err)
	})

	t.Run("dashboard_folders", func(t *testing.T) {
		err := storage.GetDb().Exec(
			`INSERT INTO dashboard_folders (id, project_id, title) VALUES (?, ?, 'x')`,
			uuid.New(), bogusProjectID,
		).Error
		assert.Error(t, err)
	})
}

// CASCADE for rows meaningless without their project, SET NULL for audit_logs,
// which must outlive the project.
func Test_DeleteProject_CascadesToDashboards_NullsAuditLogs(t *testing.T) {
	projectID := createTestProject(t)
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)

	folderID := uuid.New()
	require.NoError(t, storage.GetDb().Exec(
		`INSERT INTO dashboard_folders (id, project_id, title) VALUES (?, ?, 'x')`,
		folderID, projectID,
	).Error)

	dashboardID := uuid.New()
	require.NoError(t, storage.GetDb().Exec(
		`INSERT INTO dashboards (id, project_id, title, description) VALUES (?, ?, 'x', '')`,
		dashboardID, projectID,
	).Error)

	now := time.Now().UTC()
	auditLogID := uuid.New()
	require.NoError(t, storage.GetDb().Exec(
		`INSERT INTO audit_logs (id, project_id, user_id, message, created_at)
			VALUES (?, ?, ?, 'test entry', ?)`,
		auditLogID, projectID, user.UserID, now,
	).Error)

	require.NoError(t, storage.GetDb().Exec(`DELETE FROM projects WHERE id = ?`, projectID).Error)

	var count int64

	require.NoError(t, storage.GetDb().Raw(`SELECT count(*) FROM dashboard_folders WHERE id = ?`, folderID).Scan(&count).Error)
	assert.Equal(t, int64(0), count, "dashboard_folders row must cascade-delete with its project")

	require.NoError(t, storage.GetDb().Raw(`SELECT count(*) FROM dashboards WHERE id = ?`, dashboardID).Scan(&count).Error)
	assert.Equal(t, int64(0), count, "dashboards row must cascade-delete with its project")

	var auditLogProjectID uuid.NullUUID
	require.NoError(t, storage.GetDb().Raw(`SELECT project_id FROM audit_logs WHERE id = ?`, auditLogID).Scan(&auditLogProjectID).Error)
	assert.False(t, auditLogProjectID.Valid, "audit_logs row must survive, with project_id set to NULL")

	require.NoError(t, storage.GetDb().Raw(`SELECT count(*) FROM audit_logs WHERE id = ?`, auditLogID).Scan(&count).Error)
	assert.Equal(t, int64(1), count, "the audit log entry itself must not be deleted")
}
