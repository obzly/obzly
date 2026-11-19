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
	// Version 2 rules.
	`order received: {"orderId":"ORD7731X","customer":{"name":"Ada Lovelace","email":"ada@example.com","city":"Contoso"},"status":"PAYMENT_PENDING","items":2}`,
	`mail sent to j***9@f****.example.com`,
	`route plan_2D_Gold to merchant CTSOMRC042 denied`,
	`pod checkout-7d6d9469c8-x6vd2 restarted`,
	`convert fromCurrency=EUR toCurrency=USD amount=104.41 psp=fabrikam-acquiring method=POST`,
	`{"action": "QUERY_DONE", "detail": "took long`,
	strings.Repeat("a", 30) + ` {"note":"` + strings.Repeat("free text ", 500) + `"} end`,
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
		`192.168.67.250 - - [10/Feb/2026 14:48:12] "GET /convert?from=USD&to=GBP HTTP/1.1" 200 -`: `<ip> - - [<num>/Feb/<num> <num>:<num>:<num>] "GET /convert?from=<v>&to=<v> HTTP/<num>" <num> -`,
		`request 3f2b1c9e-8a7d-4e6f-9b0a-1c2d3e4f5a6b took 12.5ms`:                                `request <uuid> took <num>`,
		`2026-09-23T02:01:19.599Z level=info rows=1042`:                                           `<ts> level=<v> rows=<num>`,
		`trace 4bf92f3577b34da6a3ce929d0e0e4736 span 00f067aa0ba902b7`:                            `trace <hex> span <hex>`,
		`pointer 0xDEADbeef freed`:                                                                `pointer <hex> freed`,
		`user_42 logged in`:                                                                       `<id> logged in`,
		`order received: {"orderId":"ORD7731X","customer":{"name":"Ada Lovelace","email":"ada@example.com"},"status":"PAYMENT_PENDING"}`: `order received: {"orderId":<str>,"customer":{"name":<str>,"email":<str>},"status":"PAYMENT_PENDING"}`,
		`mail sent to j***9@f****.example.com`:                                                         `mail sent to <email>`,
		`route plan_2D_Gold to merchant CTSOMRC042 denied`:                                             `route <id> to merchant <id> denied`,
		`pod checkout-7d6d9469c8-x6vd2 restarted`:                                                      `pod checkout-<id>-<pod> restarted`,
		`convert fromCurrency=EUR amount=104.41 psp=fabrikam-acquiring method=POST code=CARD_DECLINED`: `convert fromCurrency=<v> amount=<num> psp=<v> method=<v> code=CARD_DECLINED`,
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

func Test_MessagePattern_WhenVersion2ChangesNothing_KeepsTheVersion1Hash(t *testing.T) {
	for _, message := range []string{
		`2026-11-19 19:55:58 - DB query initiated`,
		`at org.example.web.Filter.doFilter(Filter.java:110)`,
		`request 3f2b1c9e-8a7d-4e6f-9b0a-1c2d3e4f5a6b took 12.5ms`,
	} {
		assert.False(t, logs_core.PatternRegrouped(message), message)
		assert.Equal(t, logs_core.PatternHashOf(logs_core.MessagePatternV1(message)), logs_core.PatternHash(message), message)
		assert.Equal(t, []string{logs_core.PatternHash(message)}, logs_core.PatternHashes(message), message)
	}
}

func Test_MessagePattern_WhenValuesDiffer_GroupsVersion2Lines(t *testing.T) {
	// Lines that differ in names, IDs and JSON values: one pattern each in
	// version 1, one between them in version 2.
	lines := []string{
		`order received: {"orderId":"ORD7731X","customer":{"name":"Ada Lovelace","city":"Contoso"},"status":"PAYMENT_PENDING"}`,
		`order received: {"orderId":"ORD9020Q","customer":{"name":"Grace Hopper","city":"Fabrikam"},"status":"PAYMENT_PENDING"}`,
	}
	assert.NotEqual(t, logs_core.PatternHashOf(logs_core.MessagePatternV1(lines[0])),
		logs_core.PatternHashOf(logs_core.MessagePatternV1(lines[1])))
	assert.Equal(t, logs_core.PatternHash(lines[0]), logs_core.PatternHash(lines[1]))
	assert.True(t, logs_core.PatternRegrouped(lines[0]))
	assert.Len(t, logs_core.PatternHashes(lines[0]), 2)

	// Upper-case constants still tell kinds apart.
	assert.NotEqual(t,
		logs_core.PatternHash(`{"action": "QUERY_STARTED", "host": "worker-a"}`),
		logs_core.PatternHash(`{"action": "QUERY_DONE", "host": "worker-b"}`))
}

func Test_MessagePattern_WhenMessagesPassTheLimit_CutsAfterMasking(t *testing.T) {
	// Values of different lengths before the cut no longer move it.
	body := func(name string) string {
		return `{"customer":"` + name + `",` + strings.Repeat(`"k":"some value",`, 80) + `"end":"x"}`
	}
	short, long := body("Al"), body(strings.Repeat("Bartholomew ", 20))
	require.Greater(t, len(short), logs_core.PatternMaxChars)
	assert.Equal(t, logs_core.PatternHash(short), logs_core.PatternHash(long))
	assert.LessOrEqual(t, len([]rune(logs_core.MessagePattern(long))), logs_core.PatternMaxChars)
}

func Test_WithoutRegrouped_DropsRegroupedNewAndRetiredDroppedPatterns(t *testing.T) {
	regrouped := `order placed {"customer":"Ada Lovelace"}`
	steady := `cache warmed in <num> ms`
	grown := []logs_core.PatternChangeDTO{
		{Hash: logs_core.PatternHash(regrouped), Sample: regrouped, Change: logs_core.PatternChangeNew},
		{Hash: logs_core.PatternHash(steady), Sample: steady, Change: logs_core.PatternChangeNew},
		{Hash: logs_core.PatternHash(regrouped), Sample: regrouped, Change: logs_core.PatternChangeSpike},
		{Hash: "0123456789ab", Change: logs_core.PatternChangeNew},
	}
	falling := []logs_core.PatternChangeDTO{
		// Stored under version 1: its logs have another hash now.
		{Hash: logs_core.PatternHashOf(logs_core.MessagePatternV1(regrouped)), Sample: regrouped},
		{Hash: logs_core.PatternHash(steady), Sample: steady},
	}

	keptGrown, keptFalling := withoutRegrouped(grown, falling)

	assert.Equal(t, []string{steady, regrouped, ""}, []string{
		keptGrown[0].Sample, keptGrown[1].Sample, keptGrown[2].Sample,
	}, "the regrouped new pattern goes; a spike and a change without a sample stay")
	require.Len(t, keptFalling, 1)
	assert.Equal(t, steady, keptFalling[0].Sample)
}
