// Package segments (this file) validates user-written SQL before it runs
// against a project-scoped TEMP TABLE the server materializes. Layers:
//  1. Exactly one statement: DuckDB's Go driver runs every ;-separated
//     statement in one call ("SELECT 1; DROP TABLE logs" drops the table).
//     A quote-aware scanner splits on top-level ';'; one trailing ';' is
//     trimmed.
//  2. Must be a SELECT.
//  3. A keyword blocklist (PRAGMA, SET, ATTACH, COPY, ...) as defense in
//     depth.
//  4. The caller runs it with enable_external_access=false (blocks file
//     reads, ATTACH, COPY TO), then lock_configuration=true so no SET can
//     raise memory_limit or the spill cap.
//  5. The caller has DuckDB's own parser confirm a single SELECT
//     (ParsesAsSingleSelect), so a scanner gap can't pass a second
//     statement.
//
// The scanner knows only '...' strings and "..." identifiers. DuckDB also
// lexes E'...' and $$...$$ / $tag$...$tag$ strings, which end elsewhere, so
// those forms, unterminated quotes and comments are refused, not parsed.
package segments

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// blockedKeywords are matched as whole words, case-insensitively. A SELECT-only
// query never needs them, so this stays a strict blocklist.
var blockedKeywords = []string{
	"pragma", "attach", "detach", "copy", "export", "import",
	"install", "load", "call", "create", "insert", "update", "delete",
	"drop", "alter", "execute", "prepare", "vacuum", "checkpoint",
	"set", "reset", "grant", "revoke",
}

var blockedKeywordPattern = regexp.MustCompile(
	`(?i)\b(` + strings.Join(blockedKeywords, "|") + `)\b`,
)

var errNotSelect = errors.New("query must be a single SELECT statement")

// scannedStatement is one ';'-separated statement, plus a copy with every
// quoted string and identifier blanked out for the keyword scan.
type scannedStatement struct {
	text     string
	redacted string
}

// ValidateSingleStatementSelect checks sqlText is exactly one SELECT with no
// blockedKeywords and returns the trimmed statement, ready to execute.
func ValidateSingleStatementSelect(sqlText string) (string, error) {
	statements, err := scanStatements(sqlText)
	if err != nil {
		return "", err
	}

	nonEmpty := make([]scannedStatement, 0, len(statements))
	for _, stmt := range statements {
		if strings.TrimSpace(stmt.text) != "" {
			nonEmpty = append(nonEmpty, stmt)
		}
	}

	if len(nonEmpty) == 0 {
		return "", errors.New("query is empty")
	}
	if len(nonEmpty) > 1 {
		return "", errors.New("only a single SQL statement is allowed (remove any ';' inside the query)")
	}

	statement := strings.TrimSpace(nonEmpty[0].text)

	if !strings.HasPrefix(strings.ToLower(statement), "select") {
		return "", errNotSelect
	}

	// Scan the redacted copy so string literals like 'delete the cache' don't
	// trip the blocklist.
	if match := blockedKeywordPattern.FindString(nonEmpty[0].redacted); match != "" {
		return "", fmt.Errorf("query contains a disallowed keyword %q", match)
	}

	return statement, nil
}

// ParsesAsSingleSelect has DuckDB's own parser confirm statement is exactly
// one SELECT. json_serialize_sql only parses; nothing runs.
func ParsesAsSingleSelect(db *sql.DB, statement string) error {
	var serialized string
	if err := db.QueryRow("SELECT json_serialize_sql(?::VARCHAR)::VARCHAR", statement).Scan(&serialized); err != nil {
		return fmt.Errorf("failed to parse query: %w", err)
	}
	var parsed struct {
		Error        bool              `json:"error"`
		ErrorMessage string            `json:"error_message"`
		Statements   []json.RawMessage `json:"statements"`
	}
	if err := json.Unmarshal([]byte(serialized), &parsed); err != nil {
		return fmt.Errorf("failed to parse query: %w", err)
	}
	if parsed.Error {
		if strings.Contains(parsed.ErrorMessage, "Only SELECT") {
			return errNotSelect
		}
		return fmt.Errorf("query does not parse: %s", parsed.ErrorMessage)
	}
	if len(parsed.Statements) != 1 {
		return errNotSelect
	}
	return nil
}

// scanStatements splits sqlText on top-level ';' (outside '...' and "...";
// a doubled quote is an escaped one) and refuses whatever this scanner
// could disagree with DuckDB about: comments, '$' (dollar-quoted strings,
// parameters), E'...' escape strings and an unterminated quote.
func scanStatements(sqlText string) ([]scannedStatement, error) {
	var statements []scannedStatement
	var text, redacted strings.Builder

	runes := []rune(sqlText)
	var quote rune

	for i := 0; i < len(runes); i++ {
		c := runes[i]

		if quote != 0 {
			text.WriteRune(c)
			switch {
			case c != quote:
				redacted.WriteRune(' ')
			case i+1 < len(runes) && runes[i+1] == quote:
				text.WriteRune(runes[i+1])
				redacted.WriteString("  ")
				i++
			default:
				quote = 0
				redacted.WriteRune(c)
			}
			continue
		}

		switch {
		case c == '\'' && startsEscapeString(runes, i):
			return nil, errors.New("escape strings (E'...') are not allowed in this query: use a plain '...' string")
		case c == '\'' || c == '"':
			quote = c
		case c == '$':
			return nil, errors.New("'$' is only allowed inside a quoted string in this query")
		case c == '-' && i+1 < len(runes) && runes[i+1] == '-',
			c == '/' && i+1 < len(runes) && runes[i+1] == '*':
			return nil, errors.New("comments (-- or /* */) are not allowed in this query")
		case c == ';':
			statements = append(statements, scannedStatement{text.String(), redacted.String()})
			text.Reset()
			redacted.Reset()
			continue
		}
		text.WriteRune(c)
		redacted.WriteRune(c)
	}

	if quote != 0 {
		return nil, errors.New("query has an unterminated quote")
	}
	if strings.TrimSpace(text.String()) != "" || len(statements) == 0 {
		statements = append(statements, scannedStatement{text.String(), redacted.String()})
	}

	return statements, nil
}

// startsEscapeString reports whether the quote at runes[i] opens an E'...'
// string: an E that is a token of its own or ends a number ("1e'x'" lexes
// as 1 then E'x'), not the end of an identifier as in DATE'2026-01-01'.
func startsEscapeString(runes []rune, i int) bool {
	if i == 0 || (runes[i-1] != 'e' && runes[i-1] != 'E') {
		return false
	}
	start := i - 1
	for start > 0 && isIdentifierRune(runes[start-1]) {
		start--
	}
	return start == i-1 || unicode.IsDigit(runes[start])
}

func isIdentifierRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}
