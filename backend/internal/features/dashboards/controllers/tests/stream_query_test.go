package dashboards_controllers_tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	dashboards_dto "obzly/internal/features/dashboards/dto"
	dashboards_models "obzly/internal/features/dashboards/models"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func streamFrames(t *testing.T, body []byte) []dashboards_dto.PanelFrameDTO {
	t.Helper()
	var frames []dashboards_dto.PanelFrameDTO
	for _, line := range bytes.Split(bytes.TrimSpace(body), []byte("\n")) {
		var frame dashboards_dto.PanelFrameDTO
		require.NoError(t, json.Unmarshal(line, &frame), string(line))
		frames = append(frames, frame)
	}
	return frames
}

// A streamed time-bucketed logs panel's bucket frames add up to the
// one-shot answer, and the stream ends with "done".
func Test_StreamPanelQuery_TimeSeries_FramesAddUpToTheOneShotAnswer(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner, project, _ := SetUpProjectWithLogs(t, router, "Panel Stream")
	request := dashboards_dto.PanelQueryRequestDTO{
		Panel: dashboards_models.Panel{
			Type: dashboards_models.PanelTypeBar, GroupField: "level", BucketMs: time.Minute.Milliseconds(),
		},
		TimeRange: timeRangeCoveringNow(),
	}
	var whole dashboards_dto.PanelQueryResponseDTO
	test_utils.MakePostRequestAndUnmarshal(t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s/query", project.ID.String()),
		"Bearer "+owner.Token, request, http.StatusOK, &whole)

	response := test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method: http.MethodPost, URL: fmt.Sprintf("/api/v1/projects/dashboards/%s/query/stream", project.ID.String()),
		Body: request, AuthToken: "Bearer " + owner.Token, ExpectedStatus: http.StatusOK,
	})
	assert.Equal(t, "application/x-ndjson", response.Headers.Get("Content-Type"))
	frames := streamFrames(t, response.Body)
	require.NotEmpty(t, frames)
	assert.Equal(t, dashboards_dto.PanelFrameDone, frames[len(frames)-1].Type)
	sums := map[string]float64{}
	for _, frame := range frames {
		for _, bucket := range frame.Buckets {
			sums[bucket.Timestamp.String()+bucket.GroupKey] += bucket.Value
		}
	}
	require.Len(t, sums, len(whole.Buckets))
	for _, bucket := range whole.Buckets {
		assert.Equal(t, bucket.Value, sums[bucket.Timestamp.String()+bucket.GroupKey])
	}
}

func Test_StreamPanelQuery_NotAMember_IsAPlainForbidden(t *testing.T) {
	router := CreateDashboardTestRouter()
	_, project, _ := SetUpProjectWithLogs(t, router, "Panel Stream Access")
	outsider := users_testing.CreateTestUser(users_enums.UserRoleMember)

	test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method: http.MethodPost, URL: fmt.Sprintf("/api/v1/projects/dashboards/%s/query/stream", project.ID.String()),
		Body: dashboards_dto.PanelQueryRequestDTO{
			Panel:     dashboards_models.Panel{Type: dashboards_models.PanelTypeBar, BucketMs: 60_000},
			TimeRange: timeRangeCoveringNow(),
		},
		AuthToken: "Bearer " + outsider.Token, ExpectedStatus: http.StatusForbidden,
	})
}
