package traces_querying_tests

import (
	"fmt"
	"net/http"
	"testing"

	spans_core "obzly/internal/features/spans/core"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/stretchr/testify/assert"
)

func Test_GetProjectStats_ReturnsSeededTotals(t *testing.T) {
	router := CreateTraceQueryTestRouter()
	owner, project, _ := SetUpProjectWithTrace(t, router, "Traces Stats")

	var response spans_core.SpansStatsDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t, router,
		fmt.Sprintf("/api/v1/traces/query/stats/%s", project.ID.String()),
		"Bearer "+owner.Token, http.StatusOK, &response,
	)

	// SetUpProjectWithTrace seeds exactly 2 spans.
	assert.Equal(t, int64(2), response.TotalSpans)
	assert.Greater(t, response.TotalSizeMB, 0.0)
	assert.False(t, response.OldestSpanTime.IsZero())
	assert.False(t, response.NewestSpanTime.IsZero())
}

func Test_GetProjectStats_NonMember_ReturnsForbidden(t *testing.T) {
	router := CreateTraceQueryTestRouter()
	_, project, _ := SetUpProjectWithTrace(t, router, "Traces Stats Forbidden")
	outsider := users_testing.CreateTestUser(users_enums.UserRoleMember)

	test_utils.MakeGetRequest(
		t, router,
		fmt.Sprintf("/api/v1/traces/query/stats/%s", project.ID.String()),
		"Bearer "+outsider.Token, http.StatusForbidden,
	)
}
