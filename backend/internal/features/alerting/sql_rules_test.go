package alerting_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"obzly/internal/features/alerting"
	logs_core "obzly/internal/features/logs/core"
	projects_controllers "obzly/internal/features/projects/controllers"
	projects_testing "obzly/internal/features/projects/testing"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const errorsByServiceSQL = "SELECT json_extract_string(fields, '$.service_name') AS service, count(*) AS value " +
	"FROM logs WHERE lower(level) = 'error' GROUP BY 1"

func Test_CreateRule_WithLogsSQLSource_SavesTrimmedSQLOnly(t *testing.T) {
	router := projects_testing.CreateTestRouter(projects_controllers.GetProjectController(), alerting.GetController())
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("SQL rule", owner.Token, router)

	var created alerting.AlertRule
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/projects/"+project.ID.String()+"/alert-rules",
		"Bearer "+owner.Token, alerting.RuleRequest{
			Name: "Errors by service", Source: alerting.SourceLogsSQL, Comparison: ">", Threshold: 10,
			Query: alerting.RuleQuery{SQL: "\n" + errorsByServiceSQL + "  ", PromQL: "up", GroupBy: "level"},
		}, http.StatusOK, &created)

	assert.Equal(t, alerting.SourceLogsSQL, created.Source)
	assert.Equal(t, alerting.RuleQuery{SQL: errorsByServiceSQL}, created.Query)
}

func Test_CreateRule_WithInvalidLogsSQL_ReturnsBadRequest(t *testing.T) {
	router := projects_testing.CreateTestRouter(projects_controllers.GetProjectController(), alerting.GetController())
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("SQL rule invalid", owner.Token, router)

	cases := map[string]struct {
		sql   string
		error string
	}{
		"empty":           {"   ", "a SQL query is required"},
		"too long":        {"SELECT '" + strings.Repeat("x", 10_000) + "'", "character limit"},
		"not a select":    {"DROP TABLE logs", "SELECT"},
		"reads a file":    {"SELECT * FROM read_csv('/etc/hosts')", "disallowed function"},
		"reads a setting": {"SELECT current_setting('threads') AS value", "disallowed function"},
		"runs a string":   {"SELECT * FROM query_table('logs')", "disallowed function"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			response := test_utils.MakePostRequest(t, router,
				"/api/v1/projects/"+project.ID.String()+"/alert-rules", "Bearer "+owner.Token,
				alerting.RuleRequest{
					Name: "x", Source: alerting.SourceLogsSQL, Comparison: ">", Query: alerting.RuleQuery{SQL: tc.sql},
				}, http.StatusBadRequest)
			assert.Contains(t, string(response.Body), tc.error)
		})
	}
}

func Test_PreviewRule_WithLogsSQLSource_ReturnsOneSeriesPerRow(t *testing.T) {
	router := projects_testing.CreateTestRouter(projects_controllers.GetProjectController(), alerting.GetController())
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("SQL rule preview", owner.Token, router)
	now := time.Now().UTC()
	var logs []*logs_core.LogItem
	for i, service := range []string{"checkout", "checkout", "checkout", "search"} {
		logs = append(logs, &logs_core.LogItem{
			ID: uuid.New(), ProjectID: project.ID, Timestamp: now.Add(-time.Duration(i+1) * time.Second),
			Level: logs_core.LogLevelError, Message: "boom", Fields: map[string]any{"service_name": service},
		})
	}
	require.NoError(t,
		logs_core.GetLogCoreRepository().StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{project.ID: logs}))
	require.NoError(t, logs_core.GetLogCoreRepository().ForceFlush())

	var preview []alerting.PreviewSeries
	test_utils.MakePostRequestAndUnmarshal(t, router,
		"/api/v1/projects/"+project.ID.String()+"/alert-rules/preview", "Bearer "+owner.Token,
		alerting.RuleRequest{
			Name: "Errors by service", Source: alerting.SourceLogsSQL, Comparison: ">", Threshold: 2,
			Query: alerting.RuleQuery{SQL: errorsByServiceSQL},
		}, http.StatusOK, &preview)

	byService := map[string]alerting.PreviewSeries{}
	for _, series := range preview {
		byService[series.Labels["service"]] = series
	}
	require.Len(t, byService, 2)
	assert.Equal(t, 3.0, byService["checkout"].Value)
	assert.True(t, byService["checkout"].Firing)
	assert.Equal(t, 1.0, byService["search"].Value)
	assert.False(t, byService["search"].Firing)
}
