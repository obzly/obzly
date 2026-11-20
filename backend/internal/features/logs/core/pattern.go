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

// PatternMaxChars: a template keeps its first this many code points (as
// DuckDB's left() counts): the start tells a message's kind apart.
const PatternMaxChars = 1024

// patternInputChars: the message's first this many code points are masked,
// then the template is cut to PatternMaxChars. Version 1 cut the message
// first, so where the cut fell moved with the values before it; rule cost
// grows with length on every ingested log.
const patternInputChars = 4096

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
	// Version 2 (PatternVersion) runs on version 1's templates. A template
	// none of these change keeps its hash, so busy patterns keep their
	// history; long messages with JSON payloads, names and generated IDs
	// stop making a pattern per line (1.4 M patterns in 3.6 h of 27 M lines).
	{`[A-Za-z0-9._%+*<>-]+@[A-Za-z0-9*<>-]+(?:\.[A-Za-z0-9*<>-]+)*\.[A-Za-z]{2,}`, "<email>"},
	// A word of 6 or more characters with a digit left after the number
	// rule: generated IDs ("CTSOMRC042", "Plan_2D_Gold"), not "utf8" or "k8s".
	{`\b(?:\w{5,}\d\w*|\w\d\w{4,}|\w{2}\d\w{3,}|\w{3}\d\w{2,}|\w{4}\d\w+|\d\w{5,})\b`, "<id>"},
	// A Kubernetes pod or ReplicaSet suffix: its generator's alphabet has
	// no vowels and no 0, 1 or 3.
	{`-[bcdfghjklmnpqrstvwxz2456789]{5}\b`, "-<pod>"},
	// A quoted value after a colon (JSON, "key": "value") unless it is an
	// upper-case constant with an underscore ("action": "QUERY_STARTED");
	// short codes ("EUR", "US") vary per line. Then a value cut off at the
	// end of the input.
	{`:\s*"(?:[^"\\]|\\.)*[^A-Z_"\\](?:[^"\\]|\\.)*"`, ":<str>"},
	{`:\s*"[A-Z]*"`, ":<str>"},
	{`:\s*"[^"]*$`, ":<str>"},
	// An unquoted key=value value, unless an upper-case constant with an
	// underscore; quoted ones (logfmt's msg="...") stay.
	{`=[^\s,;|&"'()\[\]{}<>]*[^A-Z_\s,;|&"'()\[\]{}<>][^\s,;|&"'()\[\]{}<>]*`, "=<v>"},
	{`=[A-Z]+\b`, "=<v>"},
}

// PatternVersion is the version of PatternRules; patternRulesV1 rules made
// version 1. Logs keep the hash they were stored with, so comparisons that
// reach back before a project's PatternVersionSince skip the patterns
// version 2 regrouped (PatternRegrouped).
const PatternVersion = 2

// patternRulesV1: PatternRules' first rules, version 1's.
const patternRulesV1 = 6

// patternRuleID is PatternRules' index of the word-with-a-digit rule, which
// maskIDs applies without the regex.
const patternRuleID = 7

var compiledPatternRules = func() []*regexp.Regexp {
	compiled := make([]*regexp.Regexp, len(PatternRules))
	for i, rule := range PatternRules {
		compiled[i] = regexp.MustCompile(rule.Regex)
	}
	return compiled
}()

// patternRuleNeeds holds, per rule, text the rule can't match without, so a
// line lacking it skips that rule's scan: every log is hashed at ingest, on
// the shard's one goroutine, and the regexes were most of a worker's CPU.
// Each needed piece is part of every match, so skipping never changes a
// template (Test_MessagePattern_FastPathMatchesTheRegexes).
var patternRuleNeeds = []func(string) bool{
	has("-"), func(t string) bool { return strings.Contains(t, "-") && strings.Contains(t, ":") }, has("."),
	func(t string) bool { return strings.Contains(t, "0x") || strings.Contains(t, "0X") }, nil, nil,
	has("@"), nil, has("-"),
	has(`"`), has(`"`), has(`"`),
	has("="), has("="),
}

// MessagePattern returns message's template (see PatternRules), at most
// PatternMaxChars code points.
func MessagePattern(message string) string {
	return cutCodePoints(
		applyPatternRules(cutCodePoints(message, patternInputChars), len(PatternRules)),
		PatternMaxChars,
	)
}

// MessagePatternV1 is message's version 1 template.
func MessagePatternV1(message string) string {
	return applyPatternRules(cutCodePoints(message, PatternMaxChars), patternRulesV1)
}

// PatternRegrouped reports whether version 2 gives message another pattern
// than version 1 did: its hash is new since PatternVersionSince, and its
// version 1 hash stopped there.
func PatternRegrouped(message string) bool {
	return MessagePatternV1(message) != MessagePattern(message)
}

// applyPatternRules runs PatternRules' first n rules over pattern.
func applyPatternRules(pattern string, n int) string {
	for i, re := range compiledPatternRules[:n] {
		if needs := patternRuleNeeds[i]; needs != nil && !needs(pattern) {
			continue
		}
		if i == patternRuleID {
			pattern = maskIDs(pattern)
			continue
		}
		pattern = re.ReplaceAllLiteralString(pattern, PatternRules[i].Replacement)
	}
	return pattern
}

func has(piece string) func(string) bool {
	return func(text string) bool { return strings.Contains(text, piece) }
}

// maskIDs is the <id> rule without the regex: a whole word (a run of ASCII
// letters, digits and underscores, as RE2's \w and \b see it) of 6 or more
// characters holding a digit becomes "<id>".
func maskIDs(text string) string {
	var out strings.Builder
	last := 0
	for i := 0; i < len(text); {
		if !isWordByte(text[i]) {
			i++
			continue
		}
		start, digit := i, false
		for i < len(text) && isWordByte(text[i]) {
			digit = digit || (text[i] >= '0' && text[i] <= '9')
			i++
		}
		if digit && i-start >= 6 {
			if last == 0 {
				out.Grow(len(text))
			}
			out.WriteString(text[last:start])
			out.WriteString(PatternRules[patternRuleID].Replacement)
			last = i
		}
	}
	if last == 0 {
		return text
	}
	out.WriteString(text[last:])
	return out.String()
}

func isWordByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// cutCodePoints is text's first limit code points.
func cutCodePoints(text string, limit int) string {
	if len(text) <= limit {
		return text // at most limit bytes, so at most that many code points
	}
	count := 0
	for i := range text {
		if count == limit {
			return text[:i]
		}
		count++
	}
	return text
}

// PatternHashes is message's hash under PatternVersion, then under version 1
// when that differs: what a rule saved before the version change matches.
func PatternHashes(message string) []string {
	current := PatternHash(message)
	if v1 := PatternHashOf(MessagePatternV1(message)); v1 != current {
		return []string{current, v1}
	}
	return []string{current}
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
	expr := fmt.Sprintf("left(COALESCE(%s, ''), %d)", column, patternInputChars)
	for _, rule := range PatternRules {
		expr = fmt.Sprintf(
			"regexp_replace(%s, '%s', '%s', 'g')",
			expr, sqlLiteral(rule.Regex), sqlLiteral(rule.Replacement),
		)
	}
	return fmt.Sprintf("left(%s, %d)", expr, PatternMaxChars)
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
