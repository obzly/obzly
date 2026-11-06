package spans_querier

import (
	"context"
	"database/sql"
	"fmt"

	spans_core "obzly/internal/features/spans/core"
	"obzly/internal/query"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
)

// maxLLMResultRows caps what one LLM calls statement returns.
const maxLLMResultRows = 10_000

// QueryLLMCalls runs request.SQL over "llm", the project's LLM call spans in
// the range. Fragments extract only the columns in spans_core.LLMCallColumns
// (never content attributes such as gen_ai.input.messages), so neither the
// statement nor its caller can reach message text.
func (q *SpanQuerier) QueryLLMCalls(
	ctx context.Context,
	projectID uuid.UUID,
	request *spans_core.LLMCallsQueryDTO,
) (*spans_core.LLMCallsResultDTO, error) {
	from := clampToRepresentableRange(request.From)
	to := clampToRepresentableRange(request.To)
	result := &spans_core.LLMCallsResultDTO{Rows: [][]any{}}
	tail, records, err := q.inputs(projectID, from, to, segments.PruningHints{})
	if err != nil {
		return nil, err
	}

	extract := llmCallsSQL()
	whereArgs := []any{projectID.String(), from, to}
	_, err = q.runContext(ctx, query.Request{
		Segments:     records,
		LocalFiles:   tail,
		FragmentSQL:  extract,
		FragmentArgs: whereArgs,
		FinalSQL:     "WITH llm AS (SELECT * FROM " + query.ResultsPlaceholder + ")\n" + request.SQL,
		FinalArgs:    request.Args,
		SingleSQL:    "WITH llm AS (" + extract + ")\n" + request.SQL,
		SingleArgs:   append(append([]any{}, whereArgs...), request.Args...),
		ScanBudget:   request.ScanBudget,
	}, func(rows *sql.Rows) error {
		columns, err := rows.Columns()
		if err != nil {
			return err
		}
		for rows.Next() {
			if len(result.Rows) >= maxLLMResultRows {
				break
			}
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				return fmt.Errorf("failed to scan LLM calls row: %w", err)
			}
			result.Rows = append(result.Rows, values)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query LLM calls: %w", err)
	}
	return result, nil
}

// llmCallsSQL extracts LLM call spans, with the GenAI semantic conventions'
// current attribute names first and the older ones as fallbacks.
func llmCallsSQL() string {
	attr := func(key string) string { return "json_extract_string(attributes, '$." + key + "')" }
	num := func(key string) string { return "TRY_CAST(" + attr(key) + " AS DOUBLE)" }
	return `SELECT
	start_time AS ts,
	service_name AS service,
	` + attr("gen_ai_operation_name") + ` AS operation,
	coalesce(nullif(` + attr("gen_ai_request_model") + `, ''), ` + attr("gen_ai_response_model") + `, '') AS model,
	coalesce(` + attr("gen_ai_response_model") + `, '') AS response_model,
	coalesce(nullif(` + attr("gen_ai_provider_name") + `, ''), ` + attr("gen_ai_system") + `, '') AS provider,
	coalesce(` + num("gen_ai_usage_input_tokens") + `, ` + num("gen_ai_usage_prompt_tokens") + `) AS input_tokens,
	coalesce(` + num("gen_ai_usage_output_tokens") + `, ` + num("gen_ai_usage_completion_tokens") + `) AS output_tokens,
	` + num("gen_ai_usage_cache_read_input_tokens") + ` AS cached_tokens,
	(status_code = 'ERROR' OR ` + attr("error_type") + ` IS NOT NULL) AS is_error,
	coalesce(` + attr("error_type") + `, '') AS error_type,
	coalesce(` + attr("gen_ai_response_finish_reasons") + `, '') AS finish_reasons,
	(epoch_ns(end_time) - epoch_ns(start_time)) / 1000000.0 AS duration_ms,
	trace_id,
	span_id,
	coalesce(` + attr("server_address") + `, '') AS server_address,
	coalesce(` + attr("user_id") + `, ` + attr("enduser_id") + `, '') AS user_id
FROM ` + query.SourcePlaceholder + `
WHERE project_id = ? AND start_time >= ? AND start_time <= ?
	AND contains(attributes, 'gen_ai_operation_name') AND ` + attr("gen_ai_operation_name") + ` IS NOT NULL`
}
