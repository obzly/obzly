package mcp_server

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	control_plane_services "obzly/internal/features/control_plane/services"
	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_SiteCall_OnControlPlane_GoesThroughTheSiteProxy(t *testing.T) {
	var seen []string
	engine := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
		assert.Contains(t, r.Header.Get("Authorization"), "Bearer ")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	user := &users_models.User{ID: uuid.New(), Role: users_enums.UserRoleMember}

	cp := &apiExecutor{engine: engine, controlPlane: true}
	var out map[string]any
	require.NoError(t, cp.siteCall(context.Background(), user, "eu-west", http.MethodGet, "/projects", nil, nil, &out))
	assert.Equal(t, "/sites/eu-west/api/v1/projects", seen[0])

	err := cp.siteCall(context.Background(), user, "", http.MethodGet, "/projects", nil, nil, &out)
	assert.ErrorContains(t, err, "list_sites")
	err = cp.siteCall(context.Background(), user, "../admin", http.MethodGet, "/projects", nil, nil, &out)
	assert.ErrorContains(t, err, "invalid site id")

	local := &apiExecutor{engine: engine}
	require.NoError(
		t,
		local.siteCall(context.Background(), user, "ignored", http.MethodGet, "/projects", nil, nil, &out),
	)
	assert.Equal(t, "/api/v1/projects", seen[len(seen)-1])
}

func Test_Call_ErrorResponse_ReturnsItsMessage(t *testing.T) {
	engine := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"you don't have access to this site"}`))
	})
	user := &users_models.User{ID: uuid.New()}
	exec := &apiExecutor{engine: engine, controlPlane: true}

	err := exec.siteCall(context.Background(), user, "eu", http.MethodGet, "/projects", nil, nil, nil)
	assert.EqualError(t, err, "you don't have access to this site")
}

func Test_Window_ParsesSinceAndRejectsBadInput(t *testing.T) {
	w, err := windowArgs{Since: "7d"}.window()
	require.NoError(t, err)
	assert.InDelta(t, (7 * 24 * 3600), w.To.Sub(*w.From).Seconds(), 1)

	_, err = windowArgs{Since: "soon"}.window()
	assert.Error(t, err)
	_, err = windowArgs{From: "2026-09-24T10:00:00Z", To: "2026-09-24T09:00:00Z"}.window()
	assert.Error(t, err)
}

// Regression: GET calls had a nil body; the control-plane proxy reads the
// body through http.MaxBytesReader, which panicked (500 from list_projects).
func Test_Call_WithoutBody_GivesHandlersAnEmptyBody(t *testing.T) {
	engine := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		require.NoError(t, err)
		assert.Empty(t, body)
		_, _ = w.Write([]byte(`{}`))
	})
	exec := &apiExecutor{engine: engine, controlPlane: true}

	var out map[string]any
	require.NoError(t, exec.siteCall(context.Background(), &users_models.User{ID: uuid.New()},
		"site-a", http.MethodGet, "/projects", nil, nil, &out))
}

func Test_SiteCall_WriteOrUnknownEndpoint_IsRefusedBeforeTheHandler(t *testing.T) {
	called := false
	engine := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		_, _ = w.Write([]byte(`{}`))
	})
	user := &users_models.User{ID: uuid.New()}
	exec := &apiExecutor{engine: engine}
	projectID := uuid.NewString()

	for _, attempt := range []struct{ method, path string }{
		{http.MethodPost, "/projects"},
		{http.MethodDelete, "/projects/" + projectID},
		{http.MethodPut, "/projects/dashboards/" + projectID + "/" + uuid.NewString()},
		{http.MethodGet, "/logs/query/execute/" + projectID},
		{http.MethodGet, "/api-keys"},
		{http.MethodGet, "/traces/query/get/" + projectID + "/..%2F..%2Fapi-keys"},
		{http.MethodGet, "/traces/query/get/" + projectID + "/a/b"},
	} {
		err := exec.siteCall(context.Background(), user, "", attempt.method, attempt.path, nil, nil, nil)
		require.ErrorIs(t, err, errCallNotAllowed, attempt.method+" "+attempt.path)
	}
	err := exec.centralCall(context.Background(), user, http.MethodPost, "/control-plane/sites", nil, nil)
	require.ErrorIs(t, err, errCallNotAllowed)
	assert.False(t, called, "a refused call never reaches a handler")

	require.NoError(t, exec.siteCall(context.Background(), user, "", http.MethodPost,
		"/logs/query/execute/"+projectID, nil, map[string]any{}, nil))
	assert.True(t, called)
}

// Every endpoint MCP tools may call must also pass the control plane's Site
// proxy, or the tool works on a single install and fails through a control plane.
func Test_SiteCallAllowlist_EveryEntryPassesTheControlPlaneProxy(t *testing.T) {
	for method, patterns := range siteCallAllowlist.patterns {
		for _, pattern := range patterns {
			segments := make([]string, len(pattern))
			for i, part := range pattern {
				segments[i] = part
				if strings.HasPrefix(part, "{") {
					segments[i] = uuid.NewString()
				}
			}
			path := "/api/v1/" + strings.Join(segments, "/")
			assert.True(t, control_plane_services.IsProxyPathAllowed(method, path), method+" "+path)
		}
	}
}

func Test_Call_ContextCancelled_StopsWaitingAndCancelsTheHandler(t *testing.T) {
	started := make(chan struct{})
	handlerStopped := make(chan struct{})
	engine := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(handlerStopped)
	})
	exec := &apiExecutor{engine: engine}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()

	err := exec.siteCall(ctx, &users_models.User{ID: uuid.New()}, "", http.MethodPost,
		"/logs/query/execute/"+uuid.NewString(), nil, map[string]any{}, nil)

	assert.EqualError(t, err, "the call was cancelled")
	select {
	case <-handlerStopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler's request context was not cancelled")
	}
}

func Test_Call_HandlerPanics_ReturnsAnErrorInsteadOfCrashing(t *testing.T) {
	engine := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })
	exec := &apiExecutor{engine: engine}

	err := exec.siteCall(context.Background(), &users_models.User{ID: uuid.New()}, "", http.MethodGet,
		"/projects", nil, nil, nil)

	assert.EqualError(t, err, "internal error while running the query")
}
