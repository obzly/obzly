package users_controllers

import (
	"net/http"
	"testing"

	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	users_middleware "obzly/internal/features/users/middleware"
	users_models "obzly/internal/features/users/models"
	users_services "obzly/internal/features/users/services"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_GetPlans_WhenUserIsAuthenticated_ReturnsPlans(t *testing.T) {
	users_testing.CleanupPlans()
	router := createUserPlanTestRouter()

	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	planRequest := users_testing.CreateValidPlanRequest("Test Plan", users_enums.UserPlanTypePro)
	users_testing.CreateTestPlanViaAPI(t, planRequest, admin.Token, router)

	var plans []users_models.UserPlan
	test_utils.MakeGetRequestAndUnmarshal(
		t,
		router,
		"/api/v1/plans",
		"Bearer "+admin.Token,
		http.StatusOK,
		&plans,
	)

	assert.GreaterOrEqual(t, len(plans), 1)
}

// A non-public plan must be visible only to an Admin.
func Test_GetPlans_WhenUserIsMember_OnlyReturnsPublicPlans(t *testing.T) {
	users_testing.CleanupPlans()
	router := createUserPlanTestRouter()

	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	member := users_testing.CreateTestUser(users_enums.UserRoleMember)

	publicRequest := users_testing.CreateValidPlanRequest("Public Plan", users_enums.UserPlanTypePro)
	users_testing.CreateTestPlanViaAPI(t, publicRequest, admin.Token, router)

	privateRequest := users_testing.CreateValidPlanRequest("Private Plan", users_enums.UserPlanTypePro)
	privateRequest.IsPublic = false
	users_testing.CreateTestPlanViaAPI(t, privateRequest, admin.Token, router)

	var adminPlans []users_models.UserPlan
	test_utils.MakeGetRequestAndUnmarshal(
		t, router, "/api/v1/plans", "Bearer "+admin.Token, http.StatusOK, &adminPlans,
	)
	assert.Len(t, adminPlans, 2, "an admin should see every plan, public or not")

	var memberPlans []users_models.UserPlan
	test_utils.MakeGetRequestAndUnmarshal(
		t, router, "/api/v1/plans", "Bearer "+member.Token, http.StatusOK, &memberPlans,
	)
	require.Len(t, memberPlans, 1, "a member should only see the public plan")
	assert.Equal(t, "Public Plan", memberPlans[0].Name)
	assert.True(t, memberPlans[0].IsPublic)
}

func Test_GetPlans_WhenNoPlansExist_ReturnsEmptyArray(t *testing.T) {
	users_testing.CleanupPlans()
	router := createUserPlanTestRouter()

	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	var plans []users_models.UserPlan
	test_utils.MakeGetRequestAndUnmarshal(
		t,
		router,
		"/api/v1/plans",
		"Bearer "+admin.Token,
		http.StatusOK,
		&plans,
	)

	assert.Equal(t, 0, len(plans))
}

func Test_CreatePlan_WhenUserIsAdmin_PlanCreated(t *testing.T) {
	users_testing.CleanupPlans()
	router := createUserPlanTestRouter()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	request := users_testing.CreateValidPlanRequest("Admin Plan", users_enums.UserPlanTypePro)

	var plan users_models.UserPlan
	test_utils.MakePostRequestAndUnmarshal(
		t,
		router,
		"/api/v1/plans",
		"Bearer "+admin.Token,
		request,
		http.StatusOK,
		&plan,
	)

	assert.Equal(t, "Admin Plan", plan.Name)
	assert.Equal(t, users_enums.UserPlanTypePro, plan.Type)
	assert.Equal(t, 100, plan.LogsPerSecondLimit)
	assert.Equal(t, int64(1000000), plan.MaxLogsAmount)
	assert.Equal(t, 1024, plan.MaxLogsSizeMB)
	assert.Equal(t, 30, plan.MaxLogsLifeDays)
	assert.Equal(t, 256, plan.MaxLogSizeKB)
	assert.Equal(t, 5, plan.AllowedProjectsCount)
}

func Test_CreatePlan_WhenUserIsMember_ReturnsForbidden(t *testing.T) {
	users_testing.CleanupPlans()
	router := createUserPlanTestRouter()
	member := users_testing.CreateTestUser(users_enums.UserRoleMember)

	request := users_testing.CreateValidPlanRequest("Member Plan", users_enums.UserPlanTypePro)

	resp := test_utils.MakePostRequest(
		t,
		router,
		"/api/v1/plans",
		"Bearer "+member.Token,
		request,
		http.StatusForbidden,
	)

	assert.Contains(t, string(resp.Body), "Insufficient permissions")
}

func Test_CreatePlan_WithValidDefaultTypePlan_PlanCreated(t *testing.T) {
	users_testing.CleanupPlans()
	router := createUserPlanTestRouter()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	request := users_testing.CreateValidPlanRequest("Default Plan", users_enums.UserPlanTypeDefault)

	var plan users_models.UserPlan
	test_utils.MakePostRequestAndUnmarshal(
		t,
		router,
		"/api/v1/plans",
		"Bearer "+admin.Token,
		request,
		http.StatusOK,
		&plan,
	)

	assert.Equal(t, "Default Plan", plan.Name)
	assert.Equal(t, users_enums.UserPlanTypeDefault, plan.Type)
}

func Test_CreatePlan_WithDuplicateDefaultPlan_ReturnsBadRequest(t *testing.T) {
	users_testing.CleanupPlans()
	router := createUserPlanTestRouter()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	request1 := users_testing.CreateValidPlanRequest("First Default Plan", users_enums.UserPlanTypeDefault)
	users_testing.CreateTestPlanViaAPI(t, request1, admin.Token, router)

	request2 := users_testing.CreateValidPlanRequest("Second Default Plan", users_enums.UserPlanTypeDefault)
	resp := test_utils.MakePostRequest(
		t,
		router,
		"/api/v1/plans",
		"Bearer "+admin.Token,
		request2,
		http.StatusBadRequest,
	)

	assert.Contains(t, string(resp.Body), "DEFAULT plan already exists")
}

func Test_CreatePlan_WithValidExtendedTypePlan_PlanCreated(t *testing.T) {
	users_testing.CleanupPlans()
	router := createUserPlanTestRouter()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	request := users_testing.CreateValidPlanRequest("Extended Plan", users_enums.UserPlanTypePro)

	var plan users_models.UserPlan
	test_utils.MakePostRequestAndUnmarshal(
		t,
		router,
		"/api/v1/plans",
		"Bearer "+admin.Token,
		request,
		http.StatusOK,
		&plan,
	)

	assert.Equal(t, "Extended Plan", plan.Name)
	assert.Equal(t, users_enums.UserPlanTypePro, plan.Type)
}

func Test_CreatePlan_WithMissingRequiredFields_ReturnsBadRequest(t *testing.T) {
	users_testing.CleanupPlans()
	router := createUserPlanTestRouter()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	testCases := []struct {
		name    string
		request users_dto.CreatePlanRequestDTO
	}{
		{
			name: "missing name",
			request: users_dto.CreatePlanRequestDTO{
				Type:                 users_enums.UserPlanTypePro,
				LogsPerSecondLimit:   100,
				MaxLogsAmount:        1000000,
				MaxLogsSizeMB:        1024,
				MaxLogsLifeDays:      30,
				MaxLogSizeKB:         256,
				AllowedProjectsCount: 5,
			},
		},
		{
			name: "missing type",
			request: users_dto.CreatePlanRequestDTO{
				Name:                 "Test Plan",
				LogsPerSecondLimit:   100,
				MaxLogsAmount:        1000000,
				MaxLogsSizeMB:        1024,
				MaxLogsLifeDays:      30,
				MaxLogSizeKB:         256,
				AllowedProjectsCount: 5,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			test_utils.MakePostRequest(
				t,
				router,
				"/api/v1/plans",
				"Bearer "+admin.Token,
				tc.request,
				http.StatusBadRequest,
			)
		})
	}
}

func Test_UpdatePlan_WhenUserIsAdmin_PlanUpdated(t *testing.T) {
	users_testing.CleanupPlans()
	router := createUserPlanTestRouter()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	createRequest := users_testing.CreateValidPlanRequest("Original Plan", users_enums.UserPlanTypePro)
	plan := users_testing.CreateTestPlanViaAPI(t, createRequest, admin.Token, router)

	newName := "Updated Plan"
	newLogsPerSecond := 200
	updateRequest := users_dto.UpdatePlanRequestDTO{
		Name:               &newName,
		LogsPerSecondLimit: &newLogsPerSecond,
	}

	var updatedPlan users_models.UserPlan
	test_utils.MakePutRequestAndUnmarshal(
		t,
		router,
		"/api/v1/plans/"+plan.ID.String(),
		"Bearer "+admin.Token,
		updateRequest,
		http.StatusOK,
		&updatedPlan,
	)

	assert.Equal(t, "Updated Plan", updatedPlan.Name)
	assert.Equal(t, 200, updatedPlan.LogsPerSecondLimit)
	assert.Equal(t, plan.MaxLogsAmount, updatedPlan.MaxLogsAmount)
}

func Test_UpdatePlan_PartialUpdate_OnlySpecifiedFieldsUpdated(t *testing.T) {
	users_testing.CleanupPlans()
	router := createUserPlanTestRouter()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	createRequest := users_testing.CreateValidPlanRequest("Original Plan", users_enums.UserPlanTypePro)
	plan := users_testing.CreateTestPlanViaAPI(t, createRequest, admin.Token, router)

	newName := "Partially Updated Plan"
	updateRequest := users_dto.UpdatePlanRequestDTO{
		Name: &newName,
	}

	var updatedPlan users_models.UserPlan
	test_utils.MakePutRequestAndUnmarshal(
		t,
		router,
		"/api/v1/plans/"+plan.ID.String(),
		"Bearer "+admin.Token,
		updateRequest,
		http.StatusOK,
		&updatedPlan,
	)

	assert.Equal(t, "Partially Updated Plan", updatedPlan.Name)
	assert.Equal(t, plan.Type, updatedPlan.Type)
	assert.Equal(t, plan.LogsPerSecondLimit, updatedPlan.LogsPerSecondLimit)
	assert.Equal(t, plan.MaxLogsAmount, updatedPlan.MaxLogsAmount)
}

func Test_UpdatePlan_ChangingToDefaultType_WhenNoOtherDefault_Success(t *testing.T) {
	users_testing.CleanupPlans()

	router := createUserPlanTestRouter()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	createRequest := users_testing.CreateValidPlanRequest("Extended Plan", users_enums.UserPlanTypePro)
	plan := users_testing.CreateTestPlanViaAPI(t, createRequest, admin.Token, router)

	newType := users_enums.UserPlanTypeDefault
	updateRequest := users_dto.UpdatePlanRequestDTO{
		Type: &newType,
	}

	var updatedPlan users_models.UserPlan
	test_utils.MakePutRequestAndUnmarshal(
		t,
		router,
		"/api/v1/plans/"+plan.ID.String(),
		"Bearer "+admin.Token,
		updateRequest,
		http.StatusOK,
		&updatedPlan,
	)

	assert.Equal(t, users_enums.UserPlanTypeDefault, updatedPlan.Type)
}

func Test_UpdatePlan_ChangingToDefaultType_WhenOtherDefaultExists_ReturnsBadRequest(t *testing.T) {
	users_testing.CleanupPlans()

	router := createUserPlanTestRouter()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	defaultRequest := users_testing.CreateValidPlanRequest("Default Plan", users_enums.UserPlanTypeDefault)
	users_testing.CreateTestPlanViaAPI(t, defaultRequest, admin.Token, router)

	extendedRequest := users_testing.CreateValidPlanRequest("Extended Plan", users_enums.UserPlanTypePro)
	extendedPlan := users_testing.CreateTestPlanViaAPI(t, extendedRequest, admin.Token, router)

	newType := users_enums.UserPlanTypeDefault
	updateRequest := users_dto.UpdatePlanRequestDTO{
		Type: &newType,
	}

	resp := test_utils.MakePutRequest(
		t,
		router,
		"/api/v1/plans/"+extendedPlan.ID.String(),
		"Bearer "+admin.Token,
		updateRequest,
		http.StatusBadRequest,
	)

	assert.Contains(t, string(resp.Body), "DEFAULT plan already exists")
}

func Test_UpdatePlan_WhenUserIsMember_ReturnsForbidden(t *testing.T) {
	users_testing.CleanupPlans()
	router := createUserPlanTestRouter()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	member := users_testing.CreateTestUser(users_enums.UserRoleMember)

	createRequest := users_testing.CreateValidPlanRequest("Original Plan", users_enums.UserPlanTypePro)
	plan := users_testing.CreateTestPlanViaAPI(t, createRequest, admin.Token, router)

	newName := "Updated Plan"
	updateRequest := users_dto.UpdatePlanRequestDTO{
		Name: &newName,
	}

	resp := test_utils.MakePutRequest(
		t,
		router,
		"/api/v1/plans/"+plan.ID.String(),
		"Bearer "+member.Token,
		updateRequest,
		http.StatusForbidden,
	)

	assert.Contains(t, string(resp.Body), "Insufficient permissions")
}

func Test_DeletePlan_WhenUserIsAdminAndNoUsersUsingIt_PlanDeleted(t *testing.T) {
	users_testing.CleanupPlans()
	router := createUserPlanTestRouter()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	createRequest := users_testing.CreateValidPlanRequest("Plan To Delete", users_enums.UserPlanTypePro)
	plan := users_testing.CreateTestPlanViaAPI(t, createRequest, admin.Token, router)

	resp := test_utils.MakeDeleteRequest(
		t,
		router,
		"/api/v1/plans/"+plan.ID.String(),
		"Bearer "+admin.Token,
		http.StatusOK,
	)

	assert.Contains(t, string(resp.Body), "Plan deleted successfully")
}

func Test_DeletePlan_WhenUsersAreUsingPlan_ReturnsBadRequest(t *testing.T) {
	users_testing.CleanupPlans()
	router := createUserPlanTestRouter()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	createRequest := users_testing.CreateValidPlanRequest("Plan In Use", users_enums.UserPlanTypePro)
	plan := users_testing.CreateTestPlanViaAPI(t, createRequest, admin.Token, router)

	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	users_testing.AssignPlanToUser(user.UserID.String(), plan.ID.String())

	resp := test_utils.MakeDeleteRequest(
		t,
		router,
		"/api/v1/plans/"+plan.ID.String(),
		"Bearer "+admin.Token,
		http.StatusBadRequest,
	)

	assert.Contains(t, string(resp.Body), "cannot delete plan")
}

func Test_DeletePlan_WhenUserIsMember_ReturnsForbidden(t *testing.T) {
	users_testing.CleanupPlans()
	router := createUserPlanTestRouter()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	member := users_testing.CreateTestUser(users_enums.UserRoleMember)

	createRequest := users_testing.CreateValidPlanRequest("Plan To Delete", users_enums.UserPlanTypePro)
	plan := users_testing.CreateTestPlanViaAPI(t, createRequest, admin.Token, router)

	resp := test_utils.MakeDeleteRequest(
		t,
		router,
		"/api/v1/plans/"+plan.ID.String(),
		"Bearer "+member.Token,
		http.StatusForbidden,
	)

	assert.Contains(t, string(resp.Body), "Insufficient permissions")
}

func Test_CreateUnlimitedPlan_PlanCreated(t *testing.T) {
	users_testing.CleanupPlans()
	router := createUserPlanTestRouter()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)

	request := users_dto.CreatePlanRequestDTO{
		Name:                 "Unlimited Plan",
		Type:                 users_enums.UserPlanTypePro,
		LogsPerSecondLimit:   0,
		MaxLogsAmount:        0,
		MaxLogsSizeMB:        0,
		MaxLogsLifeDays:      0,
		MaxLogSizeKB:         0,
		AllowedProjectsCount: 0,
	}

	var plan users_models.UserPlan
	test_utils.MakePostRequestAndUnmarshal(
		t,
		router,
		"/api/v1/plans",
		"Bearer "+admin.Token,
		request,
		http.StatusOK,
		&plan,
	)

	assert.Equal(t, "Unlimited Plan", plan.Name)
	assert.Equal(t, users_enums.UserPlanTypePro, plan.Type)
	assert.Equal(t, 0, plan.LogsPerSecondLimit)
	assert.Equal(t, int64(0), plan.MaxLogsAmount)
	assert.Equal(t, 0, plan.MaxLogsSizeMB)
	assert.Equal(t, 0, plan.MaxLogsLifeDays)
	assert.Equal(t, 0, plan.MaxLogSizeKB)
	assert.Equal(t, 0, plan.AllowedProjectsCount)
}

func createUserPlanTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	v1 := router.Group("/api/v1")

	protected := v1.Group("").Use(users_middleware.AuthMiddleware(users_services.GetUserService()))
	GetUserPlanController().RegisterRoutes(protected.(*gin.RouterGroup))

	users_services.GetUserService().SetAuditLogWriter(&AuditLogWriterStub{})
	users_services.GetUserPlanService().SetAuditLogWriter(&AuditLogWriterStub{})

	return router
}
