package logs_querying_tests

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"
	projects_testing "obzly/internal/features/projects/testing"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sqlQueryTimeRange() *logs_core.TimeRangeDTO {
	from := time.Now().UTC().Add(-2 * time.Hour)
	to := time.Now().UTC().Add(time.Hour)
	return &logs_core.TimeRangeDTO{From: &from, To: &to}
}

func executeSQLQuery(
	t *testing.T,
	projectID uuid.UUID,
	token, sql string,
	expectedStatus int,
) *logs_core.SQLQueryResponseDTO {
	t.Helper()

	router := CreateLogQueryTestRouter()
	request := &logs_core.ExecuteSQLQueryRequestDTO{SQL: sql, TimeRange: sqlQueryTimeRange()}

	if expectedStatus != http.StatusOK {
		test_utils.MakePostRequest(
			t, router,
			fmt.Sprintf("/api/v1/logs/query/execute-sql/%s", projectID.String()),
			"Bearer "+token, request, expectedStatus,
		)
		return nil
	}

	var response logs_core.SQLQueryResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router,
		fmt.Sprintf("/api/v1/logs/query/execute-sql/%s", projectID.String()),
		"Bearer "+token, request, expectedStatus, &response,
	)
	return &response
}

func Test_ExecuteSQLQuery_ReturnsSeededLogs(t *testing.T) {
	router := CreateLogQueryTestRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	uniqueID := uuid.New().String()
	project, _ := projects_testing.CreateTestProjectWithToken(
		fmt.Sprintf("SQL Query Test %s", uniqueID[:8]), owner.Token, router,
	)

	logItems := CreateLogItemsWithMessages(
		uniqueID, []string{"first message", "second message"}, logs_core.LogLevelInfo, nil,
	)
	SubmitLogsAndProcess(t, router, project.ID, logItems)
	WaitForLogsToBeIndexed(t, router, project.ID, len(logItems), uniqueID, "Bearer "+owner.Token)

	response := executeSQLQuery(
		t, project.ID, owner.Token,
		fmt.Sprintf(
			`SELECT message FROM logs WHERE json_extract_string(fields, '$.test_id') = '%s' ORDER BY message`,
			uniqueID,
		),
		http.StatusOK,
	)

	require.NotNil(t, response)
	require.Len(t, response.Rows, 2)
	assert.Equal(t, "first message", response.Rows[0][0])
	assert.Equal(t, "second message", response.Rows[1][0])
}

func Test_ExecuteSQLQuery_DisallowedStatement_ReturnsBadRequest(t *testing.T) {
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	router := CreateLogQueryTestRouter()
	project, _ := projects_testing.CreateTestProjectWithToken("SQL Query Bad", owner.Token, router)

	for _, badSQL := range []string{"DROP TABLE logs", "SELECT 1; DROP TABLE logs", ""} {
		executeSQLQuery(t, project.ID, owner.Token, badSQL, http.StatusBadRequest)
	}
}

func Test_ExecuteSQLQuery_NonMember_ReturnsForbidden(t *testing.T) {
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	router := CreateLogQueryTestRouter()
	project, _ := projects_testing.CreateTestProjectWithToken("SQL Query Forbidden", owner.Token, router)
	outsider := users_testing.CreateTestUser(users_enums.UserRoleMember)

	executeSQLQuery(t, project.ID, outsider.Token, "SELECT * FROM logs", http.StatusForbidden)
}

func Test_ExecuteSQLQuery_MissingTimeRangeTo_ReturnsBadRequest(t *testing.T) {
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	router := CreateLogQueryTestRouter()
	project, _ := projects_testing.CreateTestProjectWithToken("SQL Query No Range", owner.Token, router)

	test_utils.MakePostRequest(
		t, router,
		fmt.Sprintf("/api/v1/logs/query/execute-sql/%s", project.ID.String()),
		"Bearer "+owner.Token,
		&logs_core.ExecuteSQLQueryRequestDTO{SQL: "SELECT * FROM logs"},
		http.StatusBadRequest,
	)
}

func Test_ExecuteSQLQuery_MissingTimeRangeFrom_ReturnsBadRequest(t *testing.T) {
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	router := CreateLogQueryTestRouter()
	project, _ := projects_testing.CreateTestProjectWithToken("SQL Query No From", owner.Token, router)
	to := time.Now().UTC()

	test_utils.MakePostRequest(
		t, router,
		fmt.Sprintf("/api/v1/logs/query/execute-sql/%s", project.ID.String()),
		"Bearer "+owner.Token,
		&logs_core.ExecuteSQLQueryRequestDTO{SQL: "SELECT * FROM logs", TimeRange: &logs_core.TimeRangeDTO{To: &to}},
		http.StatusBadRequest,
	)
}

func Test_ExecuteSQLQuery_StatementHiddenInNonStandardString_ReturnsBadRequest(t *testing.T) {
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	router := CreateLogQueryTestRouter()
	project, _ := projects_testing.CreateTestProjectWithToken("SQL Query Bypass", owner.Token, router)

	for _, badSQL := range []string{
		`SELECT E'\''; SET memory_limit='64GB'; SELECT ''`,
		`SELECT $$'$$; SET memory_limit='64GB'; SELECT $$'$$`,
		`SELECT * FROM logs WHERE message = 'unterminated`,
	} {
		executeSQLQuery(t, project.ID, owner.Token, badSQL, http.StatusBadRequest)
	}
}
