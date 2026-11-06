package alerting

import (
	"context"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"
	spans_core "obzly/internal/features/spans/core"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Logs and traces rules read the window through the existing aggregations:
// error logs per service, and error spans.
func Test_Evaluate_LogsAndTracesRules(t *testing.T) {
	f := newFixture(t, "https://example.com/hook", newSender())
	now := time.Now().UTC()
	var logs []*logs_core.LogItem
	for i := range 6 {
		service := "web"
		if i%3 == 0 {
			service = "api"
		}
		logs = append(logs, &logs_core.LogItem{
			ID: uuid.New(), ProjectID: f.projectID, Timestamp: now.Add(-time.Duration(i) * time.Second),
			Level: logs_core.LogLevelError, Message: "boom", Fields: map[string]any{"service_name": service},
		})
	}
	require.NoError(
		t,
		logs_core.GetLogCoreRepository().StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{f.projectID: logs}),
	)
	require.NoError(t, logs_core.GetLogCoreRepository().ForceFlush())
	var spans []*spans_core.Span
	for i := range 3 {
		start := now.Add(-time.Duration(i) * time.Second)
		spans = append(spans, &spans_core.Span{
			ID: uuid.New(), ProjectID: f.projectID, TraceID: uuid.NewString(), SpanID: uuid.NewString()[:16],
			Name: "GET /", ServiceName: "web", StartTime: start, EndTime: start.Add(time.Millisecond),
			StatusCode: spans_core.StatusCodeError,
		})
	}
	require.NoError(
		t,
		spans_core.GetSpanCoreRepository().StoreSpansBatch(map[uuid.UUID][]*spans_core.Span{f.projectID: spans}),
	)
	require.NoError(t, spans_core.GetSpanCoreRepository().ForceFlush())
	f.clock = now.Add(time.Second)

	errorLogs := &logs_core.QueryNode{Type: logs_core.QueryNodeTypeCondition, Condition: &logs_core.ConditionNode{
		Field: "level", Operator: logs_core.ConditionOperatorEquals, Value: "error",
	}}
	logRule := f.rule(t, "", 0, 0)
	logRule.Source, logRule.Query = SourceLogs, RuleQuery{Logs: errorLogs, GroupBy: "service_name"}
	logRule.Comparison, logRule.Threshold = ">=", 3
	traceRule := f.rule(t, "", 0, 0)
	traceRule.Source, traceRule.Query = SourceTraces, RuleQuery{Metric: "error_count"}
	traceRule.Comparison, traceRule.Threshold = ">", 2
	for _, rule := range []*AlertRule{logRule, traceRule} {
		require.NoError(t, f.repo.saveRule(rule))
		require.NoError(t, f.evaluator.Evaluate(context.Background(), rule))
	}

	logStates, err := f.repo.states(logRule.ID)
	require.NoError(t, err)
	require.Len(t, logStates, 1, "web has 4 errors, api 2")
	require.Equal(t, "web", logStates[0].Labels["service_name"])
	require.Equal(t, StateFiring, logStates[0].State)
	require.Equal(t, 4.0, logStates[0].Value)

	traceStates, err := f.repo.states(traceRule.ID)
	require.NoError(t, err)
	require.Len(t, traceStates, 1)
	require.Equal(t, 3.0, traceStates[0].Value)

	stored, err := f.repo.rule(f.projectID, logRule.ID)
	require.NoError(t, err)
	require.Empty(t, stored.LastError)
	require.NotNil(t, stored.LastEvalAt)
}

// A failing query records the error and changes no state.
func Test_Evaluate_QueryError_KeepsStates(t *testing.T) {
	f := newFixture(t, "https://example.com/hook", newSender())
	rule := f.rule(t, "vector(1)", 0, 0)
	require.NoError(t, f.evaluator.Evaluate(context.Background(), rule))
	states, _ := f.repo.states(rule.ID)
	require.Len(t, states, 1)
	rule.Query.PromQL = "rate(not_a_range)"
	f.clock = f.clock.Add(time.Minute)
	require.NoError(t, f.evaluator.Evaluate(context.Background(), rule))
	stored, err := f.repo.rule(f.projectID, rule.ID)
	require.NoError(t, err)
	require.NotEmpty(t, stored.LastError)
	states, _ = f.repo.states(rule.ID)
	require.Len(t, states, 1)
	require.Equal(t, StateFiring, states[0].State)
}

// A log patterns rule fires per error pattern that is new against its
// baseline or rose to Threshold times its rate; a pattern that holds
// doesn't.
func Test_Evaluate_LogPatternsRule_FiresForNewAndSpikingPatterns(t *testing.T) {
	f := newFixture(t, "https://example.com/hook", newSender())
	now := time.Now().UTC().Truncate(time.Second)
	var logs []*logs_core.LogItem
	add := func(ago time.Duration, n int, message string) {
		for i := range n {
			logs = append(logs, &logs_core.LogItem{
				ID: uuid.New(), ProjectID: f.projectID, Timestamp: now.Add(-ago + time.Duration(i)*time.Second),
				Level: logs_core.LogLevelError, Message: message,
			})
		}
	}
	// Baseline: the hour before the 5-minute window.
	add(30*time.Minute, 6, "db timeout after 5000ms")
	add(40*time.Minute, 6, "cache miss for key 42")
	// Window.
	add(3*time.Minute, 6, "db timeout after 5000ms")      // ~70x its baseline rate
	add(3*time.Minute, 7, "payment failed: upstream 301") // new
	add(4*time.Minute, 1, "cache miss for key 42")        // below the minimum count
	require.NoError(
		t,
		logs_core.GetLogCoreRepository().StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{f.projectID: logs}),
	)
	require.NoError(t, logs_core.GetLogCoreRepository().ForceFlush())
	f.clock = now

	rule := f.rule(t, "", 0, 0)
	rule.Source, rule.WindowSeconds = SourceLogPatterns, 300
	rule.Query = RuleQuery{BaselineSeconds: 3600, MinCount: 5}
	rule.Comparison, rule.Threshold = ">=", 3
	require.NoError(t, f.repo.saveRule(rule))
	require.NoError(t, f.evaluator.Evaluate(context.Background(), rule))

	states, err := f.repo.states(rule.ID)
	require.NoError(t, err)
	byChange := map[string]*AlertState{}
	for _, state := range states {
		byChange[state.Labels["change"]] = state
	}
	require.Len(t, states, 2, "%v", states)
	require.Equal(t, "payment failed: upstream <num>", byChange["new"].Labels["pattern"])
	require.Equal(t, StateFiring, byChange["new"].State)
	require.Equal(t, "db timeout after <num>", byChange["spike"].Labels["pattern"])
	require.Greater(t, byChange["spike"].Value, 3.0)
}
