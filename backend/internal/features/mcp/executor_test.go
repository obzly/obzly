package mcp_server

import (
	"context"
	"io"
	"net/http"
	"testing"

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
