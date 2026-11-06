package logs_querying_tests

import (
	"fmt"
	"net/http"
	"testing"

	projects_controllers "obzly/internal/features/projects/controllers"
	test_utils "obzly/internal/util/testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The search bar turns `duration_ms > 100` into greater_than on a custom
// field; on a promoted number field it must compare numerically, not 400.
func Test_ExecuteQuery_RangeOnPromotedNumberField_ComparesNumerically(t *testing.T) {
	router, owner, project, uniqueID := SetupTestProjectWithLogs(t, "promoted range", 0)
	test_utils.MakePostRequest(t, router, "/api/v1/projects/"+project.ID.String()+"/promoted-fields",
		"Bearer "+owner.Token, projects_controllers.PromoteFieldRequest{Field: "duration_ms", Type: "number"},
		http.StatusOK)
	SubmitLogsWithCustomFields(t, router, project.ID, uniqueID, 3, map[string]any{"duration_ms": 50})
	SubmitLogsWithCustomFields(t, router, project.ID, uniqueID, 2, map[string]any{"duration_ms": 500})
	WaitForLogsToBeIndexed(t, router, project.ID, 5, uniqueID, "Bearer "+owner.Token)

	slow := ExecuteTestQuery(t, router, project.ID, BuildLogicalQuery("and",
		*BuildCondition("message", "contains", uniqueID),
		*BuildCondition("duration_ms", "greater_than", 100),
	), owner.Token, http.StatusOK)
	require.NotNil(t, slow)
	assert.Len(t, slow.Logs, 2)
	for _, log := range slow.Logs {
		assert.Equal(t, "500", fmt.Sprint(log.Fields["duration_ms"]))
	}

	// An unpromoted custom field has no numbers to compare: no match, not an error.
	none := ExecuteTestQuery(t, router, project.ID, BuildLogicalQuery("and",
		*BuildCondition("message", "contains", uniqueID),
		*BuildCondition("not_promoted", "greater_than", 1),
	), owner.Token, http.StatusOK)
	require.NotNil(t, none)
	assert.Empty(t, none.Logs)
}
