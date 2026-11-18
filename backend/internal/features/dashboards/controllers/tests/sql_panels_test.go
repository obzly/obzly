package dashboards_controllers_tests

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	dashboards_dto "obzly/internal/features/dashboards/dto"
	dashboards_models "obzly/internal/features/dashboards/models"
	logs_core "obzly/internal/features/logs/core"
	projects_testing "obzly/internal/features/projects/testing"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const levelCountsSQL = "SELECT lower(level) AS level, count(*) AS value FROM logs GROUP BY 1 ORDER BY 1"

func sqlPanel(panelType dashboards_models.PanelType, sql string) dashboards_models.Panel {
	return dashboards_models.Panel{
		ID: "sql", Title: "Levels", Type: panelType, Source: dashboards_models.PanelSourceLogsSQL, SQL: sql,
		GridPos: dashboards_models.GridPos{W: 6, H: 8},
	}
}

func Test_CreateDashboard_WithLogsSQLPanel_SavesTrimmedSQLAndDropsOtherQueryFields(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("SQL Panel Save", owner.Token, router)

	panel := sqlPanel(dashboards_models.PanelTypeRankedList, "  "+levelCountsSQL+"  ")
	panel.GroupField, panel.PromQuery, panel.BucketMs = "level", "up", 60_000

	var created dashboards_models.Dashboard
	test_utils.MakePostRequestAndUnmarshal(t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s", project.ID.String()), "Bearer "+owner.Token,
		dashboards_dto.CreateDashboardRequestDTO{
			Title: "SQL", Tabs: []dashboards_models.Tab{{ID: "tab", Panels: []dashboards_models.Panel{panel}}},
		}, http.StatusOK, &created)

	saved := created.Tabs[0].Panels[0]
	assert.Equal(t, dashboards_models.PanelSourceLogsSQL, saved.Source)
	assert.Equal(t, levelCountsSQL, saved.SQL)
	assert.Empty(t, saved.GroupField)
	assert.Empty(t, saved.PromQuery)
	assert.Zero(t, saved.BucketMs)
}

func Test_CreateDashboard_WithInvalidLogsSQLPanel_ReturnsBadRequest(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("SQL Panel Invalid", owner.Token, router)

	sqlOnLogsPanel := sqlPanel(dashboards_models.PanelTypeTable, levelCountsSQL)
	sqlOnLogsPanel.Source = dashboards_models.PanelSourceLogs

	cases := map[string]struct {
		panel dashboards_models.Panel
		error string
	}{
		"empty": {sqlPanel(dashboards_models.PanelTypeTable, "  "), "a SQL query is required"},
		"too long": {
			sqlPanel(dashboards_models.PanelTypeTable, "SELECT '"+strings.Repeat("x", 10_000)+"'"),
			"character limit",
		},
		"not a select": {sqlPanel(dashboards_models.PanelTypeTable, "DELETE FROM logs"), "SELECT"},
		"reads a file": {
			sqlPanel(dashboards_models.PanelTypeTable, "SELECT * FROM read_parquet('/etc/passwd')"),
			"disallowed function",
		},
		"reads a setting": {
			sqlPanel(dashboards_models.PanelTypeStat, "SELECT current_setting('memory_limit')"),
			"disallowed function",
		},
		"introspects duckdb": {
			sqlPanel(dashboards_models.PanelTypeTable, "SELECT * FROM duckdb_settings()"),
			"disallowed function",
		},
		"two statements": {
			sqlPanel(dashboards_models.PanelTypeTable, "SELECT 1; SELECT 2"), "single SQL statement",
		},
		"unsupported type":    {sqlPanel(dashboards_models.PanelTypeGauge, levelCountsSQL), "logs_sql panel is"},
		"sql on a logs panel": {sqlOnLogsPanel, "sql is a setting of a logs_sql panel"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			response := test_utils.MakeRequest(t, router, test_utils.RequestOptions{
				Method: http.MethodPost, URL: fmt.Sprintf("/api/v1/projects/dashboards/%s", project.ID.String()),
				Body: dashboards_dto.CreateDashboardRequestDTO{
					Title: "SQL", Tabs: []dashboards_models.Tab{{Panels: []dashboards_models.Panel{tc.panel}}},
				},
				AuthToken: "Bearer " + owner.Token, ExpectedStatus: http.StatusBadRequest,
			})
			assert.Contains(t, string(response.Body), tc.error)
		})
	}
}

