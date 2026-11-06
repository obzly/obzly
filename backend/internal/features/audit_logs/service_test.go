package audit_logs

import (
	"testing"
	"time"

	projects_models "obzly/internal/features/projects/models"
	user_enums "obzly/internal/features/users/enums"
	user_models "obzly/internal/features/users/models"
	users_testing "obzly/internal/features/users/testing"
	"obzly/internal/storage"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createTestProject inserts a bare row: this package can't import projects
// (it's their dependency), and audit_logs.project_id is FK-enforced.
func createTestProject(t *testing.T) uuid.UUID {
	t.Helper()
	project := &projects_models.Project{
		ID:        uuid.New(),
		Name:      "audit log test project",
		CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, storage.GetDb().Create(project).Error)
	return project.ID
}

func Test_AuditLogs_ProjectSpecificLogs(t *testing.T) {
	service := GetAuditLogService()
	user1 := users_testing.CreateTestUser(user_enums.UserRoleMember)
	user2 := users_testing.CreateTestUser(user_enums.UserRoleMember)
	project1ID, project2ID := createTestProject(t), createTestProject(t)

	createAuditLog(service, "Test project1 log first", &user1.UserID, &project1ID)
	createAuditLog(service, "Test project1 log second", &user2.UserID, &project1ID)
	createAuditLog(service, "Test project2 log first", &user1.UserID, &project2ID)
	createAuditLog(service, "Test project2 log second", &user2.UserID, &project2ID)
	createAuditLog(service, "Test no project log", &user1.UserID, nil)

	request := &GetAuditLogsRequest{Limit: 10, Offset: 0}

	project1Response, err := service.GetProjectAuditLogs(project1ID, request)
	assert.NoError(t, err)
	assert.Equal(t, 2, len(project1Response.AuditLogs))

	messages := extractMessages(project1Response.AuditLogs)
	assert.Contains(t, messages, "Test project1 log first")
	assert.Contains(t, messages, "Test project1 log second")
	for _, log := range project1Response.AuditLogs {
		assert.Equal(t, &project1ID, log.ProjectID)
	}

	project2Response, err := service.GetProjectAuditLogs(project2ID, request)
	assert.NoError(t, err)
	assert.Equal(t, 2, len(project2Response.AuditLogs))

	messages2 := extractMessages(project2Response.AuditLogs)
	assert.Contains(t, messages2, "Test project2 log first")
	assert.Contains(t, messages2, "Test project2 log second")

	limitedResponse, err := service.GetProjectAuditLogs(project1ID,
		&GetAuditLogsRequest{Limit: 1, Offset: 0})
	assert.NoError(t, err)
	assert.Equal(t, 1, len(limitedResponse.AuditLogs))
	assert.Equal(t, 1, limitedResponse.Limit)

	beforeTime := time.Now().UTC().Add(-1 * time.Minute)
	filteredResponse, err := service.GetProjectAuditLogs(project1ID,
		&GetAuditLogsRequest{Limit: 10, BeforeDate: &beforeTime})
	assert.NoError(t, err)
	for _, log := range filteredResponse.AuditLogs {
		assert.True(t, log.CreatedAt.Before(beforeTime))
		assert.NotNil(t, log.UserEmail, "User email should be present for logs with user_id")
		assert.NotNil(t, log.ProjectName, "Project name should be present for logs with project_id")
	}
}

// Total must be the full count, not the page length.
func Test_AuditLogs_TotalReflectsFullCountNotPageLength(t *testing.T) {
	service := GetAuditLogService()
	user := users_testing.CreateTestUser(user_enums.UserRoleMember)
	projectID := createTestProject(t)

	for range 3 {
		createAuditLog(service, "project total test "+uuid.New().String(), &user.UserID, &projectID)
	}

	pageResponse, err := service.GetProjectAuditLogs(projectID, &GetAuditLogsRequest{Limit: 1, Offset: 0})
	assert.NoError(t, err)
	assert.Len(t, pageResponse.AuditLogs, 1, "the page itself should be limited")
	assert.Equal(t, int64(3), pageResponse.Total, "Total should reflect all 3 records, not just this page's length")

	userResponse, err := service.GetUserAuditLogs(user.UserID, &user_models.User{ID: user.UserID, Role: user_enums.UserRoleMember},
		&GetAuditLogsRequest{Limit: 1, Offset: 0})
	assert.NoError(t, err)
	assert.Len(t, userResponse.AuditLogs, 1)
	assert.GreaterOrEqual(t, userResponse.Total, int64(3), "Total should reflect the user's full log count, not just this page's length")
}

func createAuditLog(service *AuditLogService, message string, userID, projectID *uuid.UUID) {
	service.WriteAuditLog(message, userID, projectID)
}

func extractMessages(logs []*AuditLogDTO) []string {
	messages := make([]string, len(logs))
	for i, log := range logs {
		messages[i] = log.Message
	}
	return messages
}

// The project audit log takes the same search, sort and paging as the
// global one, scoped to its project.
func Test_GetProjectAuditLogs_SearchSortAndPage_StayWithinProject(t *testing.T) {
	service := GetAuditLogService()
	actor := users_testing.CreateTestUser(user_enums.UserRoleMember)
	projectID, otherProjectID := createTestProject(t), createTestProject(t)
	marker := uuid.NewString()[:8]
	start := time.Now().UTC().Add(-time.Hour)
	for i, message := range []string{"created key " + marker, "deleted key " + marker, "renamed key " + marker} {
		require.NoError(t, (&AuditLogRepository{}).Create(&AuditLog{
			UserID: &actor.UserID, ProjectID: &projectID, Message: message,
			CreatedAt: start.Add(time.Duration(i) * time.Minute),
		}))
	}
	require.NoError(t, (&AuditLogRepository{}).Create(&AuditLog{
		UserID: &actor.UserID, ProjectID: &otherProjectID, Message: "deleted key " + marker, CreatedAt: start,
	}))

	byAction, err := service.GetProjectAuditLogs(projectID, &GetAuditLogsRequest{Action: "DELETED key " + marker})
	require.NoError(t, err)
	assert.Equal(t, int64(1), byAction.Total, "the other project's matching entry is excluded")

	byUser, err := service.GetProjectAuditLogs(projectID, &GetAuditLogsRequest{User: actor.Email, Action: marker})
	require.NoError(t, err)
	assert.Equal(t, int64(3), byUser.Total)

	asc, err := service.GetProjectAuditLogs(projectID, &GetAuditLogsRequest{
		Action: marker, SortBy: "createdAt", SortOrder: "asc", Limit: 2,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(3), asc.Total)
	require.Len(t, asc.AuditLogs, 2)
	assert.Equal(t, "created key "+marker, asc.AuditLogs[0].Message)
}
