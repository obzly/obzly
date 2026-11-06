package bodylimit_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"obzly/internal/util/bodylimit"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func Test_Middleware_BodyOverTheLimit_FailsToRead_UnlessTheRouteSetsItsOwn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	readAll := func(ctx *gin.Context) {
		body, err := io.ReadAll(ctx.Request.Body)
		if err != nil {
			ctx.Status(http.StatusRequestEntityTooLarge)
			return
		}
		ctx.String(http.StatusOK, "%d", len(body))
	}
	api := router.Group("/api", bodylimit.Middleware(10))
	api.POST("/default", readAll)
	api.POST("/own", bodylimit.Route(100), readAll)
	api.POST("/set", func(ctx *gin.Context) {
		bodylimit.Set(ctx, 5)
		readAll(ctx)
	})

	post := func(path string, size int) int {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(
			recorder,
			httptest.NewRequest(http.MethodPost, path, strings.NewReader(strings.Repeat("x", size))),
		)
		return recorder.Code
	}

	assert.Equal(t, http.StatusOK, post("/api/default", 10))
	assert.Equal(t, http.StatusRequestEntityTooLarge, post("/api/default", 11))
	assert.Equal(t, http.StatusOK, post("/api/own", 50), "a route's own limit replaces the default")
	assert.Equal(t, http.StatusRequestEntityTooLarge, post("/api/own", 101))
	assert.Equal(t, http.StatusRequestEntityTooLarge, post("/api/set", 6))
}