func Test_ExecutePanelQuery_LogsSQLPanel_ReturnsColumnsAndRows(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner, project, _ := SetUpProjectWithLogs(t, router, "SQL Panel Query")

	var response dashboards_dto.PanelQueryResponseDTO
	test_utils.MakePostRequestAndUnmarshal(t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s/query", project.ID.String()), "Bearer "+owner.Token,
		dashboards_dto.PanelQueryRequestDTO{
			Panel: sqlPanel(dashboards_models.PanelTypeDonut, levelCountsSQL), TimeRange: timeRangeCoveringNow(),
		}, http.StatusOK, &response)

	assert.Empty(t, response.Buckets)
	require.NotNil(t, response.Table)
	assert.Equal(t, []string{"level", "value"}, response.Table.Columns)
	// 2 error and 3 info lines seeded by SetUpProjectWithLogs.
	assert.Equal(t, [][]any{{"error", float64(2)}, {"info", float64(3)}}, response.Table.Rows)
	assert.False(t, response.Table.Truncated)
}

func Test_ExecutePanelQuery_LogsSQLPanelOutsideTheWindow_ReturnsNoRows(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner, project, _ := SetUpProjectWithLogs(t, router, "SQL Panel Window")
	from, to := time.Now().UTC().Add(-48*time.Hour), time.Now().UTC().Add(-47*time.Hour)
	window := &logs_core.TimeRangeDTO{From: &from, To: &to}

	var response dashboards_dto.PanelQueryResponseDTO
	test_utils.MakePostRequestAndUnmarshal(t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s/query", project.ID.String()), "Bearer "+owner.Token,
		dashboards_dto.PanelQueryRequestDTO{
			Panel: sqlPanel(dashboards_models.PanelTypeTable, levelCountsSQL), TimeRange: window,
		}, http.StatusOK, &response)

	require.NotNil(t, response.Table)
	assert.Empty(t, response.Table.Rows)
}

func Test_ExecutePanelQuery_LogsSQLPanelWithRefusedFunction_ReturnsBadRequest(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner, project, _ := SetUpProjectWithLogs(t, router, "SQL Panel Refused")

	response := test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method: http.MethodPost, URL: fmt.Sprintf("/api/v1/projects/dashboards/%s/query", project.ID.String()),
		Body: dashboards_dto.PanelQueryRequestDTO{
			Panel:     sqlPanel(dashboards_models.PanelTypeTable, "SELECT * FROM query('SELECT 1')"),
			TimeRange: timeRangeCoveringNow(),
		},
		AuthToken: "Bearer " + owner.Token, ExpectedStatus: http.StatusBadRequest,
	})
	assert.Contains(t, string(response.Body), "disallowed function")
}

func Test_StreamPanelQuery_LogsSQLPanel_SendsTheTableThenDone(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner, project, _ := SetUpProjectWithLogs(t, router, "SQL Panel Stream")

	response := test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method: http.MethodPost, URL: fmt.Sprintf("/api/v1/projects/dashboards/%s/query/stream", project.ID.String()),
		Body: dashboards_dto.PanelQueryRequestDTO{
			Panel:     sqlPanel(dashboards_models.PanelTypeStat, "SELECT count(*) AS lines FROM logs"),
			TimeRange: timeRangeCoveringNow(),
		},
		AuthToken: "Bearer " + owner.Token, ExpectedStatus: http.StatusOK,
	})

	frames := streamFrames(t, response.Body)
	require.Len(t, frames, 2)
	require.NotNil(t, frames[0].Table)
	assert.Equal(t, []string{"lines"}, frames[0].Table.Columns)
	assert.Equal(t, [][]any{{float64(5)}}, frames[0].Table.Rows)
	assert.Equal(t, dashboards_dto.PanelFrameDone, frames[1].Type)
}

func Test_StreamPanelQuery_LogsSQLPanelThatFailsToRun_SendsAnErrorFrame(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner, project, _ := SetUpProjectWithLogs(t, router, "SQL Panel Stream Error")

	response := test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method: http.MethodPost, URL: fmt.Sprintf("/api/v1/projects/dashboards/%s/query/stream", project.ID.String()),
		Body: dashboards_dto.PanelQueryRequestDTO{
			Panel:     sqlPanel(dashboards_models.PanelTypeTable, "SELECT no_such_column FROM logs"),
			TimeRange: timeRangeCoveringNow(),
		},
		AuthToken: "Bearer " + owner.Token, ExpectedStatus: http.StatusOK,
	})

	frames := streamFrames(t, response.Body)
	last := frames[len(frames)-1]
	assert.Equal(t, dashboards_dto.PanelFrameError, last.Type)
	assert.Equal(t, http.StatusBadRequest, last.Status)
	assert.Contains(t, last.Error, "no_such_column")
}
