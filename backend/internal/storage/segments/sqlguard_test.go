package segments_test

import (
	"database/sql"
	"testing"

	"obzly/internal/storage/segments"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_ValidateSingleStatementSelect_PlainSelect_Accepted(t *testing.T) {
	stmt, err := segments.ValidateSingleStatementSelect("SELECT * FROM logs WHERE level = 'ERROR'")
	require.NoError(t, err)
	assert.Equal(t, "SELECT * FROM logs WHERE level = 'ERROR'", stmt)
}

func Test_ValidateSingleStatementSelect_CaseInsensitiveSelect_Accepted(t *testing.T) {
	_, err := segments.ValidateSingleStatementSelect("select 1")
	require.NoError(t, err)
}

func Test_ValidateSingleStatementSelect_TrailingSemicolon_Trimmed(t *testing.T) {
	stmt, err := segments.ValidateSingleStatementSelect("SELECT 1;")
	require.NoError(t, err)
	assert.Equal(t, "SELECT 1", stmt)
}

func Test_ValidateSingleStatementSelect_TrailingSemicolonWithWhitespace_Trimmed(t *testing.T) {
	stmt, err := segments.ValidateSingleStatementSelect("SELECT 1;   \n  ")
	require.NoError(t, err)
	assert.Equal(t, "SELECT 1", stmt)
}

func Test_ValidateSingleStatementSelect_SemicolonInsideStringLiteral_NotSplit(t *testing.T) {
	stmt, err := segments.ValidateSingleStatementSelect(
		`SELECT * FROM logs WHERE message = 'error; retrying'`,
	)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM logs WHERE message = 'error; retrying'`, stmt)
}

func Test_ValidateSingleStatementSelect_MultipleStatements_Rejected(t *testing.T) {
	_, err := segments.ValidateSingleStatementSelect("SELECT 1; DROP TABLE logs")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "single SQL statement")
}

func Test_ValidateSingleStatementSelect_EscapedQuoteInsideString_HandledCorrectly(t *testing.T) {
	// A doubled '' is an escaped quote, so the ';' after it is still inside the
	// string.
	stmt, err := segments.ValidateSingleStatementSelect(
		`SELECT * FROM logs WHERE message = 'it''s broken; again'`,
	)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM logs WHERE message = 'it''s broken; again'`, stmt)
}

func Test_ValidateSingleStatementSelect_NotASelect_Rejected(t *testing.T) {
	for _, sql := range []string{
		"DROP TABLE logs",
		"INSERT INTO logs VALUES (1)",
		"UPDATE logs SET level = 'ERROR'",
		"DELETE FROM logs",
		"PRAGMA database_list",
		"ATTACH '/tmp/x.db' AS x",
		"",
		"   ",
	} {
		_, err := segments.ValidateSingleStatementSelect(sql)
		assert.Error(t, err, "expected %q to be rejected", sql)
	}
}

func Test_ValidateSingleStatementSelect_BlockedKeywordInStructure_Rejected(t *testing.T) {
	for _, sql := range []string{
		"SELECT * FROM logs; CALL some_proc()",
		"SELECT * FROM read_parquet('x') AS logs, (SELECT 1) WHERE 1=1; ATTACH 'x'",
	} {
		_, err := segments.ValidateSingleStatementSelect(sql)
		assert.Error(t, err, "expected %q to be rejected", sql)
	}
}

// Log messages often contain "delete", "update", "set" or "create"; inside
// quoted data they must not trip the keyword blocklist.
func Test_ValidateSingleStatementSelect_KeywordInsideStringLiteral_NotFalselyRejected(t *testing.T) {
	for _, sql := range []string{
		`SELECT * FROM logs WHERE message = 'delete the cache'`,
		`SELECT * FROM logs WHERE message = 'update config and reset session'`,
		`SELECT * FROM logs WHERE message = 'create a new session'`,
		`SELECT * FROM logs WHERE message = 'call the payments API'`,
		`SELECT * FROM logs WHERE message = 'insert token into header'`,
	} {
		_, err := segments.ValidateSingleStatementSelect(sql)
		assert.NoError(t, err, "expected %q to be accepted", sql)
	}
}

