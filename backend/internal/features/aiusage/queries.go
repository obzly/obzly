package aiusage

import (
	"fmt"
	"strings"

	logs_core "obzly/internal/features/logs/core"
)

// Claude Code's OTel event names (the log body) and attribute keys, as stored
// after attrnames normalization. Verified against real Claude Code 2.1 output.
const (
	eventPrefix       = "claude_code."
	eventAPIRequest   = "claude_code.api_request"
	eventAPIError     = "claude_code.api_error"
	eventUserPrompt   = "claude_code.user_prompt"
	eventToolResult   = "claude_code.tool_result"
	eventToolDecision = "claude_code.tool_decision"
	maxBreakdownRows  = 100
	maxErrorRows      = 50
	maxSessionEvents  = 500
	maxEventsPage     = 200
)

var sessionSortColumns = map[string]string{
	"lastSeen": "last_seen",
	"cost":     "cost_usd",
	"requests": "requests",
	"tokens":   "tokens",
}

var timeseriesGroupFields = map[string]string{
	"model": "model",
	"user":  "user_email",
}

func overviewStatements(request *OverviewRequest) []logs_core.ScopedSQLStatement {
	groupField := timeseriesGroupFields[request.GroupBy]
	if groupField == "" {
		groupField = "model"
	}

	cte, args := eventsCTE(&request.UsageFilter)
	req := quote(eventAPIRequest)

	summary := cte + `SELECT
	count(*) FILTER (WHERE ev = ` + req + `),
	coalesce(sum(` + num("cost_usd_micros") + `) FILTER (WHERE ev = ` + req + `), 0) / 1e6,
	coalesce(sum(` + num("input_tokens") + `) FILTER (WHERE ev = ` + req + `), 0),
	coalesce(sum(` + num("output_tokens") + `) FILTER (WHERE ev = ` + req + `), 0),
	coalesce(sum(` + num("cache_read_tokens") + `) FILTER (WHERE ev = ` + req + `), 0),
	coalesce(sum(` + num("cache_creation_tokens") + `) FILTER (WHERE ev = ` + req + `), 0),
	count(DISTINCT ` + str("session_id") + `),
	count(DISTINCT ` + str("user_email") + `),
	count(*) FILTER (WHERE ev = ` + quote(eventUserPrompt) + `),
	count(*) FILTER (WHERE ev = ` + quote(eventAPIError) + `),
	count(*) FILTER (WHERE ev = ` + quote(eventToolResult) + `),
	coalesce(avg(` + num("duration_ms") + `) FILTER (WHERE ev = ` + req + `), 0),
	coalesce(quantile_cont(` + num("ttft_ms") + `, 0.5) FILTER (WHERE ev = ` + req + `), 0),
	coalesce(epoch_ms(max(ts)::TIMESTAMP), 0)
FROM e`

	timeseries := cte + `SELECT
	epoch_ms(time_bucket(to_seconds(?::BIGINT), ts::TIMESTAMP)) AS bucket,
	coalesce(` + str(groupField) + `, '') AS key,
	coalesce(sum(` + num("cost_usd_micros") + `), 0) / 1e6,
	coalesce(sum(` + tokensExpr() + `), 0),
	count(*)
FROM e WHERE ev = ` + req + `
GROUP BY 1, 2 ORDER BY 1, 2`

	tools := cte + `SELECT
	` + str("tool_name") + ` AS tool,
	count(*) FILTER (WHERE ev = ` + quote(eventToolResult) + `),
	count(*) FILTER (WHERE ev = ` + quote(eventToolResult) + ` AND ` + str("success") + ` = 'false'),
	count(*) FILTER (WHERE ev = ` + quote(eventToolDecision) + ` AND ` + str("decision") + ` = 'reject'),
	coalesce(avg(` + num("duration_ms") + `) FILTER (WHERE ev = ` + quote(eventToolResult) + `), 0),
	coalesce(quantile_cont(` + num("duration_ms") + `, 0.95) FILTER (WHERE ev = ` + quote(eventToolResult) + `), 0)
FROM e WHERE ev IN (` + quote(eventToolResult) + `, ` + quote(eventToolDecision) + `) AND ` + str("tool_name") + ` IS NOT NULL
GROUP BY 1 ORDER BY 2 DESC LIMIT ` + fmt.Sprint(maxBreakdownRows)

	errors := cte + `SELECT
	coalesce(` + str("status_code") + `, ''),
	left(coalesce(` + str("error") + `, ''), 500),
	coalesce(` + str("model") + `, ''),
	count(*),
	epoch_ms(max(ts)::TIMESTAMP)
FROM e WHERE ev = ` + quote(eventAPIError) + `
GROUP BY 1, 2, 3 ORDER BY 4 DESC LIMIT ` + fmt.Sprint(maxErrorRows)

	options := `SELECT
	list_sort(list(DISTINCT json_extract_string(fields, '$.user_email')) FILTER (WHERE json_extract_string(fields, '$.user_email') IS NOT NULL)),
	list_sort(list(DISTINCT json_extract_string(fields, '$.model')) FILTER (WHERE json_extract_string(fields, '$.model') IS NOT NULL)),
	list_sort(list(DISTINCT json_extract_string(fields, '$.terminal_type')) FILTER (WHERE json_extract_string(fields, '$.terminal_type') IS NOT NULL))
FROM logs`

	return []logs_core.ScopedSQLStatement{
		{SQL: summary, Args: args},
		{SQL: timeseries, Args: append(append([]any{}, args...), request.BucketSeconds)},
		{SQL: breakdownSQL(cte, "user_email"), Args: args},
		{SQL: breakdownSQL(cte, "model"), Args: args},
		{SQL: tools, Args: args},
		{SQL: errors, Args: args},
		{SQL: options},
	}
}

