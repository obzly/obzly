package requestlog_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"obzly/internal/util/requestlog"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func testRouter(logBuf *bytes.Buffer) *gin.Engine {
	gin.SetMode(gin.TestMode)
	log := slog.New(slog.NewTextHandler(logBuf, nil))

	router := gin.New()
	router.Use(requestlog.Middleware(log))
	router.GET("/ping", func(ctx *gin.Context) { ctx.Status(http.StatusOK) })
	return router
}

// An api_key passed as a query parameter must never appear in plaintext in the log.
func Test_Middleware_RedactsApiKeyQueryParam(t *testing.T) {
	var logBuf bytes.Buffer
	router := testRouter(&logBuf)

	req, _ := http.NewRequest(http.MethodGet, "/ping?api_key=super-secret-value&service=checkout", nil)
	router.ServeHTTP(httptest.NewRecorder(), req)

	logOutput := logBuf.String()
	assert.NotContains(t, logOutput, "super-secret-value")
	assert.Contains(t, logOutput, "REDACTED")
	assert.Contains(t, logOutput, "service=checkout", "non-sensitive query params should still be logged")
}

// A path containing an encoded newline must not inject a second, fake log line.
func Test_Middleware_NewlineInPathDoesNotForgeLogLines(t *testing.T) {
	var logBuf bytes.Buffer
	router := testRouter(&logBuf)

	// What gin hands the middleware after URL-decoding %0a: a literal newline.
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.URL.Path = "/ping\nlevel=ERROR msg=\"fabricated log line\""
	router.ServeHTTP(httptest.NewRecorder(), req)

	logOutput := logBuf.String()
	// slog escapes the newline inside a quoted value, so there is still one line.
	assert.Equal(t, 1, bytes.Count(logBuf.Bytes(), []byte("\n")),
		"the embedded newline must not produce an extra log line: %q", logOutput)
}
