package security_headers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"obzly/internal/util/security_headers"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func testRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(security_headers.Middleware())
	router.GET("/ping", func(ctx *gin.Context) { ctx.Status(http.StatusOK) })
	return router
}

func Test_Middleware_SetsBaselineHeaders(t *testing.T) {
	router := testRouter()

	req, _ := http.NewRequest(http.MethodGet, "/ping", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, "nosniff", recorder.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "DENY", recorder.Header().Get("X-Frame-Options"))
	assert.Equal(t, security_headers.ContentSecurityPolicy, recorder.Header().Get("Content-Security-Policy"))
	assert.Equal(t, "strict-origin-when-cross-origin", recorder.Header().Get("Referrer-Policy"))
}

func Test_Middleware_SPAResponse_SendsScriptRestrictingCSP(t *testing.T) {
	router := testRouter()
	router.NoRoute(func(ctx *gin.Context) { ctx.String(http.StatusOK, "<html></html>") })

	req, _ := http.NewRequest(http.MethodGet, "/projects/123", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	csp := recorder.Header().Get("Content-Security-Policy")
	assert.Contains(t, csp, "script-src 'self';")
	assert.NotContains(t, csp, "'unsafe-eval'")
	assert.NotContains(t, strings.Split(strings.Split(csp, "script-src")[1], ";")[0], "unsafe-inline")
	assert.Contains(t, csp, "object-src 'none'")
	assert.Contains(t, csp, "base-uri 'self'")
	assert.Contains(t, csp, "frame-ancestors 'none'")
	assert.Contains(t, csp, "https://fonts.googleapis.com")
	assert.Contains(t, csp, "https://fonts.gstatic.com")
}

func Test_Middleware_SwaggerUI_KeepsOnlyFrameAncestors(t *testing.T) {
	router := testRouter()
	router.GET("/api/v1/docs/swagger/*any", func(ctx *gin.Context) { ctx.Status(http.StatusOK) })

	req, _ := http.NewRequest(http.MethodGet, "/api/v1/docs/swagger/index.html", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, "frame-ancestors 'none'", recorder.Header().Get("Content-Security-Policy"))
	assert.Equal(t, "DENY", recorder.Header().Get("X-Frame-Options"))
}

func Test_Middleware_PlainHTTPRequest_OmitsHSTS(t *testing.T) {
	router := testRouter()

	req, _ := http.NewRequest(http.MethodGet, "/ping", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Empty(t, recorder.Header().Get("Strict-Transport-Security"),
		"a plain-HTTP-only deployment must not be told to force HTTPS")
}

func Test_Middleware_ForwardedHTTPS_SetsHSTS(t *testing.T) {
	router := testRouter()

	req, _ := http.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Contains(t, recorder.Header().Get("Strict-Transport-Security"), "max-age=31536000")
}

func Test_Middleware_ForwardedHTTP_OmitsHSTS(t *testing.T) {
	router := testRouter()

	req, _ := http.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set("X-Forwarded-Proto", "http")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Empty(t, recorder.Header().Get("Strict-Transport-Security"))
}
