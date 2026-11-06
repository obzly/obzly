package logs_querying_tests

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"
	"obzly/internal/storage"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_FieldValues_WithIncludeRecent_ListsValuesOutsideTheWindow(t *testing.T) {
	router, owner, project, uniqueID := SetupBasicQueryTest(t, "recent values")
	now := time.Now().UTC()
	storeSourceLog(t, project.ID, now.Add(-5*time.Hour), uniqueID, "billing", "billing-6c4f9d7b8-qwert")
	storeSourceLog(t, project.ID, now.Add(-40*time.Hour), uniqueID, "reports", "reports-5b8c7d6f9-zxcvb")
	storeSourceLog(t, project.ID, now.Add(-2*time.Minute), uniqueID, "checkout", "checkout-7d9f8b6c5-abcde")
	flushLogs(t)

	request := lastMinutes(now, 15, "service_name", nil)
	plain := getFieldValues(t, router, project.ID, owner.Token, request)
	assert.Equal(t, []string{"checkout"}, valueNames(plain.Values))
	assert.Empty(t, plain.Recent, "without includeRecent the response is as before")

	request.IncludeRecent = true
	response := getFieldValues(t, router, project.ID, owner.Token, request)
	assert.Equal(t, []string{"checkout"}, valueNames(response.Values))
	require.Len(t, response.Recent, 1, "checkout is in values; reports is older than the recent window")
	assert.Equal(t, "billing", response.Recent[0].Value)
	assert.WithinDuration(t, now.Add(-5*time.Hour), response.Recent[0].LastSeen, time.Second)
	assert.False(t, response.Recent[0].InRange)
	assert.False(t, response.RecentSkipped)
}

func Test_FieldValues_WithIncludeRecent_WhenQueryFiltersOnService_NarrowsRecentPods(t *testing.T) {
	router, owner, project, uniqueID := SetupBasicQueryTest(t, "recent pods")
	now := time.Now().UTC()
	storeSourceLog(t, project.ID, now.Add(-3*time.Hour), uniqueID, "billing", "billing-6c4f9d7b8-qwert")
	storeSourceLog(t, project.ID, now.Add(-3*time.Hour), uniqueID, "checkout", "checkout-7d9f8b6c5-abcde")
	flushLogs(t)

	// Level is ignored: sources don't record it.
	request := lastMinutes(now, 15, "k8s_pod_name", BuildLogicalQuery("and",
		*BuildCondition("service_name", "equals", "billing"),
		*BuildCondition("level", "equals", "error"),
	).Query)
	request.IncludeRecent = true
	response := getFieldValues(t, router, project.ID, owner.Token, request)

	assert.Empty(t, response.Values)
	assert.Equal(t, []string{"billing-6c4f9d7b8-qwert"}, recentNames(response.Recent))
}

func Test_FieldValues_WithIncludeRecent_WhenQueryHasFreeText_SkipsRecent(t *testing.T) {
	router, owner, project, uniqueID := SetupBasicQueryTest(t, "recent skipped")
	now := time.Now().UTC()
	storeSourceLog(t, project.ID, now.Add(-3*time.Hour), uniqueID, "billing", "billing-6c4f9d7b8-qwert")
	flushLogs(t)

	request := lastMinutes(now, 15, "service_name", BuildCondition("message", "contains", "timeout"))
	request.IncludeRecent = true
	response := getFieldValues(t, router, project.ID, owner.Token, request)

	assert.Empty(t, response.Recent)
	assert.True(t, response.RecentSkipped)
}

