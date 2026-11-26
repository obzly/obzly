package logs_core

import (
	"crypto/md5" //nolint:gosec // a stable, short content fingerprint - not used for security
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
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
// ids) go before the catch-all number rule. Replacements insert literally,
// but for ${n} group references (PatternTemplateSQL writes DuckDB's \n).
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
	// Version 3: a quoted HTTP request line keeps its method only, then a
	// quoted value after a space: names and messages quoted in prose ("Cache
	// miss for product "Alpine Denim Scarf""). Not a JSON key (a colon
	// follows), a value after a colon or "=" (the rules above decided those),
	// or one already holding a placeholder ("GET <path>", "took <num>").
	{`"([A-Z]{3,7}) [^"\s]* HTTP/<num>"`, `"${1} <path>"`},
	{`([^:=\s])\s"(?:[^"\\<]|\\.)*"([^:]|$)`, `${1} "<str>"${2}`},
}

// PatternLineTemplates (version 3) give a whole message one template when
// it matches, before PatternRules: an access log's path, sizes, referrer
// and user agent made a pattern per line (two thirds of a lab Site's
// patterns were seen once), and the number rule would mask the status.
// The method and status class (2xx, 5xx) stay. ${n} refers to a group (Go);
// PatternTemplateSQL writes it as DuckDB's \n.
var PatternLineTemplates = []PatternRule{
	// Common and combined log format: nginx, ingress-nginx, Apache, Python.
	{`(?s)^\S+ \S+ \S+ \[[^\]]*\] "([A-Z]+) [^"]*" ([1-5])\d\d\b.*`, `<ip> - - [<ts>] "${1} <path>" ${2}xx`},
	// Envoy's default access log.
	{`(?s)^\[[^\]]*\] "([A-Z]+) [^"]*" ([1-5])\d\d\b.*`, `[<ts>] "${1} <path>" ${2}xx`},
}

// PatternVersion is the current version of the rules (patternVersions).
// Logs keep the hash they were stored with, so comparisons that reach back
// before a version started skip the patterns it regrouped
// (PatternRegrouped).
const PatternVersion = 3

// patternVersion is one version of the rules: how much of a message it
// masks, whether PatternLineTemplates come first, and which PatternRules
// (indices) run, in order.
type patternVersion struct {
	inputChars    int
	lineTemplates bool
	rules         []int
}

var patternVersions = map[int]patternVersion{
	1: {inputChars: PatternMaxChars, rules: ruleRange(0, 6)},
	2: {inputChars: patternInputChars, rules: ruleRange(0, 14)},
	3: {inputChars: patternInputChars, lineTemplates: true, rules: ruleRange(0, 16)},
}

// patternRuleID is PatternRules' index of the word-with-a-digit rule, which
// maskIDs applies without the regex.
const patternRuleID = 7

var compiledPatternRules = compileRules(PatternRules)

var compiledLineTemplates = compileRules(PatternLineTemplates)

// lineTemplateNeeds: text a line template can't match without, so most
// lines skip its regex (patternRuleNeeds' reason).
var lineTemplateNeeds = []func(string) bool{
	func(t string) bool { return strings.Contains(t, `] "`) },
	func(t string) bool { return strings.HasPrefix(t, "[") && strings.Contains(t, `] "`) },
}

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
	has(" HTTP/"), has(`"`),
}

// MessagePattern returns message's template under PatternVersion, at most
// PatternMaxChars code points.
func MessagePattern(message string) string {
	return MessagePatternOf(message, PatternVersion)
}

// MessagePatternOf is message's template under the given version.
func MessagePatternOf(message string, version int) string {
	spec := patternVersions[version]
	input := cutCodePoints(message, spec.inputChars)
	if spec.lineTemplates {
		for i, re := range compiledLineTemplates {
			if lineTemplateNeeds[i](input) && re.MatchString(input) {
				return cutCodePoints(re.ReplaceAllString(input, PatternLineTemplates[i].Replacement), PatternMaxChars)
			}
		}
	}
	return cutCodePoints(applyPatternRules(input, spec.rules), PatternMaxChars)
}

// PatternRegrouped reports whether an earlier version that a baseline
// starting at from reaches gave message another pattern: its hash then
// stopped where the current one started, so against that baseline it
// looks new (or, under the old hash, dropped).
func PatternRegrouped(message string, from time.Time) bool {
	current := MessagePattern(message)
	for _, version := range PatternVersionsReached(from) {
		if MessagePatternOf(message, version) != current {
			return true
		}
	}
	return false
}

// applyPatternRules runs the PatternRules at indices over pattern.
func applyPatternRules(pattern string, indices []int) string {
	for _, i := range indices {
		if needs := patternRuleNeeds[i]; needs != nil && !needs(pattern) {
			continue
		}
		if i == patternRuleID {
			pattern = maskIDs(pattern)
			continue
		}
		rule := PatternRules[i]
		if strings.Contains(rule.Replacement, "${") {
			pattern = compiledPatternRules[i].ReplaceAllString(pattern, rule.Replacement)
		} else {
			pattern = compiledPatternRules[i].ReplaceAllLiteralString(pattern, rule.Replacement)
		}
	}
	return pattern
}

func compileRules(rules []PatternRule) []*regexp.Regexp {
	compiled := make([]*regexp.Regexp, len(rules))
	for i, rule := range rules {
		compiled[i] = regexp.MustCompile(rule.Regex)
	}
	return compiled
}

func ruleRange(from, to int) []int {
	indices := make([]int, 0, to-from)
	for i := from; i < to; i++ {
		indices = append(indices, i)
	}
	return indices
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

// PatternHashes is message's hash under PatternVersion, then under each
// earlier version where it differs, newest first: what a rule saved before
// a version change matches.
func PatternHashes(message string) []string {
	hashes := []string{PatternHash(message)}
	for version := PatternVersion - 1; version >= 1; version-- {
		if hash := PatternHashOf(MessagePatternOf(message, version)); !slices.Contains(hashes, hash) {
			hashes = append(hashes, hash)
		}
	}
	return hashes
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
	spec := patternVersions[PatternVersion]
	input := fmt.Sprintf("left(COALESCE(%s, ''), %d)", column, spec.inputChars)
	expr := input
	for _, i := range spec.rules {
		expr = fmt.Sprintf(
			"regexp_replace(%s, '%s', '%s', 'g')",
			expr, sqlLiteral(PatternRules[i].Regex), sqlLiteral(sqlReplacement(PatternRules[i].Replacement)),
		)
	}
	if spec.lineTemplates {
		var cases strings.Builder
		for _, line := range PatternLineTemplates {
			fmt.Fprintf(&cases, "WHEN regexp_matches(%s, '%s') THEN regexp_replace(%s, '%s', '%s') ",
				input, sqlLiteral(line.Regex), input, sqlLiteral(line.Regex),
				sqlLiteral(sqlReplacement(line.Replacement)))
		}
		expr = "CASE " + cases.String() + "ELSE " + expr + " END"
	}
	return fmt.Sprintf("left(%s, %d)", expr, PatternMaxChars)
}

// sqlReplacement writes a Go replacement's ${n} group references as
// DuckDB's \n.
func sqlReplacement(replacement string) string {
	return groupReference.ReplaceAllString(replacement, `\$1`)
}

var groupReference = regexp.MustCompile(`\$\{(\d)\}`)

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
