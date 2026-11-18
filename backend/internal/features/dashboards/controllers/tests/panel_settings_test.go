package dashboards_controllers_tests

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"testing"

	dashboards_dto "obzly/internal/features/dashboards/dto"
	dashboards_models "obzly/internal/features/dashboards/models"
	projects_testing "obzly/internal/features/projects/testing"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_CreateDashboard_WithV2KindsAndSettings_RoundTripsThroughGetAndUpdate(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Settings Round Trip", owner.Token, router)

	panels := []dashboards_models.Panel{
		{
			ID: "trend", Title: "Orders", Type: dashboards_models.PanelTypeStatTrend,
			HigherIs: dashboards_models.PanelHigherIsGood, GridPos: dashboards_models.GridPos{W: 3, H: 4},
		},
		{
			ID: "ranked", Title: "Top errors", Type: dashboards_models.PanelTypeRankedList,
			GroupField: "service", Limit: 5, GridPos: dashboards_models.GridPos{W: 6, H: 8},
		},
		{
			ID: "donut", Title: "Levels", Type: dashboards_models.PanelTypeDonut,
			GroupField: "level", GridPos: dashboards_models.GridPos{W: 6, H: 8},
		},
		{
			ID: "gauge", Title: "Budget left", Type: dashboards_models.PanelTypeGauge,
			Source: dashboards_models.PanelSourceMetrics, PromQuery: "up",
			Unit: dashboards_models.PanelUnitPercent, Min: floatPtr(0), Max: floatPtr(1),
			Thresholds: &dashboards_models.PanelThresholds{Warn: floatPtr(0.3), Critical: floatPtr(0.1)},
			HigherIs:   dashboards_models.PanelHigherIsGood,
			GridPos:    dashboards_models.GridPos{W: 4, H: 8},
		},
		{
			ID: "stacked", Title: "Volume", Type: dashboards_models.PanelTypeStackedBar,
			GroupField: "level", BucketMs: 60_000, GridPos: dashboards_models.GridPos{W: 12, H: 8},
		},
		{
			ID: "strip", Title: "Pods ready", Type: dashboards_models.PanelTypeStatusStrip,
			Source: dashboards_models.PanelSourceMetrics, PromQuery: "sum by (pod) (up)",
			Thresholds: &dashboards_models.PanelThresholds{Critical: floatPtr(0)},
			HigherIs:   dashboards_models.PanelHigherIsGood,
			GridPos:    dashboards_models.GridPos{W: 6, H: 8},
		},
	}

	var created dashboards_models.Dashboard
	test_utils.MakePostRequestAndUnmarshal(
		t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s", project.ID.String()),
		"Bearer "+owner.Token,
		dashboards_dto.CreateDashboardRequestDTO{
			Title: "v2 kinds",
			Tabs:  []dashboards_models.Tab{{ID: "tab", Title: "Default", Panels: panels}},
		},
		http.StatusOK, &created,
	)

	var loaded dashboards_models.Dashboard
	test_utils.MakeGetRequestAndUnmarshal(
		t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s/%s", project.ID.String(), created.ID.String()),
		"Bearer "+owner.Token, http.StatusOK, &loaded,
	)
	require.Len(t, loaded.Tabs, 1)
	assert.Equal(t, dashboards_models.Panels(panels), loaded.Tabs[0].Panels)

	var updated dashboards_models.Dashboard
	test_utils.MakePutRequestAndUnmarshal(
		t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s/%s", project.ID.String(), created.ID.String()),
		"Bearer "+owner.Token,
		dashboards_dto.UpdateDashboardRequestDTO{Title: loaded.Title, Tabs: loaded.Tabs},
		http.StatusOK, &updated,
	)
	assert.Equal(t, loaded.Tabs, updated.Tabs)
}

