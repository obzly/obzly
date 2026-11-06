package mcp_server

import (
	"context"
	"errors"
	"testing"

	users_models "obzly/internal/features/users/models"
	"obzly/internal/util/rate_limit"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

type failingLimiter struct{}

func (failingLimiter) CheckRateLimit(uuid.UUID, int, int) (*rate_limit.RateLimitResult, error) {
	return nil, errors.New("valkey down")
}

func Test_ToolCall_WhenRateLimiterFails_IsRefused(t *testing.T) {
	previous := rateLimiter
	rateLimiter = failingLimiter{}
	t.Cleanup(func() { rateLimiter = previous })
	user := &users_models.User{ID: uuid.New()}
	request := &mcp.CallToolRequest{Extra: &mcp.RequestExtra{
		TokenInfo: &auth.TokenInfo{Extra: map[string]any{userExtraKey: user}},
	}}
	called := false
	handler := wrap("test_tool", func(context.Context, *users_models.User, struct{}) (any, error) {
		called = true
		return "ok", nil
	})

	_, _, err := handler(context.Background(), request, struct{}{})

	require.Error(t, err)
	require.False(t, called, "a tool must not run when the rate limit can't be checked")
}
