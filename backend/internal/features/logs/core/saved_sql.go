package logs_core

import (
	"errors"
	"fmt"
	"strings"

	"obzly/internal/storage/segments"
)

// MaxSQLQueryLength caps user-written SQL (SQL mode, SQL panels and rules):
// a huge string burns server time before it is refused.
const MaxSQLQueryLength = 10_000

// ValidateSavedSQL checks SQL a dashboard panel or alert rule stores with the
// same static checks SQL mode runs (single SELECT, keyword and function
// blocklists), so a refused statement fails on save rather than on every
// render or evaluation. DuckDB's own parse still runs when it executes.
func ValidateSavedSQL(sql string) (string, error) {
	sql = strings.TrimSpace(sql)
	if sql == "" {
		return "", errors.New("a SQL query is required")
	}
	if len(sql) > MaxSQLQueryLength {
		return "", fmt.Errorf("the SQL query exceeds the %d character limit", MaxSQLQueryLength)
	}
	if _, err := segments.ValidateSingleStatementSelect(sql); err != nil {
		return "", fmt.Errorf("invalid SQL query: %w", err)
	}
	return sql, nil
}
