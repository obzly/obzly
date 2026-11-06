package logs_livetail_tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"
	logs_livetail "obzly/internal/features/logs/livetail"
	logs_receiving "obzly/internal/features/logs/receiving"
	projects_testing "obzly/internal/features/projects/testing"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	"obzly/internal/ingest/ingesttest"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// maxConcurrentTailsForTest mirrors the unexported maxConcurrentTails; keep
// them in sync.
const maxConcurrentTailsForTest = 5

// Test_Tail_RejectsBeyondConcurrentLimit: the tail over the cap gets a plain
// 429, and closing one connection frees a slot.
func Test_Tail_RejectsBeyondConcurrentLimit(t *testing.T) {
	router := createLiveTailTestRouter()
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	uniqueID := uuid.New().String()
	project, _ := projects_testing.CreateTestProjectWithToken(
		fmt.Sprintf("Live Tail Concurrency Test %s", uniqueID[:8]), owner.Token, router,
	)

	var cancels []context.CancelFunc
	t.Cleanup(func() {
		for _, cancel := range cancels {
			cancel()
		}
	})

	for range maxConcurrentTailsForTest {
		_, cancel := startTail(t, server.URL, project.ID.String(), owner.Token, nil)
		cancels = append(cancels, cancel)
	}

	newTailRequest := func() *http.Request {
		body, err := json.Marshal(logs_livetail.TailRequestDTO{})
		require.NoError(t, err)
		req, err := http.NewRequest(http.MethodPost,
			fmt.Sprintf("%s/api/v1/logs/tail/%s", server.URL, project.ID.String()), bytes.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+owner.Token)
		return req
	}

	resp, err := http.DefaultClient.Do(newTailRequest())
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)

	// Closing one connection frees a slot once the server sees the disconnect
	// (the slot is released in TailLogs' defer).
	cancels[0]()
	cancels = cancels[1:]
	time.Sleep(1 * time.Second)

	_, cancel := startTail(t, server.URL, project.ID.String(), owner.Token, nil)
	cancels = append(cancels, cancel)
}

// Test_Tail_HandlesHighVolumeWithoutDropping exercises the flush batching
// under a burst: nothing dropped or duplicated. Order across separate ingest
// requests isn't guaranteed (each publishes from its own goroutine).
func Test_Tail_HandlesHighVolumeWithoutDropping(t *testing.T) {
	assertTailHandlesHighVolume(t)
}

func Test_Tail_WithBroker_HandlesHighVolumeWithoutDropping(t *testing.T) {
	ingesttest.StartBroker(t, 64<<20)
	assertTailHandlesHighVolume(t)
}

func assertTailHandlesHighVolume(t *testing.T) {
	t.Helper()
	router := createLiveTailTestRouter()
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	uniqueID := uuid.New().String()
	project, _ := projects_testing.CreateTestProjectWithToken(
		fmt.Sprintf("Live Tail Volume Test %s", uniqueID[:8]), owner.Token, router,
	)

	filter := &logs_core.QueryNode{
		Type: logs_core.QueryNodeTypeCondition,
		Condition: &logs_core.ConditionNode{
			Field: "message", Operator: logs_core.ConditionOperatorContains, Value: uniqueID,
		},
	}
	reader, cancel := startTail(t, server.URL, project.ID.String(), owner.Token, filter)
	defer cancel()

	const logCount = 200
	go func() {
		for i := range logCount {
			body, err := json.Marshal(logs_receiving.SubmitLogsRequestDTO{
				Logs: []logs_receiving.LogItemRequestDTO{{
					Level:   logs_core.LogLevelInfo,
					Message: fmt.Sprintf("%s burst %d", uniqueID, i),
				}},
			})
			if err != nil {
				return
			}
			resp, err := http.Post(
				fmt.Sprintf("%s/api/v1/logs/receiving/%s", server.URL, project.ID.String()),
				"application/json", bytes.NewReader(body),
			)
			if err == nil {
				_ = resp.Body.Close()
			}
		}
	}()

	seen := make(map[string]int, logCount)
	for total := 0; total < logCount; total++ {
		frame := readFrame(t, reader, 15*time.Second)
		require.Equal(t, "log", frame.Type)
		require.NotNil(t, frame.Log)
		seen[frame.Log.Message]++
	}

	for i := range logCount {
		message := fmt.Sprintf("%s burst %d", uniqueID, i)
		require.Equalf(t, 1, seen[message], "expected exactly one delivery of %q", message)
	}
}
