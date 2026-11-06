package logs_querying_tests

import (
	"fmt"
	"net/http"
	"sort"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"
	logs_receiving "obzly/internal/features/logs/receiving"
	projects_testing "obzly/internal/features/projects/testing"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_GetQueryableFields_WithFieldOnlyBehindNewerLogs_StillDiscoversIt(t *testing.T) {
	router := CreateLogQueryTestRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	uniqueID := uuid.New().String()
	project, _ := projects_testing.CreateTestProjectWithToken(
		fmt.Sprintf("Field Discovery %s", uniqueID[:8]), owner.Token, router,
	)

	older := logs_receiving.LogItemRequestDTO{
		Level:     logs_core.LogLevelInfo,
		Message:   "payment settled",
		Timestamp: time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339Nano),
		Fields:    map[string]any{"test_id": uniqueID, "payment_provider": "stripe"},
	}
	newer := make([]string, 60)
	for i := range newer {
		newer[i] = fmt.Sprintf("heartbeat %d", i)
	}
	newerItems := CreateLogItemsWithMessages(uniqueID, newer, logs_core.LogLevelInfo, nil)
	for i := range newerItems {
		newerItems[i].Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	logItems := append([]logs_receiving.LogItemRequestDTO{older}, newerItems...)
	SubmitLogsAndProcess(t, router, project.ID, logItems)
	// The wait helper's query pages at 50; one flush writes all 61.
	WaitForLogsToBeIndexed(t, router, project.ID, 50, uniqueID, "Bearer "+owner.Token)

	response := makeGetQueryableFieldsRequest(t, router, project.ID, "", owner.Token, http.StatusOK)
	assert.Contains(t, getFieldNames(response.Fields), "payment_provider")
}

func Test_GetQueryableFields_ReturnsPredefinedFirstThenCustomAlphabetically(t *testing.T) {
	router := CreateLogQueryTestRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	uniqueID := uuid.New().String()
	project, _ := projects_testing.CreateTestProjectWithToken(
		fmt.Sprintf("Field Order %s", uniqueID[:8]), owner.Token, router,
	)

	logItems := createLogsWithCustomFields(uniqueID, map[string]any{"zeta": "1", "alpha": "2", "mid": "3"})
	SubmitLogsAndProcess(t, router, project.ID, logItems)
	WaitForLogsToBeIndexed(t, router, project.ID, len(logItems), uniqueID, "Bearer "+owner.Token)

	response := makeGetQueryableFieldsRequest(t, router, project.ID, "", owner.Token, http.StatusOK)

	predefined := len(logs_core.PredefinedQueryableFields)
	require.Greater(t, len(response.Fields), predefined)
	for i, field := range logs_core.PredefinedQueryableFields {
		assert.Equal(t, field.Name, response.Fields[i].Name)
	}
	custom := make([]string, 0, len(response.Fields)-predefined)
	for _, field := range response.Fields[predefined:] {
		custom = append(custom, field.Name)
	}
	assert.True(t, sort.StringsAreSorted(custom), "custom fields should be A-Z: %v", custom)
}
