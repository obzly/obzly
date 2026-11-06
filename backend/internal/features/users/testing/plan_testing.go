package users_testing

import (
	"net/http"
	"testing"

	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"
	"obzly/internal/storage"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
)

func CleanupPlans() {
	db := storage.GetDb()

	if err := db.Exec("UPDATE users SET plan_id = NULL WHERE plan_id IS NOT NULL").Error; err != nil {
		panic(err)
	}

	if err := db.Exec("UPDATE projects SET plan_id = NULL WHERE plan_id IS NOT NULL").Error; err != nil {
		panic(err)
	}

	if err := db.Exec("DELETE FROM user_plans").Error; err != nil {
		panic(err)
	}
}

func CreateTestPlanViaAPI(
	t *testing.T,
	request users_dto.CreatePlanRequestDTO,
	adminToken string,
	router *gin.Engine,
) *users_models.UserPlan {
	var plan users_models.UserPlan
	test_utils.MakePostRequestAndUnmarshal(
		t,
		router,
		"/api/v1/plans",
		"Bearer "+adminToken,
		request,
		http.StatusOK,
		&plan,
	)
	return &plan
}

func CreateValidPlanRequest(name string, planType users_enums.UserPlanType) users_dto.CreatePlanRequestDTO {
	return users_dto.CreatePlanRequestDTO{
		Name:                 name,
		Type:                 planType,
		IsPublic:             true,
		WarningText:          "Test warning",
		UpgradeText:          "Test upgrade text",
		LogsPerSecondLimit:   100,
		MaxLogsAmount:        1000000,
		MaxLogsSizeMB:        1024,
		MaxLogsLifeDays:      30,
		MaxLogSizeKB:         256,
		AllowedProjectsCount: 5,
	}
}

func AssignPlanToUser(userID, planID string) {
	db := storage.GetDb()
	if err := db.Exec("UPDATE users SET plan_id = ? WHERE id = ?", planID, userID).Error; err != nil {
		panic(err)
	}
}