func Test_ValidateSingleStatementSelect_ComplexRealQuery_Accepted(t *testing.T) {
	stmt, err := segments.ValidateSingleStatementSelect(`
		SELECT level, count(*) AS cnt
		FROM logs
		WHERE timestamp >= '2026-01-01' AND message LIKE '%timeout%'
		GROUP BY level
		ORDER BY cnt DESC
		LIMIT 10
	`)
	require.NoError(t, err)
	assert.Contains(t, stmt, "GROUP BY level")
}

// This payload once passed as one SELECT and dropped a table: the quote
// inside the "--" comment desynced quote tracking, hiding the ';' and DROP.
func Test_ValidateSingleStatementSelect_CommentHidingSemicolonAndKeyword_Rejected(t *testing.T) {
	_, err := segments.ValidateSingleStatementSelect("SELECT 1 -- '\n; DROP TABLE logs; -- '")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "comments")
}

func Test_ValidateSingleStatementSelect_BlockCommentHidingSemicolon_Rejected(t *testing.T) {
	_, err := segments.ValidateSingleStatementSelect("SELECT 1 /* ' */ ; DROP TABLE logs")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "comments")
}

func Test_ValidateSingleStatementSelect_TrailingLineComment_Rejected(t *testing.T) {
	_, err := segments.ValidateSingleStatementSelect("SELECT 1 -- just a harmless comment")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "comments")
}

// "--" or "/*" inside a string value is data, not a SQL comment.
func Test_ValidateSingleStatementSelect_DashDashInsideStringLiteral_NotRejected(t *testing.T) {
	stmt, err := segments.ValidateSingleStatementSelect(
		`SELECT * FROM logs WHERE message = 'error -- retrying'`,
	)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM logs WHERE message = 'error -- retrying'`, stmt)
}

// DuckDB ends these strings somewhere other than a '...'-only scanner
// thinks, so the ';' and SET after them used to pass as quoted data.
func Test_ValidateSingleStatementSelect_NonStandardStringsHidingStatement_Rejected(t *testing.T) {
	for _, sql := range []string{
		`SELECT E'\''; SET memory_limit='64GB'; SELECT ''`,
		`SELECT e'\''; SET memory_limit='64GB'; SELECT ''`,
		`SELECT 1e'\''; SET memory_limit='64GB'; SELECT ''`,
		`SELECT $$'$$; SET memory_limit='64GB'; SELECT $$'$$`,
		`SELECT $q$'$q$; SET memory_limit='64GB'; SELECT $q$'$q$`,
		`SELECT $1`,
		`SELECT * FROM logs WHERE message = 'unterminated`,
		`SELECT * FROM logs WHERE "message = 1`,
	} {
		_, err := segments.ValidateSingleStatementSelect(sql)
		assert.Error(t, err, "expected %q to be rejected", sql)
	}
}

func Test_ValidateSingleStatementSelect_TypedLiteralsAndDollarInStrings_Accepted(t *testing.T) {
	for _, sql := range []string{
		`SELECT * FROM logs WHERE timestamp >= TIMESTAMP '2026-01-01 00:00:00'`,
		`SELECT * FROM logs WHERE timestamp >= DATE'2026-01-01'`,
		`SELECT json_extract(fields, '$.user') FROM logs WHERE message = 'cost $5'`,
		`SELECT * FROM logs WHERE message LIKE '%\%'`,
	} {
		_, err := segments.ValidateSingleStatementSelect(sql)
		assert.NoError(t, err, "expected %q to be accepted", sql)
	}
}

func Test_ParsesAsSingleSelect_UsesDuckDBParser(t *testing.T) {
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	require.NoError(t, segments.ParsesAsSingleSelect(db, "SELECT level, count(*) FROM logs GROUP BY level"))
	require.NoError(t, segments.ParsesAsSingleSelect(db, "WITH x AS (SELECT 1) SELECT * FROM x"))

	for _, statement := range []string{
		"SELECT 1; SELECT 2",
		"SET memory_limit = '64GB'",
		`SELECT E'\''; SET memory_limit='64GB'; SELECT ''`,
		"SELECT * FROM logs WHERE message = 'unterminated",
	} {
		assert.Error(t, segments.ParsesAsSingleSelect(db, statement), "expected %q to be rejected", statement)
	}
}

func Test_ValidateSingleStatementSelect_BlockCommentMarkersInsideStringLiteral_NotRejected(t *testing.T) {
	stmt, err := segments.ValidateSingleStatementSelect(
		`SELECT * FROM logs WHERE message = '/* not a comment */'`,
	)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM logs WHERE message = '/* not a comment */'`, stmt)
}
