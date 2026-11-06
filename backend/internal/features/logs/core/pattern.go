package logs_core

import (
	"crypto/md5" //nolint:gosec // a stable, short content fingerprint - not used for security
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// PatternHashField is the field a log's message pattern is filtered and
// grouped by ("pattern.hash:3f9c0a1b2c4d"). Stored at ingest; older rows
// compute it from the message, in SQL and in Live Tail alike.
const PatternHashField = "pattern_hash"

// PatternMaxChars: a pattern templates only the message's first this many
// code points (as DuckDB's left() counts): the start tells a message's kind
// apart, and rule cost grows with length on every ingested log.
const PatternMaxChars = 1024

// PatternHashLength is the hex characters of a pattern's MD5 kept as its hash
// (48 bits: enough per project, short enough for a search bar).
const PatternHashLength = 12

// PatternRule is one normalization step, applied in PatternRules order. The
// regexes are RE2, which Go and DuckDB's regexp_replace both implement, so
// stored search and Live Tail agree (Test_PatternTemplate_AgreesWithDuckDB).
type PatternRule struct {
	Regex       string
	Replacement string
}

// PatternRules masks the parts of a message that vary between otherwise
// identical lines. Order matters: wider shapes (UUIDs, timestamps, IPs, hex
// ids) go before the catch-all number rule. Replacements avoid Go's "$" and
// DuckDB's "\" group references so both insert them literally.
var PatternRules = []PatternRule{
	{`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`, "<uuid>"},
	{`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?(?:Z|[+-]\d{2}:?\d{2})?`, "<ts>"},
	{`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}(?::\d+)?\b`, "<ip>"},
	{`\b0[xX][0-9a-fA-F]+\b`, "<hex>"},
	{`\b[0-9a-fA-F]{16,}\b`, "<hex>"},
	// A number with an optional short unit ("12.5ms", "3s"); without the unit
	// every distinct duration made its own pattern.
	{`\b\d+(?:\.\d+)?(?:[a-zA-Z]{1,3})?\b`, "<num>"},
}

var compiledPatternRules = func() []*regexp.Regexp {
	compiled := make([]*regexp.Regexp, len(PatternRules))
	for i, rule := range PatternRules {
		compiled[i] = regexp.MustCompile(rule.Regex)
	}
	return compiled
}()

// MessagePattern returns the template of message's first PatternMaxChars
// characters (see PatternRules).
func MessagePattern(message string) string {
	pattern := patternInput(message)
	for i, re := range compiledPatternRules {
		pattern = re.ReplaceAllLiteralString(pattern, PatternRules[i].Replacement)
	}
	return pattern
}

// patternInput is message cut to PatternMaxChars code points.
func patternInput(message string) string {
	if len(message) <= PatternMaxChars {
		return message // at most PatternMaxChars bytes, so at most that many code points
	}
	count := 0
	for i := range message {
		if count == PatternMaxChars {
			return message[:i]
		}
		count++
	}
	return message
}

// PatternHashOf returns the PatternHashField value for an already-computed
// template (MessagePattern's output).
func PatternHashOf(pattern string) string {
	sum := md5.Sum([]byte(pattern)) //nolint:gosec // fingerprint, not security
	return hex.EncodeToString(sum[:])[:PatternHashLength]
}

// PatternHash returns the PatternHashField value for a raw message.
func PatternHash(message string) string {
	return PatternHashOf(MessagePattern(message))
}

// PatternTemplateSQL compiles PatternRules into nested DuckDB
// regexp_replace() calls over column, the SQL twin of MessagePattern. Rules
// are constants; single quotes are still doubled to keep the literal closed.
func PatternTemplateSQL(column string) string {
	expr := fmt.Sprintf("left(COALESCE(%s, ''), %d)", column, PatternMaxChars)
	for _, rule := range PatternRules {
		expr = fmt.Sprintf(
			"regexp_replace(%s, '%s', '%s', 'g')",
			expr, sqlLiteral(rule.Regex), sqlLiteral(rule.Replacement),
		)
	}
	return expr
}

// PatternHashSQL is the SQL twin of PatternHash.
func PatternHashSQL(column string) string {
	return fmt.Sprintf("left(md5(%s), %d)", PatternTemplateSQL(column), PatternHashLength)
}

// StoredPatternHashSQL is a row's pattern_hash column, else computed from
// message (COALESCE evaluates the fallback only for NULL rows).
var StoredPatternHashSQL = "COALESCE(pattern_hash, " + PatternHashSQL("message") + ")"

func sqlLiteral(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}
