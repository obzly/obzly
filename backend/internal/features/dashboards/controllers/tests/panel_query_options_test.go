package dashboards_controllers_tests

import (
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	dashboards_dto "obzly/internal/features/dashboards/dto"
	dashboards_models "obzly/internal/features/dashboards/models"
	logs_core "obzly/internal/features/logs/core"
	logs_querying_tests "obzly/internal/features/logs/querying/tests"
	logs_receiving "obzly/internal/features/logs/receiving"
	projects_testing "obzly/internal/features/projects/testing"
	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_ExecutePanelQuery_AvgMeasureGroupedByService_ReturnsAveragePerGroup(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner, projectID := setUpProjectWithDurations(t, router, "Panel Avg")

	response := queryPanel(t, router, owner, projectID, dashboards_models.Panel{
		Type:         dashboards_models.PanelTypeRankedList,
		GroupField:   "service_name",
		Measure:      logs_core.AggregationMeasureAvg,
		MeasureField: "duration_ms",
	}, http.StatusOK)

	values := map[string]float64{}
	for _, bucket := range response.Buckets {
		values[bucket.GroupKey] = bucket.Value
	}
	assert.InDelta(t, 200, values["checkout"], 0.001)
	assert.InDelta(t, 40, values["search"], 0.001)
}

func Test_ExecutePanelQuery_TopGroupsOnALineChart_KeepsTheLargestGroup(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner, projectID := setUpProjectWithDurations(t, router, "Panel Top Groups")

	response := queryPanel(t, router, owner, projectID, dashboards_models.Panel{
		Type:       dashboards_models.PanelTypeLine,
		GroupField: "service_name",
		BucketMs:   time.Minute.Milliseconds(),
		TopGroups:  1,
	}, http.StatusOK)

	require.NotEmpty(t, response.Buckets)
	total := 0.0
	for _, bucket := range response.Buckets {
		assert.Equal(t, "checkout", bucket.GroupKey)
		assert.NotNil(t, bucket.Timestamp)
		total += bucket.Value
	}
	assert.Equal(t, float64(3), total)
}

func Test_ExecutePanelQuery_LogBandsOverTime_ReturnsBandsPerBucket(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner, projectID := setUpProjectWithDurations(t, router, "Panel Bands")

	response := queryPanel(t, router, owner, projectID, dashboards_models.Panel{
		Type:       dashboards_models.PanelTypeLine,
		GroupField: "duration_ms",
		GroupBands: logs_core.GroupBandsLog,
		BucketMs:   time.Hour.Milliseconds(),
	}, http.StatusOK)

	counts := map[float64]float64{}
	for _, bucket := range response.Buckets {
		band, err := strconv.ParseFloat(bucket.GroupKey, 64)
		require.NoError(t, err)
		counts[band] += bucket.Value
	}
	// 100, 200, 300 (checkout) and 20, 60 (search).
	assert.Equal(t, map[float64]float64{20: 1, 50: 1, 100: 1, 200: 2}, counts)
}

func Test_ExecutePanelQuery_MeasureWithoutField_ReturnsBadRequest(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner, projectID := setUpProjectWithDurations(t, router, "Panel Bad Measure")

	queryPanel(t, router, owner, projectID, dashboards_models.Panel{
		Type:    dashboards_models.PanelTypeStat,
		Measure: logs_core.AggregationMeasureP95,
	}, http.StatusBadRequest)
}

func Test_ExecutePanelQuery_WithMeasure_NonMember_ReturnsForbidden(t *testing.T) {
	router := CreateDashboardTestRouter()
	_, projectID := setUpProjectWithDurations(t, router, "Panel Measure Outsider")
	outsider := users_testing.CreateTestUser(users_enums.UserRoleMember)

	test_utils.MakePostRequest(
		t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s/query", projectID),
		"Bearer "+outsider.Token,
		dashboards_dto.PanelQueryRequestDTO{
			Panel: dashboards_models.Panel{
				Type: dashboards_models.PanelTypeStat, Measure: logs_core.AggregationMeasureAvg,
				MeasureField: "duration_ms",
			},
			TimeRange: timeRangeCoveringNow(),
		},
		http.StatusForbidden,
	)
}

func Test_CreateDashboard_WithMeasurePanel_KeepsTheMeasure(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Measure Panel Save", owner.Token, router)

	panel := samplePanel()
	panel.Type = dashboards_models.PanelTypeLine
	panel.GroupField = "service_name"
	panel.Measure = logs_core.AggregationMeasureP95
	panel.MeasureField = "duration_ms"
	panel.TopGroups = 5
	var response dashboards_models.Dashboard
	test_utils.MakePostRequestAndUnmarshal(
		t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s", project.ID.String()),
		"Bearer "+owner.Token,
		dashboards_dto.CreateDashboardRequestDTO{
			Title: "Measures",
			Tabs:  []dashboards_models.Tab{{Title: "Default", Panels: []dashboards_models.Panel{panel}}},
		},
		http.StatusOK, &response,
	)

	require.Len(t, response.Tabs, 1)
	require.Len(t, response.Tabs[0].Panels, 1)
	saved := response.Tabs[0].Panels[0]
	assert.Equal(t, logs_core.AggregationMeasureP95, saved.Measure)
	assert.Equal(t, "duration_ms", saved.MeasureField)
	assert.Equal(t, 5, saved.TopGroups)
}

func Test_CreateDashboard_WithInvalidLogOptions_ReturnsBadRequest(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Measure Panel Invalid", owner.Token, router)

	cases := map[string]func(*dashboards_models.Panel){
		"log bands": func(p *dashboards_models.Panel) {
			p.GroupField, p.GroupBands = "duration_ms", logs_core.GroupBandsLog
		},
		"measure on a spans panel": func(p *dashboards_models.Panel) {
			p.Source, p.Measure, p.MeasureField = dashboards_models.PanelSourceSpans, "avg", "duration_ms"
		},
		"unknown measure":            func(p *dashboards_models.Panel) { p.Measure, p.MeasureField = "mode", "x" },
		"top groups without a group": func(p *dashboards_models.Panel) { p.GroupField, p.TopGroups = "", 3 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			panel := samplePanel()
			mutate(&panel)
			test_utils.MakePostRequest(
				t, router,
				fmt.Sprintf("/api/v1/projects/dashboards/%s", project.ID.String()),
				"Bearer "+owner.Token,
				dashboards_dto.CreateDashboardRequestDTO{
					Title: "Bad options",
					Tabs:  []dashboards_models.Tab{{Title: "Default", Panels: []dashboards_models.Panel{panel}}},
				},
				http.StatusBadRequest,
			)
		})
	}
}

