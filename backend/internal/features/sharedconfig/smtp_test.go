package sharedconfig

import (
	"context"
	"mime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A rule name can't add headers to alert mail: control characters become
// spaces and the subject is RFC 2047-encoded.
func Test_EmailMessage_RuleNameCannotInjectHeaders(t *testing.T) {
	from, to, err := envelope("alerts@example.com", []string{"a@example.com"})
	require.NoError(t, err)
	message := message(from, to, "[FIRING] disk\rBcc: x@evil.test\r\nX-Evil: 1 ünïcode", "line one\nline two")
	headers, body, found := strings.Cut(message, "\r\n\r\n")
	require.True(t, found)
	lines := strings.Split(headers, "\r\n")
	require.Len(t, lines, 5, headers)
	assert.True(t, strings.HasPrefix(lines[2], "Subject: =?utf-8?q?"), lines[2])
	assert.NotContains(t, headers, "\rBcc")
	assert.NotContains(t, headers, "\nX-Evil")
	decoded, err := new(mime.WordDecoder).DecodeHeader(strings.TrimPrefix(lines[2], "Subject: "))
	require.NoError(t, err)
	assert.Equal(t, "[FIRING] disk Bcc: x@evil.test  X-Evil: 1 ünïcode", decoded)
	assert.Equal(t, "line one\r\nline two\r\n", body)
}

// An address reaches the headers only after it parses, so a recipient or
// sender that skipped validation can't add a Bcc; Send fails before dialing.
func Test_SendMail_AddressWithHeaderLine_IsRefusedBeforeDialing(t *testing.T) {
	relay := &SMTP{Host: "127.0.0.1", Port: 1, From: "alerts@example.com", TLS: "starttls"}
	for _, to := range []string{"a@example.com\r\nBcc: x@evil.test", "a@example.com\nBcc: x@evil.test"} {
		err := relay.Send(context.Background(), []string{to}, "subject", "body")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "isn't a valid email address")
	}

	relay.From = "alerts@example.com\r\nBcc: x@evil.test"
	err := relay.Send(context.Background(), []string{"a@example.com"}, "subject", "body")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "From address")
}

func Test_EmailMessage_FromWithDisplayName_IsFormattedAndEncoded(t *testing.T) {
	from, to, err := envelope("Obzly Alerts <alerts@example.com>", []string{"Ops Team <ops@example.com>", "b@example.com"})
	require.NoError(t, err)
	assert.Equal(t, "alerts@example.com", from.Address)

	headers, _, _ := strings.Cut(message(from, to, "subject", "body"), "\r\n\r\n")
	lines := strings.Split(headers, "\r\n")
	assert.Equal(t, `From: "Obzly Alerts" <alerts@example.com>`, lines[0])
	assert.Equal(t, `To: "Ops Team" <ops@example.com>, <b@example.com>`, lines[1])
}
