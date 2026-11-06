package sharedconfig

import (
	"mime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A rule name can't add headers to alert mail: control characters become
// spaces and the subject is RFC 2047-encoded.
func Test_EmailMessage_RuleNameCannotInjectHeaders(t *testing.T) {
	message := message("alerts@example.com", []string{"a@example.com"},
		"[FIRING] disk\rBcc: x@evil.test\r\nX-Evil: 1 ünïcode", "line one\nline two")
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
