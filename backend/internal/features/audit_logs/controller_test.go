package audit_logs

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	user_enums "obzly/internal/features/users/enums"
	users_middleware "obzly/internal/features/users/middleware"
	users_services "obzly/internal/features/users/services"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func Test_GetGlobalAuditLogs_WithDifferentUserRoles_EnforcesPermissionsCorrectly(t *testing.T) {
	users_testing.CleanupPlans()
	adminUser := users_testing.CreateTestUser(user_enums.UserRoleAdmin)
	memberUser := users_testing.CreateTestUser(user_enums.UserRoleMember)
	router := createRouter()
	service := GetAuditLogService()
	projectID := createTestProject(t)
	testID := uuid.New().String()

	userLogMessage := fmt.Sprintf("Test log with user %s", testID)
	projectLogMessage := fmt.Sprintf("Test log with project %s", testID)
	standaloneLogMessage := fmt.Sprintf("Test log standalone %s", testID)

	createAuditLog(service, userLogMessage, &adminUser.UserID, nil)
	createAuditLog(service, projectLogMessage, nil, &projectID)
	createAuditLog(service, standaloneLogMessage, nil, nil)

	var response GetAuditLogsResponse
	test_utils.MakeGetRequestAndUnmarshal(t, router,
		"/api/v1/audit-logs/global?limit=100", "Bearer "+adminUser.Token, http.StatusOK, &response)

	messages := extractMessages(response.AuditLogs)
	assert.Contains(t, messages, userLogMessage)
	assert.Contains(t, messages, projectLogMessage)
	assert.Contains(t, messages, standaloneLogMessage)

	resp := test_utils.MakeGetRequest(t, router, "/api/v1/audit-logs/global",
		"Bearer "+memberUser.Token, http.StatusForbidden)
	assert.Contains(t, string(resp.Body), "only administrators can view global audit logs")
}

func Test_GetUserAuditLogs_WithDifferentUserRoles_EnforcesPermissionsCorrectly(t *testing.T) {
	users_testing.CleanupPlans()
	adminUser := users_testing.CreateTestUser(user_enums.UserRoleAdmin)
	user1 := users_testing.CreateTestUser(user_enums.UserRoleMember)
	user2 := users_testing.CreateTestUser(user_enums.UserRoleMember)
	router := createRouter()
	service := GetAuditLogService()
	projectID := createTestProject(t)
	testID := uuid.New().String()

	user1FirstMessage := fmt.Sprintf("Test log user1 first %s", testID)
	user1SecondMessage := fmt.Sprintf("Test log user1 second %s", testID)
	user2FirstMessage := fmt.Sprintf("Test log user2 first %s", testID)
	user2SecondMessage := fmt.Sprintf("Test log user2 second %s", testID)
	projectLogMessage := fmt.Sprintf("Test project log %s", testID)

	createAuditLog(service, user1FirstMessage, &user1.UserID, nil)
	createAuditLog(service, user1SecondMessage, &user1.UserID, &projectID)
	createAuditLog(service, user2FirstMessage, &user2.UserID, nil)
	createAuditLog(service, user2SecondMessage, &user2.UserID, &projectID)
	createAuditLog(service, projectLogMessage, nil, &projectID)

	var user1Response GetAuditLogsResponse
	test_utils.MakeGetRequestAndUnmarshal(t, router,
		fmt.Sprintf("/api/v1/audit-logs/users/%s?limit=100", user1.UserID.String()),
		"Bearer "+adminUser.Token, http.StatusOK, &user1Response)

	messages := extractMessages(user1Response.AuditLogs)
	assert.Contains(t, messages, user1FirstMessage)
	assert.Contains(t, messages, user1SecondMessage)

	testLogsCount := 0
	for _, message := range messages {
		if message == user1FirstMessage || message == user1SecondMessage {
			testLogsCount++
		}
	}
	assert.Equal(t, 2, testLogsCount)

	var ownLogsResponse GetAuditLogsResponse
	test_utils.MakeGetRequestAndUnmarshal(t, router,
		fmt.Sprintf("/api/v1/audit-logs/users/%s?limit=100", user2.UserID.String()),
		"Bearer "+user2.Token, http.StatusOK, &ownLogsResponse)

	ownMessages := extractMessages(ownLogsResponse.AuditLogs)
	assert.Contains(t, ownMessages, user2FirstMessage)
	assert.Contains(t, ownMessages, user2SecondMessage)

	resp := test_utils.MakeGetRequest(t, router,
		fmt.Sprintf("/api/v1/audit-logs/users/%s", user1.UserID.String()),
		"Bearer "+user2.Token, http.StatusForbidden)

	assert.Contains(t, string(resp.Body), "insufficient permissions")
}

