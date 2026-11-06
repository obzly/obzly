package downdetect_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"obzly/internal/downdetect"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func Test_IsAvailable_AllBackendsReachable_ReturnsOK(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	v1 := router.Group("/api/v1")
	downdetect.GetDowndetectController().RegisterRoutes(v1)

	req, _ := http.NewRequest(http.MethodGet, "/api/v1/downdetect/is-available", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
}