func Test_SaveDashboard_WithPanelsInTheOldShape_StoresAndReturnsTheSameKeys(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Settings Old Shape", owner.Token, router)

	oldStat := map[string]any{
		"id": "stat-1", "title": "Errors", "type": "stat", "source": "metrics",
		"promQuery": "sum(up)", "unit": "percent",
		"gridPos": map[string]any{"x": 0, "y": 0, "w": 3, "h": 4},
	}
	oldPie := map[string]any{
		"id": "pie-1", "title": "By level", "type": "pie", "groupField": "level", "limit": 5,
		"gridPos": map[string]any{"x": 3, "y": 0, "w": 6, "h": 8},
	}
	body := map[string]any{
		"title": "Old shape",
		"tabs":  []any{map[string]any{"id": "tab", "title": "Default", "panels": []any{oldStat, oldPie}}},
	}

	var created dashboards_models.Dashboard
	test_utils.MakePostRequestAndUnmarshal(
		t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s", project.ID.String()),
		"Bearer "+owner.Token, body, http.StatusOK, &created,
	)

	response := test_utils.MakeGetRequest(
		t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s/%s", project.ID.String(), created.ID.String()),
		"Bearer "+owner.Token, http.StatusOK,
	)
	var raw struct {
		Tabs []struct {
			Panels []map[string]any `json:"panels"`
		} `json:"tabs"`
	}
	require.NoError(t, json.Unmarshal(response.Body, &raw))
	require.Len(t, raw.Tabs, 1)
	require.Len(t, raw.Tabs[0].Panels, 2)

	assert.Equal(t, sortedKeys(oldStat), sortedKeys(raw.Tabs[0].Panels[0]))
	assert.Equal(t, sortedKeys(oldPie), sortedKeys(raw.Tabs[0].Panels[1]))
	assert.Equal(t, "stat", raw.Tabs[0].Panels[0]["type"])
}

func Test_SaveDashboard_WithEmptyThresholds_DropsThem(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Settings Empty Thresholds", owner.Token, router)

	panel := dashboards_models.Panel{
		Title: "Disk", Type: dashboards_models.PanelTypeGauge, Max: floatPtr(100),
		Thresholds: &dashboards_models.PanelThresholds{},
		GridPos:    dashboards_models.GridPos{W: 4, H: 8},
	}
	var created dashboards_models.Dashboard
	test_utils.MakePostRequestAndUnmarshal(
		t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s", project.ID.String()),
		"Bearer "+owner.Token,
		dashboards_dto.CreateDashboardRequestDTO{
			Title: "Empty thresholds",
			Tabs:  []dashboards_models.Tab{{Title: "Default", Panels: []dashboards_models.Panel{panel}}},
		},
		http.StatusOK, &created,
	)

	require.Len(t, created.Tabs[0].Panels, 1)
	assert.Nil(t, created.Tabs[0].Panels[0].Thresholds)
	assert.Nil(t, created.Tabs[0].Panels[0].Min)
	require.NotNil(t, created.Tabs[0].Panels[0].Max)
	assert.InDelta(t, 100.0, *created.Tabs[0].Panels[0].Max, 0)
}

