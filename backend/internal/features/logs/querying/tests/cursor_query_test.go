package logs_querying_tests

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"
	logs_receiving "obzly/internal/features/logs/receiving"
	logs_receiving_tests "obzly/internal/features/logs/receiving/tests"

	"github.com/stretchr/testify/require"
)

// Paging by cursor returns every matching log once, in (timestamp, id)
// order - ten of them share one timestamp, which an offset or a
// timestamp-only cursor would split wrongly.
func Test_ExecuteQuery_PagingByCursor_ReturnsEveryLogOnceInOrder(t *testing.T) {
	router, owner, project, uniqueID := SetupBasicQueryTest(t, "Cursor paging")
	base := time.Now().UTC().Add(-30 * time.Minute).Truncate(time.Millisecond)
	var items []logs_receiving.LogItemRequestDTO
	for i := range 25 {
		at := base.Add(time.Duration(i) * time.Second)
		if i < 10 {
			at = base
		}
		items = append(items, logs_receiving.LogItemRequestDTO{
			Level: logs_core.LogLevelInfo, Message: fmt.Sprintf("cursor %s %d", uniqueID, i),
			Timestamp: at.Format(time.RFC3339Nano), Fields: map[string]any{"test_id": uniqueID},
		})
	}
	SubmitLogsAndProcess(t, router, project.ID, items)
	WaitForLogsToBeIndexed(t, router, project.ID, 25, uniqueID, owner.Token)

	request := BuildQueryWithPagination("test_id", "equals", uniqueID, 7, 0)
	var pages [][]logs_core.LogItemDTO
	for range 10 {
		response := ExecuteTestQuery(t, router, project.ID, request, owner.Token, 200)
		pages = append(pages, response.Logs)
		if len(pages) > 1 {
			require.EqualValues(t, logs_core.TotalNotCounted, response.Total, "a cursor page is not counted again")
		} else {
			require.EqualValues(t, 25, response.Total)
		}
		if response.NextCursor == "" {
			break
		}
		request.Cursor = response.NextCursor
	}

	var got []logs_core.LogItemDTO
	for _, page := range pages {
		got = append(got, page...)
	}
	require.Len(t, got, 25)
	require.Len(t, pages, 4, "7 + 7 + 7 + 4, and the short page has no cursor")
	want := slices.Clone(got)
	slices.SortFunc(want, func(a, b logs_core.LogItemDTO) int {
		if c := b.Timestamp.Compare(a.Timestamp); c != 0 {
			return c
		}
		return strings.Compare(b.ID, a.ID)
	})
	require.Equal(t, want, got, "newest first, id descending within a timestamp")
	seen := map[string]bool{}
	for _, item := range got {
		require.False(t, seen[item.ID], "log %s returned twice", item.ID)
		seen[item.ID] = true
	}
}

func Test_ExecuteQuery_WithTrackTotalFalse_SkipsTheCount(t *testing.T) {
	router, owner, project, uniqueID := SetupBasicQueryTest(t, "Track total")
	SubmitLogsAndProcess(t, router, project.ID, logs_receiving_tests.CreateValidLogItems(5, uniqueID))
	WaitForLogsToBeIndexed(t, router, project.ID, 5, uniqueID, owner.Token)
	request := BuildQueryWithPagination("test_id", "equals", uniqueID, 10, 0)
	trackTotal := false
	request.TrackTotal = &trackTotal

	response := ExecuteTestQuery(t, router, project.ID, request, owner.Token, 200)

	require.EqualValues(t, logs_core.TotalNotCounted, response.Total)
	require.Len(t, response.Logs, 5)
}

func Test_ExecuteQuery_WithInvalidCursor_ReturnsBadRequest(t *testing.T) {
	router, owner, project, uniqueID := SetupBasicQueryTest(t, "Invalid cursor")
	request := BuildQueryWithPagination("test_id", "equals", uniqueID, 10, 0)
	request.Cursor = "not-a-cursor"

	ExecuteTestQuery(t, router, project.ID, request, owner.Token, 400)
}
