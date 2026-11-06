package logs_receiving

import (
	"fmt"
	"testing"

	logs_core "obzly/internal/features/logs/core"
	projects_models "obzly/internal/features/projects/models"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Log head sampling: the first rule matching a log's service and level sets
// its rate, a trace's logs share one decision, unmatched logs are kept.
func Test_SampleLogs_AppliesTheFirstMatchingRule_PerTrace(t *testing.T) {
	rules := projects_models.LogSampleRules{
		{Service: "checkout", Level: "debug", Rate: 0},
		{Service: "", Level: "info", Rate: 0.25},
	}
	var logs []*logs_core.LogItem
	add := func(service, level, traceID string) *logs_core.LogItem {
		log := &logs_core.LogItem{
			ID: uuid.New(), Level: logs_core.LogLevel(level), TraceID: traceID,
			Fields: map[string]any{"service_name": service},
		}
		logs = append(logs, log)
		return log
	}
	debugCheckout := add("checkout", "DEBUG", "")
	warn := add("checkout", "warn", "")
	debugOther := add("search", "debug", "")
	for i := range 400 {
		traceID := fmt.Sprintf("%032x", i*7919)
		add("search", "info", traceID)
		add("checkout", "info", traceID)
	}
	for range 2000 {
		add("search", "info", "")
	}

	kept := sampleLogs(logs, rules)
	in := map[*logs_core.LogItem]bool{}
	for _, log := range kept {
		in[log] = true
	}
	require.False(t, in[debugCheckout], "rate 0 drops; levels match case-insensitively")
	require.True(t, in[warn], "no rule matches warn")
	require.True(t, in[debugOther], "the debug rule is for checkout only")

	byTrace := map[string][]bool{}
	untraced, untracedKept := 0, 0
	for _, log := range logs {
		if log.TraceID != "" {
			byTrace[log.TraceID] = append(byTrace[log.TraceID], in[log])
		} else if log.Level == "info" {
			untraced++
			if in[log] {
				untracedKept++
			}
		}
	}
	tracesKept := 0
	for traceID, decisions := range byTrace {
		require.Equal(t, decisions[0], decisions[1], "trace %s: its logs go together", traceID)
		if decisions[0] {
			tracesKept++
		}
	}
	require.InDelta(t, 0.25*400, tracesKept, 30)
	require.InDelta(t, 0.25*float64(untraced), untracedKept, 0.05*float64(untraced))
}

func Test_SampleLogs_NoRules_KeepsEverything(t *testing.T) {
	logs := []*logs_core.LogItem{{ID: uuid.New(), Level: "debug"}, {ID: uuid.New(), Level: "info"}}
	require.Len(t, sampleLogs(logs, nil), 2)
}
