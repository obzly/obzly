package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/stretchr/testify/assert"
)

// index.html and runtime-config.js change on every deploy, so they must never
// be cached like hashed assets.
func Test_CacheControlFor(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/index.html", "no-cache"},
		{"/runtime-config.js", "no-cache"},
		{"/assets/index-DfYHiBiQ.css", "public, max-age=31536000, immutable"},
		{"/assets/index-BkF7G9SR.js", "public, max-age=31536000, immutable"},
		{"/favicon.ico", "public, max-age=3600"},
		{"/logo.png", "public, max-age=3600"},
		{"/icons/foo.svg", "public, max-age=3600"},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			assert.Equal(t, tc.want, cacheControlFor(tc.path))
		})
	}
}

// A control plane answered OTLP exports (POST /v1/traces) with index.html
// and 200, so OBI pointed at it dropped all data while reporting success.
func Test_FrontendFallback_NonPageRequests_ReturnJSON404(t *testing.T) {
	staticDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(staticDir, "index.html"), []byte("<html></html>"), 0o600))

	cases := []struct {
		name           string
		method, path   string
		isControlPlane bool
		wantStatus     int
		wantBody       string
	}{
		{
			"OTLP export on control plane",
			http.MethodPost,
			"/v1/traces",
			true,
			http.StatusNotFound,
			"send telemetry to a Site",
		},
		{
			"OTLP GET on control plane",
			http.MethodGet,
			"/v1/metrics",
			true,
			http.StatusNotFound,
			"send telemetry to a Site",
		},
		{"unknown POST", http.MethodPost, "/some/page", false, http.StatusNotFound, "not found"},
		{"unknown API path", http.MethodGet, "/api/v1/nope", false, http.StatusNotFound, "not found"},
		{"SPA route", http.MethodGet, "/dashboards", false, http.StatusOK, "<html>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			router := gin.New()
			router.NoRoute(frontendFallback(staticDir, tc.isControlPlane))

			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(tc.method, tc.path, nil))

			assert.Equal(t, tc.wantStatus, recorder.Code)
			assert.Contains(t, recorder.Body.String(), tc.wantBody)
		})
	}
}

func Test_OpenQueryExecutorDB_CapsMemoryAndSpillsUnderTheGivenDir(t *testing.T) {
	spillDir := filepath.Join(t.TempDir(), "query-results", "spill")

	db, err := openQueryExecutorDB("819MB", spillDir, "2GB")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	var limit, tempDir, maxTemp string
	require.NoError(t, db.QueryRow(
		"SELECT current_setting('memory_limit'), current_setting('temp_directory'), current_setting('max_temp_directory_size')",
	).Scan(&limit, &tempDir, &maxTemp))
	require.Equal(t, "781.0 MiB", limit)
	require.Equal(t, spillDir, filepath.Dir(tempDir), "a directory of its own under the spill root")
	require.Equal(t, "1.8 GiB", maxTemp)
	require.DirExists(t, spillDir)
}

func Test_OpenQueryExecutorDB_RemovesSpillFilesLeftByAKilledExecutor(t *testing.T) {
	spillDir := filepath.Join(t.TempDir(), "query-results", "spill")
	require.NoError(t, os.MkdirAll(spillDir, 0o755))
	leftover := filepath.Join(spillDir, "duckdb_temp_storage_S32K-0.tmp")
	require.NoError(t, os.WriteFile(leftover, []byte("spilled"), 0o644))

	db, err := openQueryExecutorDB("819MB", spillDir, "2GB")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	require.NoFileExists(t, leftover)
	require.DirExists(t, spillDir)
}
