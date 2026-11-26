package logs_core

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The guards and maskIDs are only a faster way to run PatternRules: every
// template must equal what the plain regexes give, or stored hashes would
// stop matching search, Live Tail and the DuckDB twin.
func Test_MessagePattern_FastPathMatchesTheRegexes(t *testing.T) {
	messages := []string{
		"",
		"plain words only",
		"route plan_2D_Gold to merchant CTSOMRC042 denied",
		"abcde1 a1bcde ab1cde abc1de abcd1e abcde1 1abcde abc12 abcdef",
		"x_____9 9_____ é1abcdef abcdef1é <num>abc123 abc123<num>",
		"pod checkout-7d6d9469c8-x6vd2 restarted and -bcdfg -bcdfgh -BCDFG",
		`{"a":"ABC","b":"A_B","c":"x","d":"","e": "QUERY_DONE","f":"esc\"aped"} {"cut":"open`,
		"k=v K=V K_V=A_B k=12 k= k=\"q\" x==y a=B1 a=ABC_",
		"mail j***9@f****.example.com and ada@example.com at 0xDEAD and 0Xbeef",
		"2026-09-23T02:01:19.599Z 10.0.0.1:8080 3f2b1c9e-8a7d-4e6f-9b0a-1c2d3e4f5a6b 4bf92f3577b34da6a3ce929d0e0e4736",
		strings.Repeat("é", 1000) + " id 12345 " + strings.Repeat("x", 600) + " tail 999",
		strings.Repeat("ab1cdef ", 700),
		`203.0.113.7 - - [20/Nov/2026:05:03:27 +0000] "GET /a HTTP/1.1" 200 5 "-" "UA"`,
		`[2026-11-20T05:03:27.123Z] "GET /healthz HTTP/1.1" 503 - 0`,
		`not an access log: x] "GET / HTTP/1.1" 200`,
		`Cache miss for "A B" in "C"` + "\t\"tab\"",
	}
	rng := rand.New(rand.NewSource(42))
	pieces := []string{
		"a", "Z", "q", "_", "0", "1", "7", "9", "x", "X", "f", "e", "-", ":", ".", "@", `"`, `\`, "=", " ", ",",
		"<", ">", "é", "{", "}", "T", "Z", "ab12cd", "0x1F", "2026-01-02", "12:34:56", "10.0.0.1", "ORD",
		"ABC_DEF", "-bcdfg", "user@example.com", `":"`, `": "`, "=v", "\n",
		`] "`, "[", "]", `"GET `, `" 200 `, `" 503 `, "- - [", " HTTP/1.1", ` "`, `":`,
	}
	for i := 0; i < 200_000; i++ {
		var b strings.Builder
		for n := rng.Intn(24); n >= 0; n-- {
			b.WriteString(pieces[rng.Intn(len(pieces))])
		}
		messages = append(messages, b.String())
	}

	for _, message := range messages {
		for version := range patternVersions {
			require.Equal(t, regexPattern(message, version), MessagePatternOf(message, version), message)
		}
	}
}

func BenchmarkMessagePattern(b *testing.B) {
	messages := []string{
		`{"level":"info","ts":"2026-11-20T05:03:27.123Z","msg":"request completed","method":"GET","path":"/api/v1/orders/ORD-00412233","status":200,"duration_ms":12.4,"user_id":4821331,"trace_id":"4bf92f3577b34da6a3ce929d0e0e4736"}`,
		`ts=2026-11-20T05:03:27Z level=info msg="payment authorized" payment_id=pay_3KF92JQAB amount=129.95 currency=NZD card_brand=VISA attempt=1`,
		`203.0.113.7 - - [20/Nov/2026:05:03:27 +0000] "GET /products/blue-merino-scarf HTTP/1.1" 200 5123 "https://www.example.com/" "Mozilla/5.0 (X11; Linux x86_64; rv:133.0) Gecko/20100101 Firefox/133.0" 0.012`,
		`Cache miss for product "Blue Merino Scarf" in category "Winter Accessories"`,
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = MessagePattern(messages[i%len(messages)])
	}
}

// regexPattern is MessagePatternOf as the plain regexes give it.
func regexPattern(message string, version int) string {
	spec := patternVersions[version]
	pattern := cutCodePoints(message, spec.inputChars)
	if spec.lineTemplates {
		for i, re := range compiledLineTemplates {
			if re.MatchString(pattern) {
				return cutCodePoints(re.ReplaceAllString(pattern, PatternLineTemplates[i].Replacement), PatternMaxChars)
			}
		}
	}
	for _, i := range spec.rules {
		pattern = compiledPatternRules[i].ReplaceAllString(pattern, PatternRules[i].Replacement)
	}
	return cutCodePoints(pattern, PatternMaxChars)
}