func Test_FieldValues_WithIncludeRecent_WhenValueInRangeIsBeyondTheLimit_ListsItInRange(t *testing.T) {
	router, owner, project, uniqueID := SetupBasicQueryTest(t, "recent in range")
	now := time.Now().UTC()
	storeSourceLog(t, project.ID, now.Add(-2*time.Minute), uniqueID, "checkout", "checkout-7d9f8b6c5-abcde")
	storeSourceLog(t, project.ID, now.Add(-2*time.Minute), uniqueID, "checkout", "checkout-7d9f8b6c5-abcde")
	storeSourceLog(t, project.ID, now.Add(-3*time.Minute), uniqueID, "billing", "billing-6c4f9d7b8-qwert")
	flushLogs(t)

	request := lastMinutes(now, 15, "service_name", nil)
	request.Limit = 1
	request.IncludeRecent = true
	response := getFieldValues(t, router, project.ID, owner.Token, request)

	assert.Equal(t, []string{"checkout"}, valueNames(response.Values))
	require.Len(t, response.Recent, 1)
	assert.Equal(t, "billing", response.Recent[0].Value)
	assert.True(t, response.Recent[0].InRange)
}

func Test_DeleteLogs_PrunesLogSourceIndex(t *testing.T) {
	_, _, project, uniqueID := SetupBasicQueryTest(t, "source pruning")
	now := time.Now().UTC()
	storeSourceLog(t, project.ID, now.Add(-5*time.Hour), uniqueID, "billing", "billing-6c4f9d7b8-qwert")
	storeSourceLog(t, project.ID, now.Add(-time.Minute), uniqueID, "checkout", "checkout-7d9f8b6c5-abcde")
	flushLogs(t)
	require.Equal(t, int64(2), countSources(t, project.ID))

	repository := logs_core.GetLogCoreRepository()
	require.NoError(t, repository.DeleteOldLogs(project.ID, now.Add(-time.Hour)))
	assert.Equal(t, int64(1), countSources(t, project.ID), "the source last seen 5 h ago goes with its logs")

	require.NoError(t, repository.DeleteLogsByProject(project.ID))
	assert.Equal(t, int64(0), countSources(t, project.ID))
}

func storeSourceLog(t *testing.T, projectID uuid.UUID, at time.Time, uniqueID, service, pod string) {
	t.Helper()
	require.NoError(t, logs_core.GetLogCoreRepository().StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{
		projectID: {{
			ID:        uuid.New(),
			ProjectID: projectID,
			Timestamp: at,
			Level:     logs_core.LogLevelInfo,
			Message:   "request handled",
			Fields: map[string]any{
				"test_id":                     uniqueID,
				"resource.service.name":       service,
				"resource.k8s.namespace.name": "shop",
				"resource.k8s.pod.name":       pod,
			},
		}},
	}))
}

func flushLogs(t *testing.T) {
	t.Helper()
	require.NoError(t, logs_core.GetLogCoreRepository().ForceFlush())
}

func lastMinutes(now time.Time, minutes int, field string, query *logs_core.QueryNode) logs_core.FieldValuesRequestDTO {
	from := now.Add(-time.Duration(minutes) * time.Minute)
	to := now
	return logs_core.FieldValuesRequestDTO{
		Field:     field,
		Query:     query,
		TimeRange: &logs_core.TimeRangeDTO{From: &from, To: &to},
		Limit:     200,
	}
}

func getFieldValues(
	t *testing.T,
	router *gin.Engine,
	projectID uuid.UUID,
	token string,
	request logs_core.FieldValuesRequestDTO,
) logs_core.FieldValuesResponseDTO {
	t.Helper()
	var response logs_core.FieldValuesResponseDTO
	test_utils.MakePostRequestAndUnmarshal(t, router,
		fmt.Sprintf("/api/v1/logs/query/field-values/%s", projectID), "Bearer "+token,
		request, http.StatusOK, &response)
	return response
}

func valueNames(values []logs_core.FieldValueDTO) []string {
	names := []string{}
	for _, value := range values {
		names = append(names, value.Value)
	}
	return names
}

func recentNames(values []logs_core.RecentFieldValueDTO) []string {
	names := []string{}
	for _, value := range values {
		names = append(names, value.Value)
	}
	return names
}

func countSources(t *testing.T, projectID uuid.UUID) int64 {
	t.Helper()
	var count int64
	require.NoError(t, storage.GetDb().Raw(
		"SELECT count(*) FROM log_source_index WHERE project_id = ?", projectID).Scan(&count).Error)
	return count
}