func sessionsStatements(request *SessionsRequest) []logs_core.ScopedSQLStatement {
	sortColumn := sessionSortColumns[request.SortBy]
	if sortColumn == "" {
		sortColumn = "last_seen"
	}

	cte, args := eventsCTE(&request.UsageFilter)
	where := str("session_id") + " IS NOT NULL"
	if request.Search != "" {
		where += " AND (contains(lower(" + str("session_id") + "), lower(?)) OR contains(lower(coalesce(" +
			str("user_email") + ", '')), lower(?)))"
		args = append(args, request.Search, request.Search)
	}
	req := quote(eventAPIRequest)

	page := cte + `SELECT
	` + str("session_id") + ` AS session_id,
	coalesce(max(` + str("user_email") + `), ''),
	coalesce(max(` + str("terminal_type") + `), ''),
	epoch_ms(min(ts)::TIMESTAMP),
	epoch_ms(max(ts)::TIMESTAMP) AS last_seen,
	count(*) FILTER (WHERE ev = ` + req + `) AS requests,
	coalesce(sum(` + num("cost_usd_micros") + `) FILTER (WHERE ev = ` + req + `), 0) / 1e6 AS cost_usd,
	coalesce(sum(` + tokensExpr() + `) FILTER (WHERE ev = ` + req + `), 0) AS tokens,
	count(*) FILTER (WHERE ev = ` + quote(eventUserPrompt) + `),
	count(*) FILTER (WHERE ev = ` + quote(eventToolResult) + `),
	count(*) FILTER (WHERE ev = ` + quote(eventAPIError) + `),
	list_sort(list(DISTINCT ` + str("model") + `) FILTER (WHERE ` + str("model") + ` IS NOT NULL))
FROM e WHERE ` + where + `
GROUP BY 1 ORDER BY ` + sortColumn + ` DESC, 1 LIMIT ? OFFSET ?`

	total := cte + `SELECT count(DISTINCT ` + str("session_id") + `) FROM e WHERE ` + where

	return []logs_core.ScopedSQLStatement{
		{SQL: page, Args: append(append([]any{}, args...), request.Limit, request.Offset)},
		{SQL: total, Args: args},
	}
}

