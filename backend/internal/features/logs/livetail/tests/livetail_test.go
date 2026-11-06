package logs_livetail_tests

import (
	"bufio"
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
	projects_controllers "obzly/internal/features/projects/controllers"
	projects_testing "obzly/internal/features/projects/testing"
	users_enums "obzly/internal/features/users/enums"
	users_middleware "obzly/internal/features/users/middleware"
	users_services "obzly/internal/features/users/services"
	users_testing "obzly/internal/features/users/testing"
	"obzly/internal/ingest/ingesttest"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func createLiveTailTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	v1 := router.Group("/api/v1")
	logs_receiving.GetReceivingController().RegisterRoutes(v1)

	protected := v1.Group("").Use(users_middleware.AuthMiddleware(users_services.GetUserService()))
	if routerGroup, ok := protected.(*gin.RouterGroup); ok {
		projects_controllers.GetProjectController().RegisterRoutes(routerGroup)
		projects_controllers.GetMembershipController().RegisterRoutes(routerGroup)
		logs_livetail.GetController().RegisterRoutes(routerGroup)
	}

	return router
}

type tailFrame struct {
	Type string                `json:"type"`
	Log  *logs_core.LogItemDTO `json:"log,omitempty"`
}

// readFrame reads one NDJSON line off r, failing after timeout so a stalled
// stream fails loudly instead of hanging CI.
func readFrame(t *testing.T, r *bufio.Reader, timeout time.Duration) tailFrame {
	t.Helper()

	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := r.ReadString('\n')
		ch <- result{line, err}
	}()

	select {
	case res := <-ch:
		require.NoError(t, res.err)
		var frame tailFrame
		require.NoError(t, json.Unmarshal([]byte(res.line), &frame))
		return frame
	case <-time.After(timeout):
		t.Fatalf("timed out after %s waiting for a tail frame", timeout)
		return tailFrame{}
	}
}

// startTail opens a real streaming POST (a ResponseRecorder can't be read
// mid-handler) and returns a reader positioned after the "ready" frame, plus
// a cancel func that ends the stream.
func startTail(
	t *testing.T,
	serverURL, projectID, token string,
	query *logs_core.QueryNode,
) (*bufio.Reader, context.CancelFunc) {
	t.Helper()

	body, err := json.Marshal(logs_livetail.TailRequestDTO{Query: query})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/api/v1/logs/tail/%s", serverURL, projectID), bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, http.StatusOK, resp.StatusCode)

	reader := bufio.NewReader(resp.Body)
	ready := readFrame(t, reader, 5*time.Second)
	require.Equal(t, "ready", ready.Type)

	return reader, cancel
}

// Test_Tail_StreamsOnlyMatchingLogsInRealTime checks Live Tail end to end:
// logs submitted over the real ingest HTTP path reach an open tail only when
// they match its filter.
func Test_Tail_StreamsOnlyMatchingLogsInRealTime(t *testing.T) {
	assertTailStreamsOnlyMatchingLogs(t)
}

// With the broker in use (INGEST_BUFFER=jetstream) the same tail runs on
// core NATS instead of Valkey Pub/Sub.
func Test_Tail_WithBroker_StreamsOnlyMatchingLogsInRealTime(t *testing.T) {
	ingesttest.StartBroker(t, 64<<20)
	assertTailStreamsOnlyMatchingLogs(t)
}

func assertTailStreamsOnlyMatchingLogs(t *testing.T) {
	t.Helper()
	router := createLiveTailTestRouter()
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	uniqueID := uuid.New().String()
	project, _ := projects_testing.CreateTestProjectWithToken(
		fmt.Sprintf("Live Tail Test %s", uniqueID[:8]), owner.Token, router,
	)

	filter := &logs_core.QueryNode{
		Type: logs_core.QueryNodeTypeCondition,
		Condition: &logs_core.ConditionNode{
			Field: "level", Operator: logs_core.ConditionOperatorEquals, Value: "ERROR",
		},
	}
	reader, cancel := startTail(t, server.URL, project.ID.String(), owner.Token, filter)
	defer cancel()

	submit := func(level logs_core.LogLevel, message string) {
		body, err := json.Marshal(logs_receiving.SubmitLogsRequestDTO{
			Logs: []logs_receiving.LogItemRequestDTO{{Level: level, Message: message}},
		})
		require.NoError(t, err)
		resp, err := http.Post(
			fmt.Sprintf("%s/api/v1/logs/receiving/%s", server.URL, project.ID.String()),
			"application/json", bytes.NewReader(body),
		)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusAccepted, resp.StatusCode)
	}

	// Non-matching, then matching: only the second may arrive.
	submit(logs_core.LogLevelInfo, "request handled")
	submit(logs_core.LogLevelError, "connection refused")

	frame := readFrame(t, reader, 10*time.Second)
	require.Equal(t, "log", frame.Type)
	require.NotNil(t, frame.Log)
	require.Equal(t, "connection refused", frame.Log.Message)
	require.Equal(t, "error", frame.Log.Level)
}

// Test_Tail_ForbiddenForNonMember: a non-member gets a plain 403 JSON
// response, not a stream.
func Test_Tail_ForbiddenForNonMember(t *testing.T) {
	router := createLiveTailTestRouter()

	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	outsider := users_testing.CreateTestUser(users_enums.UserRoleMember)
	uniqueID := uuid.New().String()
	project, _ := projects_testing.CreateTestProjectWithToken(
		fmt.Sprintf("Live Tail Forbidden Test %s", uniqueID[:8]), owner.Token, router,
	)

	body, err := json.Marshal(logs_livetail.TailRequestDTO{})
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("/api/v1/logs/tail/%s", project.ID.String()), bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+outsider.Token)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusForbidden, w.Code)
}
