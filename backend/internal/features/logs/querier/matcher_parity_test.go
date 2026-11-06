package logs_querier_test

import (
	"sort"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"

	"github.com/stretchr/testify/require"
)

// Test_Matches_AgreesWithWhereBuilder guards against drift between Live
// Tail's in-memory logs_core.Matches and whereBuilder's SQL: both must accept
// the same logs for every QueryNode.
func Test_Matches_AgreesWithWhereBuilder(t *testing.T) {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	logItems := []*logs_core.LogItem{
		{
			Timestamp: base,
			Level:     logs_core.LogLevelError,
			Message:   "connection timeout to database",
			ClientIP:  "10.0.0.1",
			TraceID:   "trace-1",
			Fields: map[string]any{
				"resource.k8s.namespace.name": "prod",
				"resource.service.name":       "api",
				"status_code":                 float64(500),
			},
		},
		{
			Timestamp: base.Add(1 * time.Minute),
			Level:     logs_core.LogLevelInfo,
			Message:   "request completed",
			ClientIP:  "10.0.0.2",
			Fields: map[string]any{
				"k8s.namespace.name": "staging", // bare-key fallback, no "resource." prefix
				"status_code":        float64(200),
			},
		},
		{
			Timestamp: base.Add(2 * time.Minute),
			Level:     logs_core.LogLevelWarn,
			Message:   "slow query detected",
			ClientIP:  "10.0.0.1",
			Fields: map[string]any{
				"resource.k8s.namespace.name": "prod",
			},
		},
		{
			Timestamp: base.Add(3 * time.Minute),
			Level:     logs_core.LogLevelDebug,
			Message:   "cache miss",
			// No fields: exercises the "absent" branches.
		},
	}

	projectID, querier := seedProject(t, logItems)

	cases := []struct {
		name  string
		query *logs_core.QueryNode
	}{
		{"level equals", condition("level", logs_core.ConditionOperatorEquals, "ERROR")},
		{"level not_equals", condition("level", logs_core.ConditionOperatorNotEquals, "ERROR")},
		{"message contains", condition("message", logs_core.ConditionOperatorContains, "query")},
		{"message not_contains", condition("message", logs_core.ConditionOperatorNotContains, "query")},
		{"client_ip in", condition("client_ip", logs_core.ConditionOperatorIn, []string{"10.0.0.1"})},
		{"client_ip not_in", condition("client_ip", logs_core.ConditionOperatorNotIn, []string{"10.0.0.1"})},
		{"trace_id exists", condition("trace_id", logs_core.ConditionOperatorExists, nil)},
		{"trace_id not_exists", condition("trace_id", logs_core.ConditionOperatorNotExists, nil)},
		{
			"promoted k8s_namespace_name equals (resource-prefixed)",
			condition("k8s_namespace_name", logs_core.ConditionOperatorEquals, "prod"),
		},
		{
			"promoted k8s_namespace_name equals (bare fallback)",
			condition("k8s_namespace_name", logs_core.ConditionOperatorEquals, "staging"),
		},
		{"promoted service_name not_exists", condition("service_name", logs_core.ConditionOperatorNotExists, nil)},
		{"custom field equals", condition("status_code", logs_core.ConditionOperatorEquals, "500")},
		{
			"custom field not_equals (absent counts as not-equal)",
			condition("status_code", logs_core.ConditionOperatorNotEquals, "500"),
		},
		{"custom field exists", condition("status_code", logs_core.ConditionOperatorExists, nil)},
		{"custom field not_exists", condition("status_code", logs_core.ConditionOperatorNotExists, nil)},
		{
			"timestamp greater_or_equal",
			condition(
				"timestamp",
				logs_core.ConditionOperatorGreaterOrEqual,
				base.Add(1*time.Minute).Format(time.RFC3339Nano),
			),
		},
		{
			"timestamp less_than",
			condition(
				"timestamp",
				logs_core.ConditionOperatorLessThan,
				base.Add(2*time.Minute).Format(time.RFC3339Nano),
			),
		},
		{
			"AND of level+message", logical(logs_core.LogicalOperatorAnd,
				condition("level", logs_core.ConditionOperatorNotEquals, "DEBUG"),
				condition("message", logs_core.ConditionOperatorContains, "o"),
			),
		},
		{
			"OR of two levels", logical(logs_core.LogicalOperatorOr,
				condition("level", logs_core.ConditionOperatorEquals, "ERROR"),
				condition("level", logs_core.ConditionOperatorEquals, "DEBUG"),
			),
		},
		{
			"NOT of a condition", logical(logs_core.LogicalOperatorNot,
				condition("level", logs_core.ConditionOperatorEquals, "ERROR"),
			),
		},
		{"nil query (no filter)", nil},
		{
			"pattern.hash equals (computed in Go vs SQL)",
			condition(
				logs_core.PatternHashField,
				logs_core.ConditionOperatorEquals,
				logs_core.PatternHash("slow query detected"),
			),
		},
		{
			"pattern.hash not_equals",
			condition(
				logs_core.PatternHashField,
				logs_core.ConditionOperatorNotEquals,
				logs_core.PatternHash("cache miss"),
			),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := queryRequest(tc.query, base.Add(-1*time.Hour), base.Add(1*time.Hour))
			request.Limit = 100
			response, err := querier.ExecuteQuery(projectID, request)
			require.NoError(t, err)
			storedMatches := messagesOf(response.Logs)
			sort.Strings(storedMatches)

			var liveMatches []string
			for _, item := range logItems {
				if logs_core.Matches(tc.query, item) {
					liveMatches = append(liveMatches, item.Message)
				}
			}
			sort.Strings(liveMatches)

			require.Equal(t, storedMatches, liveMatches,
				"logs_core.Matches (Live Tail) disagrees with whereBuilder's SQL (stored search) for query %q", tc.name)
		})
	}
}