func Test_SaveDashboard_WithInvalidPanelSettings_ReturnsBadRequest(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Settings Invalid", owner.Token, router)

	gauge := func() dashboards_models.Panel {
		return dashboards_models.Panel{
			Title: "Gauge", Type: dashboards_models.PanelTypeGauge,
			GridPos: dashboards_models.GridPos{W: 4, H: 8},
		}
	}

	cases := map[string]func(p *dashboards_models.Panel){
		"min equal to max": func(p *dashboards_models.Panel) {
			p.Min, p.Max = floatPtr(10), floatPtr(10)
		},
		"min above max": func(p *dashboards_models.Panel) {
			p.Min, p.Max = floatPtr(10), floatPtr(1)
		},
		"warn above critical when higher is bad": func(p *dashboards_models.Panel) {
			p.HigherIs = dashboards_models.PanelHigherIsBad
			p.Thresholds = &dashboards_models.PanelThresholds{Warn: floatPtr(90), Critical: floatPtr(80)}
		},
		"warn above critical when higher is unset": func(p *dashboards_models.Panel) {
			p.Thresholds = &dashboards_models.PanelThresholds{Warn: floatPtr(90), Critical: floatPtr(80)}
		},
		"warn below critical when higher is good": func(p *dashboards_models.Panel) {
			p.HigherIs = dashboards_models.PanelHigherIsGood
			p.Thresholds = &dashboards_models.PanelThresholds{Warn: floatPtr(0.1), Critical: floatPtr(0.3)}
		},
		"unknown higherIs": func(p *dashboards_models.Panel) {
			p.HigherIs = "sideways"
		},
		"min/max on a line panel": func(p *dashboards_models.Panel) {
			p.Type = dashboards_models.PanelTypeLine
			p.Max = floatPtr(100)
		},
		"thresholds on a stat with trend": func(p *dashboards_models.Panel) {
			p.Type = dashboards_models.PanelTypeStatTrend
			p.Thresholds = &dashboards_models.PanelThresholds{Warn: floatPtr(1)}
		},
		"min/max on a status strip": func(p *dashboards_models.Panel) {
			p.Type = dashboards_models.PanelTypeStatusStrip
			p.Min = floatPtr(0)
		},
		"higherIs on an old stat panel": func(p *dashboards_models.Panel) {
			p.Type = dashboards_models.PanelTypeStat
			p.HigherIs = dashboards_models.PanelHigherIsGood
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			panel := gauge()
			mutate(&panel)
			test_utils.MakePostRequest(
				t, router,
				fmt.Sprintf("/api/v1/projects/dashboards/%s", project.ID.String()),
				"Bearer "+owner.Token,
				dashboards_dto.CreateDashboardRequestDTO{
					Title: "Invalid settings",
					Tabs:  []dashboards_models.Tab{{Title: "Default", Panels: []dashboards_models.Panel{panel}}},
				},
				http.StatusBadRequest,
			)
		})
	}
}

