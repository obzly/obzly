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

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_SubmitLogs_WithClaudeCodeScope_StoredInAIStreamNotSearch(t *testing.T) {
	router := CreateLogQueryTestRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	uniqueID := uuid.New().String()
	project, _ := projects_testing.CreateTestProjectWithToken(
		fmt.Sprintf("AI Stream Test %s", uniqueID[:8]), owner.Token, router,
	)

	infra := CreateLogItemsWithMessages(uniqueID, []string{"infra line"}, logs_core.LogLevelInfo, nil)
	ai := CreateLogItemsWithMessages(
		uniqueID, []string{"claude_code.api_request"}, logs_core.LogLevelInfo,
		map[string]any{"scope.name": "com.anthropic.claude_code.events"},
	)
	SubmitLogsAndProcess(t, router, project.ID, append(infra, ai...))
	WaitForLogsToBeIndexed(t, router, project.ID, 1, uniqueID, "Bearer "+owner.Token)

	response := ExecuteTestQuery(
		t, router, project.ID, BuildSimpleConditionQuery("test_id", "equals", uniqueID),
		owner.Token, http.StatusOK,
	)
	require.Len(t, response.Logs, 1)
	assert.Equal(t, "infra line", response.Logs[0].Message)

	aiRepository := logs_core.GetAIRepository()
	require.NoError(t, aiRepository.ForceFlush())
	results, err := aiRepository.ExecuteScopedSQL(project.ID, &logs_core.ScopedSQLRequestDTO{
		From: time.Now().UTC().Add(-time.Hour),
		To:   time.Now().UTC().Add(time.Hour),
		Statements: []logs_core.ScopedSQLStatement{{
			SQL:  "SELECT message FROM logs WHERE json_extract_string(fields, '$.test_id') = ?",
			Args: []any{uniqueID},
		}},
	})
	require.NoError(t, err)
	require.Len(t, results[0].Rows, 1)
	assert.Equal(t, "claude_code.api_request", results[0].Rows[0][0])
}
