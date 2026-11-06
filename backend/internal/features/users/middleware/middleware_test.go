package users_middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	users_enums "obzly/internal/features/users/enums"
	users_services "obzly/internal/features/users/services"
	users_testing "obzly/internal/features/users/testing"
)

// A failed revocation lookup (Valkey unreachable) must be a retryable 503, not
// a 401 that signs users out.
func Test_AuthMiddleware_WhenTheRevocationCheckFails_Returns503(t *testing.T) {
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	restore := users_services.SetTokenRevocationCheckForTest(func(string) (bool, error) {
		return false, errors.New("dial tcp 10.0.0.1:6379: i/o timeout")
	})
	t.Cleanup(restore)

	recorder := authenticatedRequest(user.Token)

	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.Equal(t, "5", recorder.Header().Get("Retry-After"))
}

func Test_AuthMiddleware_WhenTheTokenIsRevoked_Returns401(t *testing.T) {
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	restore := users_services.SetTokenRevocationCheckForTest(func(string) (bool, error) { return true, nil })
	t.Cleanup(restore)

	recorder := authenticatedRequest(user.Token)

	require.Equal(t, http.StatusUnauthorized, recorder.Code)
}

func Test_AuthMiddleware_WithAMalformedToken_Returns401(t *testing.T) {
	recorder := authenticatedRequest("not-a-jwt")

	require.Equal(t, http.StatusUnauthorized, recorder.Code)
}

func Test_AuthMiddleware_WithAValidToken_PassesThrough(t *testing.T) {
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)

	recorder := authenticatedRequest(user.Token)

	require.Equal(t, http.StatusOK, recorder.Code)
}

func authenticatedRequest(token string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/probe", AuthMiddleware(users_services.GetUserService()), func(ctx *gin.Context) {
		ctx.Status(http.StatusOK)
	})
	request := httptest.NewRequest(http.MethodGet, "/probe", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}