func Test_ImportDashboard_GrafanaV2Kinds_MapsTypesAndSettings(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Import Grafana v2", owner.Token, router)

	grafanaJSON, err := json.Marshal(map[string]any{
		"title": "Grafana v2 kinds",
		"panels": []map[string]any{
			{
				"title": "Disk used", "type": "gauge",
				"fieldConfig": map[string]any{"defaults": map[string]any{
					"unit": "percentunit", "min": 0, "max": 1,
					"thresholds": map[string]any{"mode": "absolute", "steps": []any{
						map[string]any{"color": "green", "value": nil},
						map[string]any{"color": "orange", "value": 0.8},
						map[string]any{"color": "red", "value": 0.9},
					}},
				}},
			},
			{
				"title": "Budget left", "type": "gauge",
				"fieldConfig": map[string]any{"defaults": map[string]any{
					"thresholds": map[string]any{"mode": "absolute", "steps": []any{
						map[string]any{"color": "red", "value": nil},
						map[string]any{"color": "yellow", "value": 10},
						map[string]any{"color": "green", "value": 30},
					}},
				}},
			},
			{
				"title": "Bad range", "type": "gauge",
				"fieldConfig": map[string]any{"defaults": map[string]any{"min": 100, "max": 10}},
			},
			{"title": "Top hosts", "type": "bargauge"},
			{"title": "Levels", "type": "piechart", "options": map[string]any{"pieType": "donut"}},
			{"title": "Plain pie", "type": "piechart", "options": map[string]any{"pieType": "pie"}},
			{"title": "Requests", "type": "stat", "options": map[string]any{"graphMode": "area"}},
			{"title": "Plain stat", "type": "stat", "options": map[string]any{"graphMode": "none"}},
			{"title": "Nodes", "type": "status-history"},
		},
	})
	require.NoError(t, err)

	var response dashboards_dto.ImportDashboardResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s/import", project.ID.String()),
		"Bearer "+owner.Token,
		dashboards_dto.ImportDashboardRequestDTO{Format: "grafana", JSON: string(grafanaJSON)},
		http.StatusOK, &response,
	)

	require.NotNil(t, response.Dashboard)
	panels := response.Dashboard.Tabs[0].Panels
	require.Len(t, panels, 9)

	disk := panels[0]
	assert.Equal(t, dashboards_models.PanelTypeGauge, disk.Type)
	assert.Equal(t, dashboards_models.PanelUnitPercent, disk.Unit)
	require.NotNil(t, disk.Min)
	require.NotNil(t, disk.Max)
	assert.InDelta(t, 0.0, *disk.Min, 0)
	assert.InDelta(t, 1.0, *disk.Max, 0)
	require.NotNil(t, disk.Thresholds)
	assert.InDelta(t, 0.8, *disk.Thresholds.Warn, 0)
	assert.InDelta(t, 0.9, *disk.Thresholds.Critical, 0)
	assert.Equal(t, dashboards_models.PanelHigherIsUnset, disk.HigherIs)

	budget := panels[1]
	require.NotNil(t, budget.Thresholds)
	assert.InDelta(t, 30.0, *budget.Thresholds.Warn, 0)
	assert.InDelta(t, 10.0, *budget.Thresholds.Critical, 0)
	assert.Equal(t, dashboards_models.PanelHigherIsGood, budget.HigherIs)

	badRange := panels[2]
	assert.Equal(t, dashboards_models.PanelTypeGauge, badRange.Type)
	assert.Nil(t, badRange.Min, "an invalid range is dropped, not imported")
	assert.Nil(t, badRange.Max)

	assert.Equal(t, dashboards_models.PanelTypeRankedList, panels[3].Type)
	assert.Equal(t, dashboards_models.PanelTypeDonut, panels[4].Type)
	assert.Equal(t, dashboards_models.PanelTypePie, panels[5].Type)
	assert.Equal(t, dashboards_models.PanelTypeStatTrend, panels[6].Type)
	assert.Equal(t, dashboards_models.PanelTypeStat, panels[7].Type)
	assert.Equal(t, dashboards_models.PanelTypeStatusStrip, panels[8].Type)
}

func Test_ImportDashboard_OpenObserveV2Kinds_MapsTypesAndGaugeRange(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Import OO v2", owner.Token, router)

	openObserveJSON, err := json.Marshal(map[string]any{
		"title": "OpenObserve v2 kinds",
		"panels": []map[string]any{
			{
				"title": "Memory", "type": "gauge",
				"config":  map[string]any{"unit": "bytes"},
				"queries": []any{map[string]any{"config": map[string]any{"min": 0, "max": 4096}}},
			},
			{"title": "Levels", "type": "donut"},
			{"title": "Volume", "type": "stacked"},
			{"title": "Count", "type": "metric"},
		},
	})
	require.NoError(t, err)

	var response dashboards_dto.ImportDashboardResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s/import", project.ID.String()),
		"Bearer "+owner.Token,
		dashboards_dto.ImportDashboardRequestDTO{Format: "openobserve", JSON: string(openObserveJSON)},
		http.StatusOK, &response,
	)

	require.NotNil(t, response.Dashboard)
	panels := response.Dashboard.Tabs[0].Panels
	require.Len(t, panels, 4)
	assert.Equal(t, dashboards_models.PanelTypeGauge, panels[0].Type)
	assert.Equal(t, dashboards_models.PanelUnitBytes, panels[0].Unit)
	require.NotNil(t, panels[0].Max)
	assert.InDelta(t, 4096.0, *panels[0].Max, 0)
	assert.Equal(t, dashboards_models.PanelTypeDonut, panels[1].Type)
	assert.Equal(t, dashboards_models.PanelTypeStackedBar, panels[2].Type)
	assert.Equal(t, dashboards_models.PanelTypeStat, panels[3].Type)
}

func floatPtr(value float64) *float64 {
	return &value
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
