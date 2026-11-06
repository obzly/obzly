package logs_querying_tests

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"
	logs_querying "obzly/internal/features/logs/querying"
	logs_receiving "obzly/internal/features/logs/receiving"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// A streamed search returns the same page as execute, as rows frames in
// page order, then done with its cursor - the newest rows first.
func Test_StreamQuery_ReturnsTheSamePageAsExecuteInFrames(t *testing.T) {
	router, owner, project, uniqueID := SetupBasicQueryTest(t, "Stream query")
	now := time.Now().UTC().Truncate(time.Millisecond)
	var items []logs_receiving.LogItemRequestDTO
	// Spread over two days, so the search walks more than one window.
	for i := range 12 {
		items = append(items, logs_receiving.LogItemRequestDTO{
			Level: logs_core.LogLevelInfo, Message: fmt.Sprintf("stream %s %d", uniqueID, i),
			Timestamp: now.Add(-time.Duration(i) * 4 * time.Hour).Format(time.RFC3339Nano),
			Fields:    map[string]any{"test_id": uniqueID},
		})
	}
	SubmitLogsAndProcess(t, router, project.ID, items)
	WaitForLogsToBeIndexed(t, router, project.ID, 1, uniqueID, owner.Token)
	request := BuildSimpleConditionQuery("test_id", "equals", uniqueID)
	from, to := now.Add(-3*24*time.Hour), now.Add(time.Minute)
	request.TimeRange = &logs_core.TimeRangeDTO{From: &from, To: &to}
	request.Limit = 5
	falseValue := false
	request.TrackTotal = &falseValue

	executed := ExecuteTestQuery(t, router, project.ID, request, owner.Token, http.StatusOK)
	frames := streamQuery(t, router, project.ID, request, "Bearer "+owner.Token, http.StatusOK)

	var streamed []logs_core.LogItemDTO
	for _, frame := range frames[:len(frames)-1] {
		require.Contains(t, []string{logs_querying.SearchFrameRows, logs_querying.SearchFrameProgress}, frame.Type)
		streamed = append(streamed, frame.Logs...)
	}
	done := frames[len(frames)-1]
	require.Equal(t, logs_querying.SearchFrameDone, done.Type)
	require.Len(t, executed.Logs, 5)
	require.Equal(t, ids(executed.Logs), ids(streamed))
	require.Equal(t, executed.NextCursor, done.NextCursor)
	require.Equal(t, fmt.Sprintf("stream %s 0", uniqueID), streamed[0].Message, "newest first")
}

// Access and validation errors come back as plain JSON before any frame.
func Test_StreamQuery_ByANonMember_ReturnsForbiddenAsJSON(t *testing.T) {
	router, _, project, uniqueID := SetupBasicQueryTest(t, "Stream forbidden")
	stranger := users_testing.CreateTestUser(users_enums.UserRoleMember)

	response := test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method: http.MethodPost, URL: "/api/v1/logs/query/stream/" + project.ID.String(),
		AuthToken: "Bearer " + stranger.Token, Body: BuildSimpleConditionQuery("test_id", "equals", uniqueID),
		ExpectedStatus: http.StatusForbidden,
	})

	require.Contains(t, response.Headers.Get("Content-Type"), "application/json")
}

func streamQuery(
	t *testing.T, router *gin.Engine, projectID uuid.UUID, request *logs_core.LogQueryRequestDTO,
	token string, expectedStatus int,
) []logs_querying.SearchFrameDTO {
	t.Helper()
	body, err := json.Marshal(request)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, "/api/v1/logs/query/stream/"+projectID.String(), bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", token)
	req.RemoteAddr = "127.0.0.1:1234"
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	require.Equal(t, expectedStatus, recorder.Code)
	require.Equal(t, "application/x-ndjson", recorder.Header().Get("Content-Type"))

	var frames []logs_querying.SearchFrameDTO
	scanner := bufio.NewScanner(recorder.Body)
	scanner.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for scanner.Scan() {
		var frame logs_querying.SearchFrameDTO
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &frame))
		frames = append(frames, frame)
	}
	require.NotEmpty(t, frames)
	return frames
}

func ids(logs []logs_core.LogItemDTO) []string {
	result := make([]string, len(logs))
	for i, log := range logs {
		result[i] = log.ID
	}
	return result
}
