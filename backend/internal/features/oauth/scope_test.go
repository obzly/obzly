package oauth

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_NormalizeScope_WriteAlwaysComesWithRead(t *testing.T) {
	for requested, want := range map[string]string{
		"":                     "mcp:read",
		"mcp:read":             "mcp:read",
		"  mcp:read  mcp:read": "mcp:read",
		"mcp:write":            "mcp:read mcp:write",
		"mcp:read mcp:write":   "mcp:read mcp:write",
		"mcp:write mcp:read":   "mcp:read mcp:write",
	} {
		got, ok := normalizeScope(requested)
		assert.True(t, ok, requested)
		assert.Equal(t, want, got, requested)
	}
	for _, refused := range []string{"mcp:admin", "mcp:read openid", "MCP:WRITE", "mcp:write:all"} {
		_, ok := normalizeScope(refused)
		assert.False(t, ok, refused)
	}
}
