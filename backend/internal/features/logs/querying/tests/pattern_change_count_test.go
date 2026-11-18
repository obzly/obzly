package logs_querying_tests

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"
	logs_receiving "obzly/internal/features/logs/receiving"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func changesURL(projectID fmt.Stringer) string {
	return "/api/v1/logs/query/changes/" + projectID.String()
}

// The Anomalies button's count: how many patterns are new, spiking or dropping, without
// the list; a query on raw-only fields isn't counted; the answer is cached, so logs
// arriving right after don't change it.
func Test_PatternChangesCountOnly_CountsChangesAndCachesTheAnswer(t *testing.T) {
	router, owner, project, uniqueID := SetupBasicQueryTest(t, "Pattern change count")
	currentFrom := time.Now().UTC().Truncate(time.Minute).Add(-40 * time.Minute)
	baselineFrom := currentFrom.Add(-30 * time.Minute)
	var items []logs_receiving.LogItemRequestDTO
	add := func(from time.Time, n int, level logs_core.LogLevel, format string) {
		for i := range n {
			items = append(items, logs_receiving.LogItemRequestDTO{
				Level: level, Message: fmt.Sprintf(format, i),
				Timestamp: from.Add(time.Duration(i+1) * time.Minute).Format(time.RFC3339Nano),
				Fields:    map[string]any{"test_id": uniqueID},
			})
		}
	}
	add(baselineFrom, 6, logs_core.LogLevelInfo, "request %d served")
	add(currentFrom, 6, logs_core.LogLevelInfo, "request %d served")
	add(baselineFrom, 10, logs_core.LogLevelInfo, "cache %d warmed")
	add(currentFrom, 6, logs_core.LogLevelError, "payment %d failed")
	add(currentFrom, 7, logs_core.LogLevelWarn, "retry %d scheduled")
	SubmitLogsAndProcess(t, router, project.ID, items)
	WaitForLogsToBeIndexed(t, router, project.ID, len(items), uniqueID, owner.Token)

	request := logs_core.PatternChangesRequestDTO{
		Current: &logs_core.TimeRangeDTO{From: &currentFrom, To: new(currentFrom.Add(30 * time.Minute))},
		Baseline: &logs_core.TimeRangeDTO{
			From: &baselineFrom, To: new(currentFrom),
		},
		CountOnly: true,
	}
	var counted logs_core.PatternChangesResponseDTO
	test_utils.MakePostRequestAndUnmarshal(t, router, changesURL(project.ID), "Bearer "+owner.Token, request,
		http.StatusOK, &counted)
	assert.True(t, counted.Counted)
	assert.Equal(t, 2, counted.NewCount, "payment and retry")
	assert.Equal(t, 0, counted.SpikeCount)
	assert.Equal(t, 1, counted.DropCount, "cache")
	assert.Empty(t, counted.Changes)

	// The full call agrees.
	full := request
	full.CountOnly = false
	var listed logs_core.PatternChangesResponseDTO
	test_utils.MakePostRequestAndUnmarshal(t, router, changesURL(project.ID), "Bearer "+owner.Token, full,
		http.StatusOK, &listed)
	require.Len(t, listed.Changes, 3)
	assert.Equal(t, counted.NewCount, listed.NewCount)

	// Cached: a new pattern arriving now isn't counted until the entry expires.
	add(currentFrom.Add(10*time.Minute), 6, logs_core.LogLevelError, "disk %d full")
	SubmitLogsAndProcess(t, router, project.ID, items[len(items)-6:])
	WaitForLogsToBeIndexed(t, router, project.ID, len(items), uniqueID, owner.Token)
	var again logs_core.PatternChangesResponseDTO
	test_utils.MakePostRequestAndUnmarshal(t, router, changesURL(project.ID), "Bearer "+owner.Token, request,
		http.StatusOK, &again)
	assert.Equal(t, counted, again)

	// A message search needs raw logs: not counted.
	byMessage := request
	byMessage.Query = BuildCondition("message", "contains", "payment")
	var raw logs_core.PatternChangesResponseDTO
	test_utils.MakePostRequestAndUnmarshal(t, router, changesURL(project.ID), "Bearer "+owner.Token, byMessage,
		http.StatusOK, &raw)
	assert.False(t, raw.Counted)
}

func Test_PatternChangesCountOnly_WhenUserIsNotProjectMember_ReturnsForbidden(t *testing.T) {
	router, _, project, _ := SetupBasicQueryTest(t, "Pattern change count access")
	nonMember := users_testing.CreateTestUser(users_enums.UserRoleMember)
	now := time.Now().UTC()
	test_utils.MakePostRequest(t, router, changesURL(project.ID), "Bearer "+nonMember.Token,
		logs_core.PatternChangesRequestDTO{
			Current:   &logs_core.TimeRangeDTO{From: new(now.Add(-time.Hour)), To: &now},
			Baseline:  &logs_core.TimeRangeDTO{From: new(now.Add(-2 * time.Hour)), To: new(now.Add(-time.Hour))},
			CountOnly: true,
		}, http.StatusForbidden)
}
