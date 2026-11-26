package dashboards_services

import (
	"errors"
	"fmt"
	"strings"

	logs_core "obzly/internal/features/logs/core"
	metrics_promql "obzly/internal/features/metrics/promql"
)

// ErrPanelQueryFailed marks a panel query that failed in storage (a segment
// that can't be read, an executor gone). What it wraps can name segment
// files, fragments and executors, so the controller logs it and answers
// with this text alone. A scan budget refusal, a full limiter and a timeout
// stay in the chain: the controller looks for them first.
var ErrPanelQueryFailed = errors.New("failed to execute panel query")

// panelQueryError is a storage call's error as the panel's. What the caller
// can fix keeps its text and nothing else (a mistake in the panel's SQL, a
// query that needs more memory, a logs validation error); the rest is
// ErrPanelQueryFailed.
func panelQueryError(err error) error {
	if errors.Is(err, logs_core.ErrSQLOutOfMemory) {
		// Without DuckDB's own text, which can name the spill directory.
		return fmt.Errorf("%s: %w", ErrPanelQueryFailed.Error(), logs_core.ErrSQLOutOfMemory)
	}
	if errors.Is(err, logs_core.ErrInvalidAggregation) {
		// Refused before storage is read: the text names the panel's own fields.
		return fmt.Errorf("%s: %w", ErrPanelQueryFailed.Error(), err)
	}
	var validation *logs_core.ValidationError
	if errors.As(err, &validation) {
		return fmt.Errorf("%s: %s", ErrPanelQueryFailed.Error(), validation.Message)
	}
	// SQL mode's "invalid query: ..." (the statement guard, DuckDB's parser
	// and binder) is written for the user, as the Logs page shows it.
	if text := err.Error(); strings.HasPrefix(text, "invalid query") {
		return fmt.Errorf("%s: %s", ErrPanelQueryFailed.Error(), text)
	}
	return fmt.Errorf("%w: %w", ErrPanelQueryFailed, err)
}

// metricPanelError is a PromQL panel's error: the expression's own (a parse
// or evaluation error, a limit) keeps its text after prefix, a storage
// failure is ErrPanelQueryFailed.
func metricPanelError(prefix string, err error) error {
	message, ok := metrics_promql.ClientMessage(err)
	if !ok {
		return fmt.Errorf("%w: %w", ErrPanelQueryFailed, err)
	}
	return fmt.Errorf("%s: %s", prefix, message)
}