// setUpProjectWithDurations seeds checkout lines taking 100, 200 and 300 ms
// and search lines taking 20 and 60 ms, plus one search line without a duration.
func setUpProjectWithDurations(t *testing.T, router *gin.Engine, testName string) (
	owner *users_dto.SignInResponseDTO, projectID string,
) {
	t.Helper()
	owner = users_testing.CreateTestUser(users_enums.UserRoleMember)
	uniqueID := uuid.New().String()
	project, _ := projects_testing.CreateTestProjectWithToken(
		fmt.Sprintf("%s %s", testName, uniqueID[:8]), owner.Token, router)

	var items []logs_receiving.LogItemRequestDTO
	for _, ms := range []int{100, 200, 300} {
		items = append(items, logs_querying_tests.CreateLogItemsWithMessages(uniqueID, []string{"charge"},
			logs_core.LogLevelInfo, map[string]any{"service_name": "checkout", "duration_ms": ms})...)
	}
	for _, ms := range []int{20, 60} {
		items = append(items, logs_querying_tests.CreateLogItemsWithMessages(uniqueID, []string{"lookup"},
			logs_core.LogLevelInfo, map[string]any{"service_name": "search", "duration_ms": ms})...)
	}
	items = append(items, logs_querying_tests.CreateLogItemsWithMessages(uniqueID, []string{"no duration"},
		logs_core.LogLevelInfo, map[string]any{"service_name": "search"})...)
	logs_querying_tests.SubmitLogsAndProcess(t, router, project.ID, items)
	require.NoError(t, logs_core.GetLogCoreRepository().ForceFlush())
	return owner, project.ID.String()
}

func queryPanel(
	t *testing.T,
	router *gin.Engine,
	owner *users_dto.SignInResponseDTO,
	projectID string,
	panel dashboards_models.Panel,
	expectedStatus int,
) dashboards_dto.PanelQueryResponseDTO {
	t.Helper()
	request := dashboards_dto.PanelQueryRequestDTO{Panel: panel, TimeRange: timeRangeCoveringNow()}
	var response dashboards_dto.PanelQueryResponseDTO
	if expectedStatus != http.StatusOK {
		test_utils.MakePostRequest(t, router, fmt.Sprintf("/api/v1/projects/dashboards/%s/query", projectID),
			"Bearer "+owner.Token, request, expectedStatus)
		return response
	}
	test_utils.MakePostRequestAndUnmarshal(t, router, fmt.Sprintf("/api/v1/projects/dashboards/%s/query", projectID),
		"Bearer "+owner.Token, request, http.StatusOK, &response)
	return response
}
