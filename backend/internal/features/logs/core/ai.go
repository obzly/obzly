package logs_core

import (
	"strings"

	"github.com/google/uuid"
)

const (
	StreamLogs = "logs"
	// StreamAILogs holds AI tool telemetry (Claude Code events) apart from
	// "logs", so Search, patterns and live tail only show infra/app logs.
	StreamAILogs = "ai_logs"
	// StreamLogsRollup holds per-minute log counts of compacted logs
	// segments, one rollup segment per source (logs_compactor).
	StreamLogsRollup = "logs_1m"

	// Claude Code's OTel instrumentation scope, flattened into fields at ingest.
	claudeCodeScope = "com.anthropic.claude_code"
)

// IsAIEvent reports whether a log item belongs in StreamAILogs.
func IsAIEvent(item *LogItem) bool {
	scope, _ := item.Fields["scope_name"].(string)
	return strings.HasPrefix(scope, claudeCodeScope)
}

// SplitAIEvents separates AI events from ordinary logs, keeping per-project grouping.
func SplitAIEvents(batch map[uuid.UUID][]*LogItem) (logs, ai map[uuid.UUID][]*LogItem) {
	logs = make(map[uuid.UUID][]*LogItem, len(batch))
	ai = map[uuid.UUID][]*LogItem{}
	for projectID, items := range batch {
		for _, item := range items {
			if IsAIEvent(item) {
				ai[projectID] = append(ai[projectID], item)
			} else {
				logs[projectID] = append(logs[projectID], item)
			}
		}
	}
	return logs, ai
}
