package logs_receiving_tests

import (
	"fmt"
	"net/http"
	"testing"

	api_keys "obzly/internal/features/api_keys"
	"obzly/internal/features/ingestion"
	logs_receiving "obzly/internal/features/logs/receiving"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

// The receiving endpoint is open and takes the project id from the URL: a
// caller the project doesn't accept must not be able to add to its refused
// counts on Ingestion status.
func Test_SubmitLogs_WithoutApiKeyOnProtectedProject_CountsNoRefusal(t *testing.T) {
	users_testing.CleanupPlans()
	testData := setupApiKeyTest("Refusals No Key", true)

	status := submitLogsForRefusals(t, testData.Router, testData.Project.ID, "", 50, testData.UniqueID)

	assert.Equal(t, http.StatusUnauthorized, status)
	assert.Zero(t, ingestion.GetTracker().PendingRefused(testData.Project.ID))
}

func Test_SubmitLogs_WithInvalidApiKey_CountsNoRefusal(t *testing.T) {
	users_testing.CleanupPlans()
	testData := setupApiKeyTest("Refusals Invalid Key", true)

	status := submitLogsForRefusals(t, testData.Router, testData.Project.ID, generateInvalidApiKeyToken(), 50,
		testData.UniqueID)

	assert.Equal(t, http.StatusUnauthorized, status)
	assert.Zero(t, ingestion.GetTracker().PendingRefused(testData.Project.ID))
}

// The size check runs before the project is looked up, so an oversized batch
// used to be counted for whoever sent it.
func Test_SubmitLogs_OversizedBatchWithoutApiKey_CountsNoRefusal(t *testing.T) {
	users_testing.CleanupPlans()
	testData := setupApiKeyTest("Refusals Oversized No Key", true)

	status := submitLogsForRefusals(t, testData.Router, testData.Project.ID, "", logs_receiving.MaxBatchSize+1,
		testData.UniqueID)

	assert.Equal(t, http.StatusBadRequest, status)
	assert.Zero(t, ingestion.GetTracker().PendingRefused(testData.Project.ID))
}

// Each unknown project took a slot of the tracker's bounded map until the
// next flush; filling it stopped every project's refusals being counted.
func Test_SubmitLogs_ToUnknownProject_TracksNoRefusal(t *testing.T) {
	router := CreateLogsTestRouter()
	unknownProjectID := uuid.New()

	status := submitLogsForRefusals(t, router, unknownProjectID, "", 10, uuid.NewString())

	assert.GreaterOrEqual(t, status, http.StatusBadRequest)
	assert.Zero(t, ingestion.GetTracker().PendingRefused(unknownProjectID))
}

func Test_SubmitLogs_OversizedBatchWithValidApiKey_CountsRefusalCappedAtMaxBatchSize(t *testing.T) {
	users_testing.CleanupPlans()
	testData := setupApiKeyTest("Refusals Oversized Valid Key", true)
	apiKey := api_keys.CreateTestApiKey("Refusals Key", testData.Project.ID, testData.User.Token, testData.Router)

	status := submitLogsForRefusals(t, testData.Router, testData.Project.ID, apiKey.Token,
		logs_receiving.MaxBatchSize+500, testData.UniqueID)

	assert.Equal(t, http.StatusBadRequest, status)
	assert.EqualValues(t, logs_receiving.MaxBatchSize, ingestion.GetTracker().PendingRefused(testData.Project.ID))
}

func Test_SubmitLogs_RateLimitedBatchFromAcceptedCaller_CountsRefusal(t *testing.T) {
	users_testing.CleanupPlans()
	testData := setupRateLimitTest("Refusals Rate Limited", 5) // 5 per second, burst 25

	// A full bucket admits 30 and then owes 5: the next 3 are refused.
	assert.Equal(t, http.StatusAccepted,
		submitLogsForRefusals(t, testData.Router, testData.Project.ID, "", 30, testData.UniqueID+"_big"))
	status := submitLogsForRefusals(t, testData.Router, testData.Project.ID, "", 3, testData.UniqueID+"_next")

	assert.Equal(t, http.StatusTooManyRequests, status)
	assert.EqualValues(t, 3, ingestion.GetTracker().PendingRefused(testData.Project.ID))
}

func submitLogsForRefusals(
	t *testing.T,
	router *gin.Engine,
	projectID uuid.UUID,
	apiKeyToken string,
	count int,
	uniqueID string,
) int {
	headers := map[string]string{}
	if apiKeyToken != "" {
		headers["X-API-Key"] = apiKeyToken
	}
	return test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method:  "POST",
		URL:     fmt.Sprintf("/api/v1/logs/receiving/%s", projectID.String()),
		Body:    &logs_receiving.SubmitLogsRequestDTO{Logs: CreateValidLogItems(count, uniqueID)},
		Headers: headers,
	}).StatusCode
}