func Test_GetGlobalAuditLogs_WithBeforeDateFilter_ReturnsFilteredLogs(t *testing.T) {
	users_testing.CleanupPlans()
	adminUser := users_testing.CreateTestUser(user_enums.UserRoleAdmin)
	router := createRouter()
	baseTime := time.Now().UTC()

	beforeTime := baseTime.Add(-30 * time.Minute)

	var filteredResponse GetAuditLogsResponse
	test_utils.MakeGetRequestAndUnmarshal(t, router,
		fmt.Sprintf("/api/v1/audit-logs/global?beforeDate=%s&limit=1000", beforeTime.Format(time.RFC3339)),
		"Bearer "+adminUser.Token, http.StatusOK, &filteredResponse)

	for _, log := range filteredResponse.AuditLogs {
		assert.True(t, log.CreatedAt.Before(beforeTime),
			fmt.Sprintf("Log created at %s should be before filter time %s",
				log.CreatedAt.Format(time.RFC3339), beforeTime.Format(time.RFC3339)))
	}
}

func createRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	SetupDependencies()

	v1 := router.Group("/api/v1")
	protected := v1.Group("").Use(users_middleware.AuthMiddleware(users_services.GetUserService()))
	GetAuditLogController().RegisterRoutes(protected.(*gin.RouterGroup))

	return router
}

// seedAuditLogs writes marker-tagged entries a minute apart (oldest first).
func seedAuditLogs(t *testing.T, userID *uuid.UUID, messages ...string) {
	t.Helper()
	start := time.Now().UTC().Add(-time.Hour)
	for i, message := range messages {
		assert.NoError(t, (&AuditLogRepository{}).Create(&AuditLog{
			UserID: userID, Message: message, CreatedAt: start.Add(time.Duration(i) * time.Minute),
		}))
	}
}

func getGlobalAuditLogs(t *testing.T, router *gin.Engine, token, query string) GetAuditLogsResponse {
	t.Helper()
	var response GetAuditLogsResponse
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/audit-logs/global?"+query,
		"Bearer "+token, http.StatusOK, &response)
	return response
}

func Test_GetGlobalAuditLogs_FilterByActionAndUser_TotalMatchesFilter(t *testing.T) {
	users_testing.CleanupPlans()
	admin := users_testing.CreateTestUser(user_enums.UserRoleAdmin)
	actor := users_testing.CreateTestUser(user_enums.UserRoleMember)
	router := createRouter()
	marker := uuid.NewString()[:8]
	seedAuditLogs(t, &actor.UserID, "created project "+marker, "deleted project "+marker, "renamed project "+marker)
	seedAuditLogs(t, &admin.UserID, "created project "+marker)

	byAction := getGlobalAuditLogs(t, router, admin.Token, "action=deleted+project+"+marker)
	assert.Equal(t, int64(1), byAction.Total)
	assert.Len(t, byAction.AuditLogs, 1)

	byUser := getGlobalAuditLogs(t, router, admin.Token, "action="+marker+"&user="+actor.Email)
	assert.Equal(t, int64(3), byUser.Total, "only the actor's entries, counted with the same filter")
	for _, log := range byUser.AuditLogs {
		assert.Equal(t, actor.Email, *log.UserEmail)
	}

	paged := getGlobalAuditLogs(t, router, admin.Token, "action="+marker+"&limit=2")
	assert.Equal(t, int64(4), paged.Total)
	assert.Len(t, paged.AuditLogs, 2)
}

func Test_GetGlobalAuditLogs_SearchWildcardsMatchLiterally(t *testing.T) {
	users_testing.CleanupPlans()
	admin := users_testing.CreateTestUser(user_enums.UserRoleAdmin)
	router := createRouter()
	marker := uuid.NewString()[:8]
	seedAuditLogs(t, nil, "p50%_q"+marker, "p50zzq"+marker)

	response := getGlobalAuditLogs(t, router, admin.Token, "action=50%25_q"+marker)
	assert.Equal(t, int64(1), response.Total, "% and _ in the search are literal, not LIKE wildcards")
}

func Test_GetGlobalAuditLogs_SortByCreatedAt_OrdersBothWays(t *testing.T) {
	users_testing.CleanupPlans()
	admin := users_testing.CreateTestUser(user_enums.UserRoleAdmin)
	router := createRouter()
	marker := uuid.NewString()[:8]
	seedAuditLogs(t, nil, "first "+marker, "second "+marker, "third "+marker)

	desc := getGlobalAuditLogs(t, router, admin.Token, "action="+marker)
	asc := getGlobalAuditLogs(t, router, admin.Token, "action="+marker+"&sortBy=createdAt&sortOrder=asc")
	assert.Equal(t, "third "+marker, desc.AuditLogs[0].Message, "newest first by default")
	assert.Equal(t, "first "+marker, asc.AuditLogs[0].Message)

	// An unknown sort field falls back to time, never reaching the SQL.
	injected := getGlobalAuditLogs(t, router, admin.Token, "action="+marker+"&sortBy=message;DROP+TABLE+audit_logs")
	assert.Equal(t, int64(3), injected.Total)
}