func sessionEventsStatement(sessionID string) logs_core.ScopedSQLStatement {
	return logs_core.ScopedSQLStatement{
		SQL: `SELECT epoch_ms(timestamp::TIMESTAMP), message, fields FROM logs
WHERE json_extract_string(fields, '$.session_id') = ?
ORDER BY timestamp LIMIT ` + fmt.Sprint(maxSessionEvents),
		Args: []any{sessionID},
	}
}

func eventsStatements(request *EventsRequest) []logs_core.ScopedSQLStatement {
	cte, args := eventsCTE(&request.UsageFilter)
	where := "true"
	filterArgs := append([]any{}, args...)
	if request.Event != "" {
		where += " AND ev = ?"
		filterArgs = append(filterArgs, eventPrefix+request.Event)
	}
	if request.SessionID != "" {
		where += " AND " + str("session_id") + " = ?"
		filterArgs = append(filterArgs, request.SessionID)
	}
	if request.Search != "" {
		where += " AND contains(lower(f::VARCHAR), lower(?))"
		filterArgs = append(filterArgs, request.Search)
	}

	page := cte + `SELECT epoch_ms(ts::TIMESTAMP), ev, f::VARCHAR FROM e WHERE ` + where + `
ORDER BY ts DESC LIMIT ? OFFSET ?`
	total := cte + `SELECT count(*) FROM e WHERE ` + where
	types := cte + `SELECT DISTINCT ev FROM e ORDER BY 1`

	return []logs_core.ScopedSQLStatement{
		{SQL: page, Args: append(append([]any{}, filterArgs...), request.Limit, request.Offset)},
		{SQL: total, Args: filterArgs},
		{SQL: types, Args: args},
	}
}

func breakdownSQL(cte, field string) string {
	req := quote(eventAPIRequest)
	return cte + `SELECT
	` + str(field) + ` AS key,
	count(*),
	coalesce(sum(` + num("cost_usd_micros") + `), 0) / 1e6 AS cost_usd,
	coalesce(sum(` + num("input_tokens") + `), 0),
	coalesce(sum(` + num("output_tokens") + `), 0),
	coalesce(sum(` + num("cache_read_tokens") + `), 0),
	coalesce(sum(` + num("cache_creation_tokens") + `), 0),
	count(DISTINCT ` + str("session_id") + `),
	epoch_ms(max(ts)::TIMESTAMP)
FROM e WHERE ev = ` + req + ` AND ` + str(field) + ` IS NOT NULL
GROUP BY 1 ORDER BY cost_usd DESC LIMIT ` + fmt.Sprint(maxBreakdownRows)
}

// eventsCTE parses fields once per row and applies the filters. A model
// filter keeps only events that carry a model (requests, errors, responses).
func eventsCTE(filter *UsageFilter) (string, []any) {
	conditions := []string{}
	args := []any{}
	for _, f := range []struct{ field, value string }{
		{"user_email", filter.User},
		{"model", filter.Model},
		{"terminal_type", filter.Terminal},
	} {
		if f.value != "" {
			conditions = append(conditions, "json_extract_string(fields, '$."+f.field+"') = ?")
			args = append(args, f.value)
		}
	}

	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}

	return "WITH e AS (SELECT timestamp AS ts, message AS ev, fields::JSON AS f FROM logs" + where + ")\n", args
}

func str(field string) string {
	return "json_extract_string(f, '$." + field + "')"
}

func num(field string) string {
	return "TRY_CAST(" + str(field) + " AS DOUBLE)"
}

func tokensExpr() string {
	return "coalesce(" + num("input_tokens") + ", 0) + coalesce(" + num("output_tokens") + ", 0) + coalesce(" +
		num("cache_read_tokens") + ", 0) + coalesce(" + num("cache_creation_tokens") + ", 0)"
}

func quote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
