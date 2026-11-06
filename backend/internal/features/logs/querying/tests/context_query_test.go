package logs_querying_tests

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"
	logs_receiving "obzly/internal/features/logs/receiving"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/stretchr/testify/require"
)

func contextURL(projectID fmt.Stringer) string {
	return "/api/v1/logs/query/context/" + projectID.String()
}

// From a search hit, the context endpoint returns its deployment's logs
// around it - every replica, including the logs the search filtered out.
func Test_ExecuteContext_FromASearchHit_ReturnsItsDeploymentsLogsAroundIt(t *testing.T) {
	router, owner, project, uniqueID := SetupBasicQueryTest(t, "Log context")
	base := time.Now().UTC().Add(-30 * time.Minute).Truncate(time.Millisecond)
	item := func(offset time.Duration, pod string, level logs_core.LogLevel, message string) logs_receiving.LogItemRequestDTO {
		return logs_receiving.LogItemRequestDTO{
			Level: level, Message: message + " " + uniqueID, Timestamp: base.Add(offset).Format(time.RFC3339Nano),
			Fields: map[string]any{
				"test_id": uniqueID, "k8s_pod_name": pod + uniqueID,
				"k8s_deployment_name": strings.Split(pod, "-")[0] + uniqueID,
			},
		}
	}
	SubmitLogsAndProcess(t, router, project.ID, []logs_receiving.LogItemRequestDTO{
		item(-3*time.Second, "api-1-", logs_core.LogLevelWarn, "pool exhausted"),
		item(-2*time.Second, "api-2-", logs_core.LogLevelWarn, "other replica"),
		item(-time.Second, "web-1-", logs_core.LogLevelInfo, "other deployment"),
		item(0, "api-1-", logs_core.LogLevelError, "request failed"),
		item(time.Second, "api-1-", logs_core.LogLevelInfo, "retrying"),
	})
	WaitForLogsToBeIndexed(t, router, project.ID, 5, uniqueID, owner.Token)
	hits := ExecuteTestQuery(t, router, project.ID,
		BuildSimpleConditionQuery("level", "equals", string(logs_core.LogLevelError)), owner.Token, http.StatusOK)
	require.Len(t, hits.Logs, 1)
	hit := hits.Logs[0]

	var response logs_core.LogContextResponseDTO
	test_utils.MakePostRequestAndUnmarshal(t, router, contextURL(project.ID), "Bearer "+owner.Token,
		logs_core.LogContextRequestDTO{
			Anchor: logs_core.LogContextAnchorDTO{ID: hit.ID, Timestamp: hit.Timestamp}, Before: 5, After: 5,
		}, http.StatusOK, &response)

	require.NotNil(t, response.Anchor)
	require.Equal(t, hit.ID, response.Anchor.ID)
	require.Len(t, response.Before, 2)
	require.Contains(t, response.Before[0].Message, "pool exhausted")
	require.Contains(t, response.Before[1].Message, "other replica")
	require.Len(t, response.After, 1)
	require.Contains(t, response.After[0].Message, "retrying")
	require.Equal(t, []logs_core.LogContextScopeDTO{{Field: "k8s_deployment_name", Value: "api" + uniqueID}},
		response.Scope)
}

func Test_ExecuteContext_InvalidRequests_ReturnBadRequest(t *testing.T) {
	router, owner, project, _ := SetupBasicQueryTest(t, "Log context validation")
	anchor := logs_core.LogContextAnchorDTO{ID: "x", Timestamp: time.Now().UTC()}

	for name, request := range map[string]logs_core.LogContextRequestDTO{
		"no anchor":     {},
		"bad direction": {Anchor: anchor, Direction: "sideways"},
		"bad cursor":    {Anchor: anchor, Direction: logs_core.ContextBefore, Cursor: "!!"},
	} {
		t.Run(name, func(t *testing.T) {
			test_utils.MakePostRequest(t, router, contextURL(project.ID), "Bearer "+owner.Token, request,
				http.StatusBadRequest)
		})
	}
}

func Test_ExecuteContext_WhenUserIsNotProjectMember_ReturnsForbidden(t *testing.T) {
	router, _, project, _ := SetupBasicQueryTest(t, "Log context access")
	nonMember := users_testing.CreateTestUser(users_enums.UserRoleMember)

	test_utils.MakePostRequest(t, router, contextURL(project.ID), "Bearer "+nonMember.Token,
		logs_core.LogContextRequestDTO{Anchor: logs_core.LogContextAnchorDTO{ID: "x", Timestamp: time.Now().UTC()}},
		http.StatusForbidden)
	test_utils.MakePostRequest(t, router, contextURL(project.ID), "",
		logs_core.LogContextRequestDTO{}, http.StatusUnauthorized)
}
