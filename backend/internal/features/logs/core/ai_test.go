package logs_core

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func Test_SplitAIEvents_ByClaudeCodeScope_SeparatesAIEvents(t *testing.T) {
	projectID := uuid.New()
	infra := &LogItem{Message: "GET /health", Fields: map[string]any{"scope_name": "io.opentelemetry.http"}}
	ai := &LogItem{
		Message: "claude_code.api_request",
		Fields:  map[string]any{"scope_name": "com.anthropic.claude_code.events"},
	}
	plain := &LogItem{Message: "no fields"}

	logs, aiEvents := SplitAIEvents(map[uuid.UUID][]*LogItem{projectID: {infra, ai, plain}})

	assert.Equal(t, []*LogItem{infra, plain}, logs[projectID])
	assert.Equal(t, []*LogItem{ai}, aiEvents[projectID])
}
