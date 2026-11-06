package logs_core

import "strings"

// LowercaseCondition returns cond with string values lowercased. Levels are
// stored lowercase and matched case-insensitively, so level:ERROR from an
// old saved search or API client still matches.
func LowercaseCondition(cond *ConditionNode) *ConditionNode {
	lowered := *cond
	switch value := cond.Value.(type) {
	case string:
		lowered.Value = strings.ToLower(value)
	case []string:
		out := make([]string, len(value))
		for i, v := range value {
			out[i] = strings.ToLower(v)
		}
		lowered.Value = out
	case []any:
		out := make([]any, len(value))
		for i, v := range value {
			if text, ok := v.(string); ok {
				out[i] = strings.ToLower(text)
			} else {
				out[i] = v
			}
		}
		lowered.Value = out
	}
	return &lowered
}
