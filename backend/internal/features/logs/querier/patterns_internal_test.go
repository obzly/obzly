package logs_querier

import (
	"database/sql"
	"strings"
	"testing"

	logs_core "obzly/internal/features/logs/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// patternCorpus exercises every rule in logs_core.PatternRules, their
// interactions (an IP next to a port, a timestamp that would otherwise be
// chopped into numbers), and the no-op cases (words with digits in them).
var patternCorpus = []string{
	// Past PatternMaxChars (code points, multi-byte ones included): both
	// engines template the same first 1024 characters.
	strings.Repeat("é", 1000) + " id 12345 " + strings.Repeat("x", 600) + " tail 999",
	`192.168.67.250 - - [10/Feb/2026 14:48:12] "GET /convert?from=USD&to=GBP HTTP/1.1" 200 -`,
	`192.168.68.44:8080 - - [10/Feb/2026 14:48:06] "GET /healthz/live HTTP/1.1" 200 -`,
	`Converting currency`,
	`request 3f2b1c9e-8a7d-4e6f-9b0a-1c2d3e4f5a6b took 12.5ms`,
	`2026-09-23T02:01:19.599Z level=info msg="flushed segment" rows=1042`,
	`2026-09-23 02:01:19,599+02:00 flushed`,
	`trace 4bf92f3577b34da6a3ce929d0e0e4736 span 00f067aa0ba902b7`,
	`pointer 0xDEADbeef freed`,
	`v2 api upgraded to k8s 1.31 on node ip-10-0-1-5`,
	`user_42 logged in`,
	``,
	`ünïcödé 123 ✓`,
}

// Test_PatternTemplate_AgreesWithDuckDB guards against drift between the Go
// pattern (Live Tail) and the DuckDB one (stored search): a pattern_hash filter
// must match the same logs in both.
func Test_PatternTemplate_AgreesWithDuckDB(t *testing.T) {
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	for _, message := range patternCorpus {
		var sqlTemplate, sqlHash string
		row := db.QueryRow(
			"SELECT "+logs_core.PatternTemplateSQL("m")+", "+logs_core.PatternHashSQL("m")+" FROM (SELECT ?::VARCHAR AS m)",
			message,
		)
		require.NoError(t, row.Scan(&sqlTemplate, &sqlHash), message)

		assert.Equal(t, logs_core.MessagePattern(message), sqlTemplate, "template of %q", message)
		assert.Equal(t, logs_core.PatternHash(message), sqlHash, "hash of %q", message)
	}
}

func Test_MessagePattern_MasksVariableParts(t *testing.T) {
	cases := map[string]string{
		`192.168.67.250 - - [10/Feb/2026 14:48:12] "GET /convert?from=USD&to=GBP HTTP/1.1" 200 -`: `<ip> - - [<num>/Feb/<num> <num>:<num>:<num>] "GET /convert?from=USD&to=GBP HTTP/<num>" <num> -`,
		`request 3f2b1c9e-8a7d-4e6f-9b0a-1c2d3e4f5a6b took 12.5ms`:                                `request <uuid> took <num>`,
		`2026-09-23T02:01:19.599Z level=info rows=1042`:                                           `<ts> level=info rows=<num>`,
		`trace 4bf92f3577b34da6a3ce929d0e0e4736 span 00f067aa0ba902b7`:                            `trace <hex> span <hex>`,
		`pointer 0xDEADbeef freed`:                                                                `pointer <hex> freed`,
		`user_42 logged in`:                                                                       `user_42 logged in`,
	}
	for message, want := range cases {
		assert.Equal(t, want, logs_core.MessagePattern(message), message)
	}

	// Durations with a unit collapse too.
	assert.Equal(t, logs_core.PatternHash("took 12.5ms"), logs_core.PatternHash("took 3s"))

	// Two lines differing only in their variable parts share a pattern.
	assert.Equal(t,
		logs_core.PatternHash(`192.168.67.250 - - [10/Feb/2026 14:48:12] "GET /healthz/live HTTP/1.1" 200 -`),
		logs_core.PatternHash(`10.0.0.9 - - [11/Feb/2026 09:01:59] "GET /healthz/live HTTP/1.1" 503 -`),
	)
}
